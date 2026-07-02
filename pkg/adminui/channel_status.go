package adminui

import (
	"fmt"
	"sort"
	"strconv"
	"strings"
	"time"

	"go.openai.org/api/tunnel-client/pkg/config"
	"go.openai.org/api/tunnel-client/pkg/harpoon"
	"go.openai.org/api/tunnel-client/pkg/mcpclient"
	"go.openai.org/api/tunnel-client/pkg/types"
)

// MCPServerKind describes the kind of MCP server backing a channel.
type MCPServerKind string

const (
	MCPServerKindExternal MCPServerKind = "external"
	MCPServerKindBuiltin  MCPServerKind = "builtin"
)

const mcpTransportUnknown config.MCPTransportKind = "unknown"

// ChannelStatus describes the runtime availability of a tunnel-client channel.
type ChannelStatus struct {
	Name          string                  `json:"name"`
	Enabled       bool                    `json:"enabled"`
	ServerKind    MCPServerKind           `json:"server_kind"`
	TransportKind config.MCPTransportKind `json:"transport_kind,omitempty"`
	ProbeStatus   string                  `json:"probe_status,omitempty"`
	ProbeError    string                  `json:"probe_error,omitempty"`
	Reason        string                  `json:"reason,omitempty"`
	Details       []ChannelStatusDetail   `json:"details,omitempty"`
}

// ChannelStatusDetail describes a transport-specific detail shown in the overview table.
type ChannelStatusDetail struct {
	Key   string `json:"key"`
	Value string `json:"value"`
}

// BuildChannelStatuses returns the ordered list of channel statuses for the current runtime state.
func BuildChannelStatuses(
	mcpCfg *config.MCPConfig,
	harpoonRegistry *harpoon.Registry,
	stdioInfoProvider mcpclient.ChannelStdioRuntimeInfoProvider,
	probeState *mcpclient.ProbeState,
) []ChannelStatus {
	statuses := make([]ChannelStatus, 0, 4)
	if mcpCfg == nil || len(mcpCfg.ChannelBindings) == 0 {
		statuses = append(statuses, buildFallbackMainChannelStatus(mcpCfg, stdioInfoProvider, probeState))
	} else {
		entries := make([]ChannelStatus, 0, len(mcpCfg.ChannelBindings))
		for _, binding := range mcpCfg.ChannelBindings {
			entries = append(entries, buildChannelStatus(binding, stdioInfoProvider, probeState))
		}
		sort.SliceStable(entries, func(i, j int) bool {
			return entries[i].Name < entries[j].Name
		})
		statuses = append(statuses, entries...)
	}
	statuses = append(statuses, buildHarpoonChannelStatus(harpoonRegistry))
	return statuses
}

func buildFallbackMainChannelStatus(mcpCfg *config.MCPConfig, stdioInfoProvider mcpclient.ChannelStdioRuntimeInfoProvider, probeState *mcpclient.ProbeState) ChannelStatus {
	status := ChannelStatus{
		Name:       types.DefaultChannel.String(),
		ServerKind: MCPServerKindExternal,
	}
	if mcpCfg == nil {
		status.TransportKind = config.MCPTransportHTTPStreamable
		status.Reason = "mcp config missing"
		return status
	}

	transportKind := mcpCfg.TransportKind
	if transportKind == "" {
		transportKind = config.MCPTransportHTTPStreamable
	}

	switch transportKind {
	case config.MCPTransportHTTPStreamable:
		status.TransportKind = config.MCPTransportHTTPStreamable
		if mcpCfg.ServerURL == nil {
			status.Reason = "mcp.server-url not configured"
			return status
		}
		status.Details = []ChannelStatusDetail{
			{
				Key:   "address",
				Value: mcpCfg.ServerURL.String(),
			},
		}
		status.Enabled = true
	case config.MCPTransportStdio:
		status.TransportKind = config.MCPTransportStdio
		if mcpCfg.Command == "" {
			status.Reason = "mcp.command not configured"
			return status
		}
		command := strings.TrimSpace(mcpCfg.Command)
		if command == "" {
			command = strings.Join(mcpCfg.CommandArgs, " ")
		}
		pid := ""
		pid, command = fillStdioRuntimeDetails(stdioInfoProvider, types.DefaultChannel, pid, command)
		if pid == "" {
			pid = "—"
		}
		status.Details = []ChannelStatusDetail{
			{
				Key:   "pid",
				Value: pid,
			},
			{
				Key:   "command",
				Value: command,
			},
		}
		status.Enabled = true
	case config.MCPTransportInMemory:
		status.TransportKind = config.MCPTransportInMemory
		status.ServerKind = MCPServerKindBuiltin
		status.Enabled = true
	default:
		status.TransportKind = mcpTransportUnknown
		status.Reason = fmt.Sprintf("unsupported mcp transport %q", transportKind)
	}

	applyMainChannelProbeStatus(&status, probeState)
	return status
}

