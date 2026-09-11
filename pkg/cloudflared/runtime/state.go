package runtime

import (
	"strings"
	"sync"
	"time"

	"github.com/openai/tunnel-client/pkg/healthstate"
	"github.com/openai/tunnel-client/pkg/runtimeconfig"
)

// State tracks whether the optional cloudflared companion is currently ready.
// It intentionally stores no token material.
type State struct {
	mu         sync.RWMutex
	enabled    bool
	ready      bool
	reason     string
	observedAt time.Time
}

// NewState creates readiness state from the effective cloudflared runtimeconfig.
func NewState(cfg *runtimeconfig.CloudflaredSettings) *State {
	enabled := cfg != nil && cfg.Enabled()
	state := &State{enabled: enabled}
	if enabled {
		state.reason = "cloudflared startup pending"
	}
	return state
}

// Enabled reports whether this runtime requested cloudflared supervision.
func (s *State) Enabled() bool {
	if s == nil {
		return false
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.enabled
}

// Readiness returns whether cloudflared permits tunnel-client readiness and a
// token-safe reason when it does not.
func (s *State) Readiness() (bool, string) {
	if s == nil {
		return true, ""
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	if !s.enabled {
		return true, ""
	}
	if s.ready {
		return true, ""
	}
	if strings.TrimSpace(s.reason) == "" {
		return false, "cloudflared is not ready"
	}
	return false, s.reason
}

func (s *State) setReady() {
	if s == nil {
		return
	}
	s.mu.Lock()
	if !s.ready || s.observedAt.IsZero() {
		s.observedAt = time.Now().UTC()
	}
	s.ready = true
	s.reason = ""
	s.mu.Unlock()
}

func (s *State) setNotReady(reason string) {
	if s == nil {
		return
	}
	s.mu.Lock()
	if s.ready || s.observedAt.IsZero() {
		s.observedAt = time.Now().UTC()
	}
	s.ready = false
	s.reason = strings.TrimSpace(reason)
	s.mu.Unlock()
}

func (*State) Name() string { return "cloudflared" }

func (s *State) Snapshot(time.Time) healthstate.ComponentSnapshot {
	s.mu.RLock()
	defer s.mu.RUnlock()
	result := healthstate.ComponentSnapshot{Status: healthstate.StatusDisabled, State: "disabled", Details: healthstate.CloudflaredDetails{Enabled: s.enabled, Ready: s.ready}}
	if !s.enabled {
		return result
	}
	result.Status, result.State = healthstate.StatusUnknown, "pending"
	if !s.observedAt.IsZero() {
		observed := s.observedAt
		result.ObservedAt = &observed
		result.Status, result.State, result.ReasonCode = healthstate.StatusDegraded, "not_ready", "companion_not_ready"
	}
	if s.ready {
		result.Status, result.State, result.ReasonCode = healthstate.StatusOK, "ready", ""
	}
	return result
}

func componentHealth(state *State) healthstate.Component { return state }
