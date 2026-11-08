package oauth

import (
	"fmt"
	"net/http"
	"slices"
	"strings"
	"sync/atomic"
	"time"

	"github.com/openai/tunnel-client/pkg/clientinstance"
	"github.com/openai/tunnel-client/pkg/healthstate"
)

const (
	maxActiveProbes          = 16
	maxRecentProbes          = 16
	maxResponseRequestIDSize = 128
	maxResponseHeaderNames   = 128
	maxConnectionValues      = 32
	maxConnectionBytes       = 1024
	maxProbeSequenceAttempts = 16
)

type probeType string

const (
	probeTypeWWWAuthenticate             probeType = "www_authenticate"
	probeTypeProtectedResourceMetadata   probeType = "protected_resource_metadata"
	probeTypeAuthorizationServerMetadata probeType = "authorization_server_metadata"
)

type probeDisposition string

const (
	probeDispositionPending         probeDisposition = "pending"
	probeDispositionAccepted        probeDisposition = "accepted"
	probeDispositionFallback        probeDisposition = "fallback"
	probeDispositionRetry           probeDisposition = "retry"
	probeDispositionRedirect        probeDisposition = "redirect"
	probeDispositionNotAdvertised   probeDisposition = "not_advertised"
	probeDispositionTerminalFailure probeDisposition = "terminal_failure"
	probeDispositionCancelled       probeDisposition = "cancelled"
)

var probeSequence atomic.Uint64

type probeReceipt struct {
	state    *DiscoveryState
	sequence uint64
	id       string
}

type probeObservation struct {
	probeID           string
	probeType         probeType
	method            string
	startedAt         time.Time
	responseAt        time.Time
	responseObserved  bool
	statusCode        int
	completedAt       time.Time
	disposition       probeDisposition
	responseRequestID string
}

type probeObservationState struct {
	active      map[uint64]probeObservation
	recent      [maxRecentProbes]probeObservation
	recentStart int
	recentLen   int
	limited     bool
	closed      bool
}

type discoveryHealthSnapshot struct {
	completedAt     time.Time
	optionalFailure bool
	failed          bool
	activeProbes    []healthstate.OAuthProbeObservation
	recentProbes    []healthstate.OAuthProbeObservation
	limited         bool
}

func (s *DiscoveryState) observationTime() time.Time {
	if s != nil && s.now != nil {
		return s.now().UTC()
	}
	return time.Now().UTC()
}

func nextProbeSequence() (uint64, bool) {
	for range maxProbeSequenceAttempts {
		current := probeSequence.Load()
		if current == ^uint64(0) {
			return 0, false
		}
		if probeSequence.CompareAndSwap(current, current+1) {
			return current + 1, true
		}
	}
	return 0, false
}

