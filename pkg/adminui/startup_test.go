package adminui

import (
	"errors"
	"net/url"
	"testing"

	"github.com/stretchr/testify/require"

	"go.openai.org/api/tunnel-client/pkg/codexplugin"
	"go.openai.org/api/tunnel-client/pkg/config"
	"go.openai.org/api/tunnel-client/pkg/mcpclient"
	"go.openai.org/api/tunnel-client/pkg/oauth"
	"go.openai.org/api/tunnel-client/pkg/types"
)

func TestBuildStartupSummaryIncludesRuntimeAndPluginGuidance(t *testing.T) {
	t.Parallel()

	serverURL, err := url.Parse("http://127.0.0.1:3001/mcp")
	require.NoError(t, err)

	cfg := &config.Config{
		ControlPlane: config.ControlPlaneConfig{
			TunnelID: types.TunnelID("tunnel_0123456789abcdef0123456789abcdef"),
		},
		MCP: config.MCPConfig{
			TransportKind: config.MCPTransportHTTPStreamable,
			ChannelBindings: []config.MCPChannelBinding{
				{
					Channel:       types.DefaultChannel,
					TransportKind: config.MCPTransportHTTPStreamable,
					ServerURL:     serverURL,
				},
			},
		},
		Runtime: config.RuntimeConfig{
			ProfileName: "demo",
			ProfilePath: "/tmp/demo.yaml",
		},
		Health: config.HealthConfig{
			URLFile: "/tmp/health-url",
		},
	}

	summary := buildStartupSummary(cfg, "http://127.0.0.1:7777", nil, nil, codexplugin.Detection{
		Detected:        true,
		CodexHome:       "/tmp/.codex",
		PluginInstalled: false,
		PluginDir:       "/tmp/.codex/plugins/cache/debug/tunnel-mcp/local",
		InstallHint:     "tunnel-client codex plugin install",
	})

	require.Equal(t, "http://127.0.0.1:7777", summary.HealthURL)
	require.Equal(t, "/tmp/health-url", summary.HealthURLFile)
	require.Equal(t, "http://127.0.0.1:7777/ui", summary.UIURL)
	require.Equal(t, "profile:demo", summary.ConfigSource)
	require.Equal(t, "demo", summary.ProfileName)
	require.Equal(t, "/tmp/demo.yaml", summary.ProfilePath)
	require.Equal(t, "tunnel_0123456789abcdef0123456789abcdef", summary.TunnelID)
	require.Equal(t, string(config.MCPTransportHTTPStreamable), summary.MCPTargetKind)
	require.Equal(t, "http://127.0.0.1:3001/mcp", summary.MCPTargetValue)
	require.True(t, summary.CodexDetected)
	require.False(t, summary.CodexPluginInstalled)
	require.Equal(t, "tunnel-client codex plugin install", summary.CodexPluginInstallHint)
}

func TestBuildStartupSummaryMarksProfileFileSource(t *testing.T) {
	t.Parallel()

	summary := buildStartupSummary(&config.Config{
		Runtime: config.RuntimeConfig{
			ProfileName: "demo",
			ProfilePath: "/tmp/demo.yaml",
			ProfileFile: true,
		},
	}, "http://127.0.0.1:7777", nil, nil, codexplugin.Detection{})

	require.Equal(t, "profile-file:/tmp/demo.yaml", summary.ConfigSource)
}

func TestBuildStartupSummaryHandlesNilConfig(t *testing.T) {
	t.Parallel()

	summary := buildStartupSummary(nil, "http://127.0.0.1:7777", nil, nil, codexplugin.Detection{})

	require.Equal(t, "http://127.0.0.1:7777", summary.HealthURL)
	require.Equal(t, "", summary.HealthURLFile)
	require.Equal(t, "http://127.0.0.1:7777/ui", summary.UIURL)
	require.Equal(t, "http://127.0.0.1:7777/metrics", summary.MetricsURL)
}

func TestStartupFirstFailingDependencyPrefersProbeOverOAuth(t *testing.T) {
	t.Parallel()

	cfg := &config.Config{
		MCP: config.MCPConfig{
			TransportKind: config.MCPTransportHTTPStreamable,
		},
	}
	probe := mcpclient.NewProbeState()
	probe.Set(errors.New("initialize failed"))

	oauthState := oauth.NewDiscoveryState()
	oauthState.Set(nil, errors.New("oauth metadata missing"), nil, nil)

	require.Equal(t, "mcp_probe: initialize failed", startupFirstFailingDependency(cfg, probe, oauthState))
}

func TestStartupFirstFailingDependencyUsesOAuthWhenProbeSucceeded(t *testing.T) {
	t.Parallel()

	cfg := &config.Config{
		MCP: config.MCPConfig{
			TransportKind: config.MCPTransportHTTPStreamable,
		},
	}
	probe := mcpclient.NewProbeState()
	probe.Set(nil)

	oauthState := oauth.NewDiscoveryState()
	oauthState.Set(nil, errors.New("oauth metadata missing"), nil, nil)

	require.Equal(t, "oauth_metadata: oauth metadata missing", startupFirstFailingDependency(cfg, probe, oauthState))
}
