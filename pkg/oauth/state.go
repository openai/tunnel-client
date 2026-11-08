package oauth

import (
	"encoding/json"
	"net/http"
	"net/url"
	"sync"
	"time"

	"github.com/openai/tunnel-client/pkg/types"
)

// DefaultDiscoveryTimeout bounds how long we wait for OAuth ProtectedResourceMetaData discovery.
const DefaultDiscoveryTimeout = 5 * time.Second

// DiscoveryResult captures OAuth ProtectedResourceMetaData returned by the MCP server.
type DiscoveryResult struct {
	URL                string                         `json:"url,omitempty"`
	FetchedAt          time.Time                      `json:"fetched_at"`
	StatusCode         int                            `json:"status_code,omitempty"`
	Headers            http.Header                    `json:"headers,omitempty"`
	Body               json.RawMessage                `json:"body,omitempty"`
	BodyText           string                         `json:"body_text,omitempty"`
	Attempts           []DiscoveryAttempt             `json:"attempts,omitempty"`
	AuthServerMetadata *AuthServerMetadataFetchResult `json:"auth_server_metadata,omitempty"`
}

// DiscoveryState tracks the result of a background OAuth ProtectedResourceMetaData fetch.
type DiscoveryState struct {
	done            chan struct{}
	mu              sync.Mutex
	result          *DiscoveryResult
	err             error
	probe           *WWWAuthenticateProbeStatus
	urls            []string
	once            sync.Once
	completedAt     time.Time
	optionalFailure bool
	observations    probeObservationState
	now             func() time.Time
}

// NewDiscoveryState constructs a DiscoveryState ready for updates.
func NewDiscoveryState() *DiscoveryState {
	return newDiscoveryStateWithClock(time.Now)
}

func newDiscoveryStateWithClock(now func() time.Time) *DiscoveryState {
	return &DiscoveryState{done: make(chan struct{}), now: now}
}

// IsDone reports whether the discovery process has completed.
func (s *DiscoveryState) IsDone() bool {
	if s == nil {
		return true
	}
	select {
	case <-s.done:
		return true
	default:
		return false
	}
}

// Set records the OAuth discovery result and signals waiters.
func (s *DiscoveryState) Set(
	result *DiscoveryResult,
	err error,
	probe *WWWAuthenticateProbeStatus,
	urls []string,
) {
	s.setWithProbe(result, err, probe, urls, nil)
}

// setWithProbe settles an attributable final probe and the existing discovery
// outcome atomically. A nil receipt preserves final outcomes that do not belong
// to one HTTP exchange, such as configuration or bundle failures.
func (s *DiscoveryState) setWithProbe(
	result *DiscoveryResult,
	err error,
	probe *WWWAuthenticateProbeStatus,
	urls []string,
	final *probeReceipt,
) {
	if s == nil {
		return
	}
	s.once.Do(func() {
		s.mu.Lock()
		defer s.mu.Unlock()
		completedAt := s.observationTime()
		optionalFailure := IsOptionalDiscoveryFailure(result, probe, err)
		if final != nil {
			if final.state != s {
				s.observations.limited = true
			} else {
				disposition := probeDispositionAccepted
				if optionalFailure {
					disposition = probeDispositionNotAdvertised
				} else if err != nil {
					disposition = probeDispositionTerminalFailure
				}
				s.finishProbeLocked(final.sequence, disposition, completedAt)
			}
		}
		s.observations.closed = true
		s.result = result
		s.err = err
		s.completedAt = completedAt
		s.optionalFailure = optionalFailure
		if probe != nil {
			probeCopy := *probe
			s.probe = &probeCopy
		}
		if len(urls) > 0 {
			s.urls = append([]string{}, urls...)
		}
		close(s.done)
	})
}

// Wait blocks until the OAuth ProtectedResourceMetaData is available or the timeout elapses.
func (s *DiscoveryState) Wait(
	timeout time.Duration,
) (*DiscoveryResult, *WWWAuthenticateProbeStatus, []string, error, bool) {
	if s == nil {
		return nil, nil, nil, nil, false
	}

	snapshot := func() (*DiscoveryResult, *WWWAuthenticateProbeStatus, []string, error, bool) {
		s.mu.Lock()
		defer s.mu.Unlock()
		urlsCopy := append([]string{}, s.urls...)
		var probeCopy *WWWAuthenticateProbeStatus
		if s.probe != nil {
			copyVal := *s.probe
			probeCopy = &copyVal
		}
		return s.result, probeCopy, urlsCopy, s.err, true
	}

	select {
	case <-s.done:
		return snapshot()
	default:
	}

	timer := time.NewTimer(timeout)
	defer timer.Stop()
	select {
	case <-s.done:
		return snapshot()
	case <-timer.C:
		select {
		case <-s.done:
			return snapshot()
		default:
			return nil, nil, nil, nil, false
		}
	}
}

// BuildDiscoveryResult converts the tunnel response into a UI-friendly payload.
func BuildDiscoveryResult(
	resp *types.TunnelResponse,
	sourceURL *url.URL,
	fetchedAt time.Time,
	attempts []DiscoveryAttempt,
) *DiscoveryResult {
	if resp == nil && len(attempts) == 0 {
		return nil
	}

	result := &DiscoveryResult{
		Attempts: attempts,
	}
	if resp == nil {
		return result
	}

	result.FetchedAt = fetchedAt
	result.StatusCode = resp.ResponseCode()
	result.Headers = resp.Headers()
	if sourceURL != nil {
		result.URL = sourceURL.String()
	}

	payload := resp.Payload()
	if json.Valid(payload) {
		result.Body = payload
	} else if len(payload) > 0 {
		result.BodyText = string(payload)
	}

	return result
}
