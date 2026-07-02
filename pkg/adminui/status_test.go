package adminui

import (
	"context"
	"io"
	"log/slog"
	"net/url"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/openai/tunnel-client/pkg/clientinstance"
	"github.com/openai/tunnel-client/pkg/config"
	"github.com/openai/tunnel-client/pkg/harpoon"
	"github.com/openai/tunnel-client/pkg/mcpclient"
	"github.com/openai/tunnel-client/pkg/types"
)

type stubStdioRuntimeInfoProvider struct {
	info    mcpclient.StdioRuntimeInfo
	channel types.Channel
}

func (s stubStdioRuntimeInfoProvider) StdioRuntimeInfo(channel types.Channel) (mcpclient.StdioRuntimeInfo, bool) {
	if s.channel.Canonical() != channel.Canonical() {
		return mcpclient.StdioRuntimeInfo{}, false
	}
	return s.info, true
}

func TestBuildStatusIncludesChannels(t *testing.T) {
	buffer := NewLogBufferWithCapacity(1)
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))

	registry, err := harpoon.NewRegistry(logger, false, nil)
	require.NoError(t, err)

	serverURL, err := url.Parse("https://example.com/mcp")
	require.NoError(t, err)

	out := buildStatus(routeParams{
		Buffer: buffer,
		MCPConfig: &config.MCPConfig{
			ServerURL: serverURL,
			ChannelBindings: []config.MCPChannelBinding{
				{
					Channel:       types.DefaultChannel,
					TransportKind: config.MCPTransportHTTPStreamable,
					ServerURL:     serverURL,
				},
			},
		},
		HarpoonReg:    registry,
		MCPProbeState: mcpclient.NewProbeState(),
	})

	require.Equal(t, clientinstance.ID(), out.ClientInstanceID)
	require.Len(t, out.Channels, 2)
	require.Equal(t, types.DefaultChannel.String(), out.Channels[0].Name)
	require.True(t, out.Channels[0].Enabled)
	require.Equal(t, "pending", out.Channels[0].ProbeStatus)
	require.Equal(t, MCPServerKindExternal, out.Channels[0].ServerKind)
	require.Equal(t, config.MCPTransportHTTPStreamable, out.Channels[0].TransportKind)
	require.Equal(t, []ChannelStatusDetail{
		{
			Key:   "address",
			Value: "https://example.com/mcp",
		},
	}, out.Channels[0].Details)

	require.Equal(t, types.ChannelHarpoon.String(), out.Channels[1].Name)
	require.False(t, out.Channels[1].Enabled)
	require.Equal(t, MCPServerKindBuiltin, out.Channels[1].ServerKind)
	require.Equal(t, config.MCPTransportInMemory, out.Channels[1].TransportKind)
	require.Equal(t, "no harpoon targets registered", out.Channels[1].Reason)
	require.Empty(t, out.Channels[1].Details)
}

func TestBuildStatusIncludesStdioChannelDetails(t *testing.T) {
	buffer := NewLogBufferWithCapacity(1)
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))

	registry, err := harpoon.NewRegistry(logger, false, nil)
	require.NoError(t, err)

	out := buildStatus(routeParams{
		Buffer: buffer,
		MCPConfig: &config.MCPConfig{
			TransportKind: config.MCPTransportStdio,
			Command:       "python /tmp/mcp.py --stdio",
			CommandArgs:   []string{"python", "/tmp/mcp.py", "--stdio"},
			ChannelBindings: []config.MCPChannelBinding{
				{
					Channel:       types.DefaultChannel,
					TransportKind: config.MCPTransportStdio,
					Command:       "python /tmp/mcp.py --stdio",
					CommandArgs:   []string{"python", "/tmp/mcp.py", "--stdio"},
				},
			},
		},
		HarpoonReg: registry,
		StdioInfo: stubStdioRuntimeInfoProvider{
			info: mcpclient.StdioRuntimeInfo{
				PID:     4242,
				Command: "python /tmp/mcp.py --stdio",
			},
			channel: types.DefaultChannel,
		},
	})

	require.Len(t, out.Channels, 2)
	require.Equal(t, types.DefaultChannel.String(), out.Channels[0].Name)
	require.True(t, out.Channels[0].Enabled)
	require.Equal(t, config.MCPTransportStdio, out.Channels[0].TransportKind)
	require.Equal(t, []ChannelStatusDetail{
		{
			Key:   "pid",
			Value: "4242",
		},
		{
			Key:   "command",
			Value: "python /tmp/mcp.py --stdio",
		},
	}, out.Channels[0].Details)
}

func TestBuildStatusIncludesMainChannelProbeFailure(t *testing.T) {
	buffer := NewLogBufferWithCapacity(1)
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))

	registry, err := harpoon.NewRegistry(logger, false, nil)
	require.NoError(t, err)

	serverURL, err := url.Parse("https://example.com/mcp")
	require.NoError(t, err)

	probeState := mcpclient.NewProbeState()
	probeState.Set(assertiveError("dial tcp 127.0.0.1:1: connection refused"))

	out := buildStatus(routeParams{
		Buffer: buffer,
		MCPConfig: &config.MCPConfig{
			ServerURL: serverURL,
			ChannelBindings: []config.MCPChannelBinding{
				{
					Channel:       types.DefaultChannel,
					TransportKind: config.MCPTransportHTTPStreamable,
					ServerURL:     serverURL,
				},
			},
		},
		HarpoonReg:    registry,
		MCPProbeState: probeState,
	})

	require.Len(t, out.Channels, 2)
	require.False(t, out.Channels[0].Enabled)
	require.Equal(t, "failed", out.Channels[0].ProbeStatus)
	require.Contains(t, out.Channels[0].ProbeError, "connection refused")
	require.Equal(t, "initial mcp probe failed", out.Channels[0].Reason)
}

func TestBuildStatusIncludesMainChannelProbeTimeoutWithoutDisablingChannel(t *testing.T) {
	buffer := NewLogBufferWithCapacity(1)
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))

	registry, err := harpoon.NewRegistry(logger, false, nil)
	require.NoError(t, err)

	serverURL, err := url.Parse("https://example.com/mcp")
	require.NoError(t, err)

	probeState := mcpclient.NewProbeState()
	probeState.Set(mcpclient.NewProbeTimeoutError(2*time.Second, context.DeadlineExceeded))

	out := buildStatus(routeParams{
		Buffer: buffer,
		MCPConfig: &config.MCPConfig{
			ServerURL: serverURL,
			ChannelBindings: []config.MCPChannelBinding{
				{
					Channel:       types.DefaultChannel,
					TransportKind: config.MCPTransportHTTPStreamable,
					ServerURL:     serverURL,
				},
			},
		},
		HarpoonReg:    registry,
		MCPProbeState: probeState,
	})

	require.Len(t, out.Channels, 2)
	require.True(t, out.Channels[0].Enabled)
	require.Equal(t, "timeout", out.Channels[0].ProbeStatus)
	require.Contains(t, out.Channels[0].ProbeError, "mcp probe timed out")
	require.Equal(t, "initial mcp probe timed out", out.Channels[0].Reason)
}

type assertiveError string

func (e assertiveError) Error() string { return string(e) }
