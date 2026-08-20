package cloudflared

import (
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/openai/tunnel-client/pkg/config"
	"github.com/openai/tunnel-client/pkg/runtimeconfig"
)

func TestCloudflaredConfigSharesRuntimeSettingsType(t *testing.T) {
	t.Parallel()

	cfg := &config.CloudflaredConfig{
		Token:        "token",
		Managed:      true,
		Path:         "/opt/cloudflared",
		ReadyTimeout: 17 * time.Second,
	}

	requireRuntimeSettingsType(cfg)
	got := cfg
	require.Same(t, cfg, got)
	require.Equal(t, cfg.Token, got.Token)
	require.Equal(t, cfg.Managed, got.Managed)
	require.Equal(t, cfg.Path, got.Path)
	require.Equal(t, cfg.ReadyTimeout, got.ReadyTimeout)
}

func requireRuntimeSettingsType(*runtimeconfig.CloudflaredSettings) {}

func TestCompatibilityAdapterDelegatesManifestAndState(t *testing.T) {
	t.Parallel()

	require.Equal(t, "2026.7.2", BundledVersion())
	require.Equal(t, BundledVersion(), BundledManifest().Version)

	state := NewState(&config.CloudflaredConfig{Token: "secret-token"})
	ready, reason := state.Readiness()
	require.False(t, ready)
	require.Equal(t, "cloudflared startup pending", reason)
	require.NotContains(t, reason, "secret-token")
}
