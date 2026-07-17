package cloudflared

import (
	"strings"
	"sync"

	"github.com/openai/tunnel-client/pkg/config"
)

// State tracks whether the optional cloudflared companion is currently ready.
// It intentionally stores no token material.
type State struct {
	mu      sync.RWMutex
	enabled bool
	ready   bool
	reason  string
}

// NewState creates readiness state from the effective cloudflared config.
func NewState(cfg *config.CloudflaredConfig) *State {
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
	s.ready = true
	s.reason = ""
	s.mu.Unlock()
}

func (s *State) setNotReady(reason string) {
	if s == nil {
		return
	}
	s.mu.Lock()
	s.ready = false
	s.reason = strings.TrimSpace(reason)
	s.mu.Unlock()
}
