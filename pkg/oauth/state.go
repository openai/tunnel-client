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
	FetchedAt          time.Time                      `json:"fetched_at,omitempty"`
	StatusCode         int                            `json:"status_code,omitempty"`
	Headers            http.Header                    `json:"headers,omitempty"`
	Body               json.RawMessage                `json:"body,omitempty"`
	BodyText           string                         `json:"body_text,omitempty"`
	Attempts           []DiscoveryAttempt             `json:"attempts,omitempty"`
	AuthServerMetadata *AuthServerMetadataFetchResult `json:"auth_server_metadata,omitempty"`
}

// DiscoveryState tracks the result of a background OAuth ProtectedResourceMetaData fetch.
type DiscoveryState struct {
	done   chan struct{}
	mu     sync.Mutex
	result *DiscoveryResult
	err    error
	probe  *WWWAuthenticateProbeStatus
	urls   []string
	once   sync.Once
}

// NewDiscoveryState constructs a DiscoveryState ready for updates.
func NewDiscoveryState() *DiscoveryState {
	return &DiscoveryState{done: make(chan struct{})}
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
	if s == nil {
		return
	}
	s.once.Do(func() {
		s.mu.Lock()
		defer s.mu.Unlock()
		s.result = result
		s.err = err
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