func buildChannelStatus(binding config.MCPChannelBinding, stdioInfoProvider mcpclient.ChannelStdioRuntimeInfoProvider, probeState *mcpclient.ProbeState) ChannelStatus {
	status := ChannelStatus{
		Name:       binding.Channel.Canonical().String(),
		ServerKind: MCPServerKindExternal,
	}
	transportKind := binding.TransportKind
	if transportKind == "" {
		transportKind = config.MCPTransportHTTPStreamable
	}

	switch transportKind {
	case config.MCPTransportHTTPStreamable:
		status.TransportKind = config.MCPTransportHTTPStreamable
		if binding.ServerURL == nil {
			status.Reason = "mcp.server-url not configured"
			return status
		}
		status.Details = []ChannelStatusDetail{
			{
				Key:   "address",
				Value: binding.ServerURL.String(),
			},
		}
		status.Enabled = true
	case config.MCPTransportStdio:
		status.TransportKind = config.MCPTransportStdio
		if binding.Command == "" && len(binding.CommandArgs) == 0 {
			status.Reason = "mcp.command not configured"
			return status
		}
		command := strings.TrimSpace(binding.Command)
		if command == "" {
			command = strings.Join(binding.CommandArgs, " ")
		}
		pid := ""
		pid, command = fillStdioRuntimeDetails(stdioInfoProvider, binding.Channel, pid, command)
		if pid == "" {
			pid = "—"
		}
		status.Details = []ChannelStatusDetail{
			{
				Key:   "pid",
				Value: pid,
			},
			{
				Key:   "command",
				Value: command,
			},
		}
		status.Enabled = true
	case config.MCPTransportInMemory:
		status.TransportKind = config.MCPTransportInMemory
		status.ServerKind = MCPServerKindBuiltin
		status.Enabled = true
	default:
		status.TransportKind = mcpTransportUnknown
		status.Reason = fmt.Sprintf("unsupported mcp transport %q", transportKind)
	}
	if binding.Channel.Canonical() == types.DefaultChannel {
		applyMainChannelProbeStatus(&status, probeState)
	}
	return status
}

func applyMainChannelProbeStatus(status *ChannelStatus, probeState *mcpclient.ProbeState) {
	if status == nil || probeState == nil || !status.Enabled {
		return
	}
	if !probeState.IsDone() {
		status.ProbeStatus = "pending"
		return
	}
	if _, err, ok := probeState.Wait(10 * time.Millisecond); ok {
		if err == nil {
			status.ProbeStatus = "ok"
			return
		}
		status.ProbeError = err.Error()
		if mcpclient.IsAuthRequiredProbeError(err) {
			status.ProbeStatus = "auth-required"
			if status.Reason == "" {
				status.Reason = "mcp initialize requires auth"
			}
			return
		}
		if mcpclient.IsTimeoutProbeError(err) {
			status.ProbeStatus = "timeout"
			if status.Reason == "" {
				status.Reason = "initial mcp probe timed out"
			}
			return
		}
		status.ProbeStatus = "failed"
		status.Enabled = false
		if status.Reason == "" {
			status.Reason = "initial mcp probe failed"
		}
	}
}

func fillStdioRuntimeDetails(provider mcpclient.ChannelStdioRuntimeInfoProvider, channel types.Channel, pid string, command string) (string, string) {
	if provider == nil {
		return pid, command
	}
	info, ok := provider.StdioRuntimeInfo(channel)
	if !ok {
		return pid, command
	}
	if strings.TrimSpace(info.Command) != "" {
		command = info.Command
	}
	if info.PID > 0 {
		pid = strconv.Itoa(info.PID)
	}
	return pid, command
}

func buildHarpoonChannelStatus(registry *harpoon.Registry) ChannelStatus {
	status := ChannelStatus{
		Name:          types.ChannelHarpoon.String(),
		ServerKind:    MCPServerKindBuiltin,
		TransportKind: config.MCPTransportInMemory,
	}
	if registry == nil {
		status.Reason = "harpoon registry missing"
		return status
	}
	if registry.Count() == 0 {
		status.Reason = "no harpoon targets registered"
		return status
	}
	status.Enabled = true
	return status
}
