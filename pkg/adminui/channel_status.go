package adminui

import (
	"fmt"
	"strconv"
	"strings"

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
	stdioInfoProvider mcpclient.StdioRuntimeInfoProvider,
) []ChannelStatus {
	return []ChannelStatus{
		buildMainChannelStatus(mcpCfg, stdioInfoProvider),
		buildHarpoonChannelStatus(harpoonRegistry),
	}
}

func buildMainChannelStatus(mcpCfg *config.MCPConfig, stdioInfoProvider mcpclient.StdioRuntimeInfoProvider) ChannelStatus {
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
		if stdioInfoProvider != nil {
			info := stdioInfoProvider.StdioRuntimeInfo()
			if strings.TrimSpace(info.Command) != "" {
				command = info.Command
			}
			if info.PID > 0 {
				pid = strconv.Itoa(info.PID)
			}
		}
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

	return status
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
