package adminui

import (
	"io"
	"log/slog"
	"net/url"
	"testing"

	"github.com/stretchr/testify/require"

	"go.openai.org/api/tunnel-client/pkg/config"
	"go.openai.org/api/tunnel-client/pkg/harpoon"
	"go.openai.org/api/tunnel-client/pkg/mcpclient"
	"go.openai.org/api/tunnel-client/pkg/types"
)

type stubStdioRuntimeInfoProvider struct {
	info mcpclient.StdioRuntimeInfo
}

func (s stubStdioRuntimeInfoProvider) StdioRuntimeInfo() mcpclient.StdioRuntimeInfo {
	return s.info
}

func TestBuildStatusIncludesChannels(t *testing.T) {
	buffer := NewLogBufferWithCapacity(1)
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))

	registry, err := harpoon.NewRegistry(logger, false, nil)
	require.NoError(t, err)

	serverURL, err := url.Parse("https://example.com/mcp")
	require.NoError(t, err)

	out := buildStatus(routeParams{
		Buffer:     buffer,
		MCPConfig:  &config.MCPConfig{ServerURL: serverURL},
		HarpoonReg: registry,
	})

	require.Len(t, out.Channels, 2)
	require.Equal(t, types.DefaultChannel.String(), out.Channels[0].Name)
	require.True(t, out.Channels[0].Enabled)
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
		},
		HarpoonReg: registry,
		StdioInfo: stubStdioRuntimeInfoProvider{
			info: mcpclient.StdioRuntimeInfo{
				PID:     4242,
				Command: "python /tmp/mcp.py --stdio",
			},
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
