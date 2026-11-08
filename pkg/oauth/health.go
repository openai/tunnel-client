package oauth

import (
	"time"

	"github.com/openai/tunnel-client/pkg/healthstate"
	"github.com/openai/tunnel-client/pkg/runtimeconfig"
)

type componentHealth struct {
	state   *DiscoveryState
	enabled bool
}

func newComponentHealth(state *DiscoveryState, cfg *runtimeconfig.MCPConfig) healthstate.Component {
	enabled := false
	if cfg != nil && !cfg.AllowNoMain {
		kind, serverURL := cfg.TransportKind, cfg.ServerURL
		if main := cfg.MainChannelBinding(); main != nil {
			kind, serverURL = main.TransportKind, main.ServerURL
		}
		enabled = (kind == "" || kind == runtimeconfig.MCPTransportHTTPStreamable) && serverURL != nil
	}
	return &componentHealth{state: state, enabled: enabled}
}

func (*componentHealth) Name() string { return "oauth" }

func (h *componentHealth) Snapshot(time.Time) healthstate.ComponentSnapshot {
	result := healthstate.ComponentSnapshot{Status: healthstate.StatusDisabled, State: "disabled", Details: healthstate.OAuthDetails{}}
	if !h.enabled {
		return result
	}
	result.Status, result.State = healthstate.StatusUnknown, "pending"
	if h.state == nil {
		return result
	}
	snapshot := h.state.healthSnapshot()
	details := healthstate.OAuthDetails{
		ActiveProbes: snapshot.activeProbes,
		RecentProbes: snapshot.recentProbes,
	}
	result.Details = details
	result.Limited = snapshot.limited
	if snapshot.completedAt.IsZero() {
		return result
	}
	checked := snapshot.completedAt
	result.ObservedAt = &checked
	details.DiscoveryComplete = true
	result.Details = details
	result.Status, result.State = healthstate.StatusOK, "complete"
	if snapshot.optionalFailure {
		result.State, result.ReasonCode = "not_advertised", "metadata_not_advertised"
	} else if snapshot.failed {
		result.Status, result.State, result.ReasonCode = healthstate.StatusDegraded, "failed", "discovery_failed"
	}
	return result
}