// beginProbe records one configured RoundTripper invocation. Recording is
// bounded and never delays an HTTP request waiting for observation capacity.
func (s *DiscoveryState) beginProbe(kind probeType, method string) *probeReceipt {
	if s == nil {
		return nil
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.observations.closed || len(s.observations.active) >= maxActiveProbes {
		s.observations.limited = true
		return nil
	}
	switch kind {
	case probeTypeWWWAuthenticate:
		kind = probeTypeWWWAuthenticate
	case probeTypeProtectedResourceMetadata:
		kind = probeTypeProtectedResourceMetadata
	case probeTypeAuthorizationServerMetadata:
		kind = probeTypeAuthorizationServerMetadata
	default:
		s.observations.limited = true
		return nil
	}
	switch method {
	case http.MethodGet:
		method = http.MethodGet
	case http.MethodPost:
		method = http.MethodPost
	default:
		method = "OTHER"
		s.observations.limited = true
	}
	sequence, ok := nextProbeSequence()
	if !ok {
		s.observations.limited = true
		return nil
	}
	id := fmt.Sprintf("%s-%016x", clientinstance.ID(), sequence)
	if s.observations.active == nil {
		s.observations.active = make(map[uint64]probeObservation, maxActiveProbes)
	}
	s.observations.active[sequence] = probeObservation{
		probeID:     id,
		probeType:   kind,
		method:      method,
		startedAt:   s.observationTime(),
		disposition: probeDispositionPending,
	}
	return &probeReceipt{state: s, sequence: sequence, id: id}
}

func (r *probeReceipt) ID() string {
	if r == nil {
		return ""
	}
	return r.id
}

// observeResponse retains only the bounded response fields used by health.
// It must be called before the response is exposed to another owner.
func (r *probeReceipt) observeResponse(status int, headers http.Header) {
	if r == nil || r.state == nil {
		return
	}
	requestID, limited := boundedResponseRequestID(headers)
	r.state.mu.Lock()
	defer r.state.mu.Unlock()
	observation, ok := r.state.observations.active[r.sequence]
	if !ok {
		return
	}
	if observation.responseObserved {
		r.state.observations.limited = true
		return
	}
	observation.responseObserved = true
	observation.responseAt = r.state.observationTime()
	if status >= 100 && status <= 999 {
		observation.statusCode = status
	} else {
		limited = true
	}
	observation.responseRequestID = requestID
	r.state.observations.active[r.sequence] = observation
	r.state.observations.limited = r.state.observations.limited || limited
}

func (r *probeReceipt) finish(disposition probeDisposition) {
	if r == nil || r.state == nil {
		return
	}
	r.state.mu.Lock()
	defer r.state.mu.Unlock()
	disposition, valid := canonicalImmediateProbeDisposition(disposition)
	if !valid {
		if _, ok := r.state.observations.active[r.sequence]; ok {
			r.state.observations.limited = true
		}
		return
	}
	r.state.finishProbeLocked(r.sequence, disposition, r.state.observationTime())
}

func canonicalImmediateProbeDisposition(disposition probeDisposition) (probeDisposition, bool) {
	switch disposition {
	case probeDispositionAccepted:
		return probeDispositionAccepted, true
	case probeDispositionFallback:
		return probeDispositionFallback, true
	case probeDispositionRetry:
		return probeDispositionRetry, true
	case probeDispositionRedirect:
		return probeDispositionRedirect, true
	default:
		return "", false
	}
}

func (s *DiscoveryState) finishProbeLocked(sequence uint64, disposition probeDisposition, completedAt time.Time) {
	observation, ok := s.observations.active[sequence]
	if !ok {
		return
	}
	delete(s.observations.active, sequence)
	observation.completedAt = completedAt
	observation.disposition = disposition
	if s.observations.recentLen < maxRecentProbes {
		index := (s.observations.recentStart + s.observations.recentLen) % maxRecentProbes
		s.observations.recent[index] = observation
		s.observations.recentLen++
		return
	}
	s.observations.recent[s.observations.recentStart] = observation
	s.observations.recentStart = (s.observations.recentStart + 1) % maxRecentProbes
	s.observations.limited = true
}

// cancelActiveProbes closes observation intake and records owner cancellation
// without changing the existing final discovery outcome or waking its waiters.
func (s *DiscoveryState) cancelActiveProbes() {
	if s == nil {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.observations.closed = true
	completedAt := s.observationTime()
	sequences := make([]uint64, 0, len(s.observations.active))
	for sequence := range s.observations.active {
		sequences = append(sequences, sequence)
	}
	slices.Sort(sequences)
	for _, sequence := range sequences {
		s.finishProbeLocked(sequence, probeDispositionCancelled, completedAt)
	}
}

func (s *DiscoveryState) healthSnapshot() discoveryHealthSnapshot {
	if s == nil {
		return discoveryHealthSnapshot{}
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	snapshot := discoveryHealthSnapshot{
		completedAt:     s.completedAt,
		optionalFailure: s.optionalFailure,
		failed:          s.err != nil,
		limited:         s.observations.limited,
	}
	sequences := make([]uint64, 0, len(s.observations.active))
	for sequence := range s.observations.active {
		sequences = append(sequences, sequence)
	}
	slices.Sort(sequences)
	for _, sequence := range sequences {
		snapshot.activeProbes = append(snapshot.activeProbes, s.observations.active[sequence].health())
	}
	for i := 0; i < s.observations.recentLen; i++ {
		index := (s.observations.recentStart + i) % maxRecentProbes
		snapshot.recentProbes = append(snapshot.recentProbes, s.observations.recent[index].health())
	}
	return snapshot
}

func (o probeObservation) health() healthstate.OAuthProbeObservation {
	observation := healthstate.OAuthProbeObservation{
		ProbeID:           o.probeID,
		Origin:            "startup",
		ProbeType:         string(o.probeType),
		Method:            o.method,
		StartedAt:         o.startedAt,
		StatusCode:        o.statusCode,
		Disposition:       string(o.disposition),
		ResponseRequestID: o.responseRequestID,
	}
	if o.responseObserved {
		responseAt := o.responseAt
		observation.ResponseAt = &responseAt
	}
	if o.disposition != probeDispositionPending {
		completedAt := o.completedAt
		observation.CompletedAt = &completedAt
	}
	return observation
}

func boundedResponseRequestID(headers http.Header) (string, bool) {
	// Custom transports can return arbitrarily large header maps. Bound the
	// diagnostic inspection independently from the HTTP transport's own limits.
	if len(headers) > maxResponseHeaderNames {
		return "", true
	}
	var candidate string
	count := 0
	present := false
	for name, values := range headers {
		if !strings.EqualFold(name, "X-Request-Id") {
			continue
		}
		present = true
		for _, value := range values {
			count++
			if count > 1 {
				return "", true
			}
			candidate = value
		}
	}
	if count == 0 {
		return "", present
	}
	if len(candidate) == 0 || len(candidate) > maxResponseRequestIDSize {
		return "", true
	}
	for i := range len(candidate) {
		c := candidate[i]
		if (c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z') ||
			(c >= '0' && c <= '9') || c == '.' || c == '_' || c == ':' || c == '-' {
			continue
		}
		return "", true
	}
	connectionValues := 0
	connectionBytes := 0
	for name, values := range headers {
		if !strings.EqualFold(name, "Connection") {
			continue
		}
		if len(values) > maxConnectionValues-connectionValues {
			return "", true
		}
		connectionValues += len(values)
		for _, value := range values {
			if len(value) > maxConnectionBytes-connectionBytes {
				return "", true
			}
			connectionBytes += len(value)
		}
	}
	for name, values := range headers {
		if !strings.EqualFold(name, "Connection") {
			continue
		}
		for _, value := range values {
			for token := range strings.SplitSeq(value, ",") {
				if strings.EqualFold(strings.Trim(token, " \t"), "X-Request-Id") {
					return "", true
				}
			}
		}
	}
	return strings.Clone(candidate), false
}
