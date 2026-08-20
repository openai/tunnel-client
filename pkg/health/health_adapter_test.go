package health

import (
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/openai/tunnel-client/pkg/cloudflared"
	"github.com/openai/tunnel-client/pkg/config"
)

func TestFullReadinessGateDelegatesCloudflaredState(t *testing.T) {
	t.Parallel()

	state := cloudflared.NewState(&config.CloudflaredConfig{Token: "secret-token"})
	gate := fullReadinessGate(readinessGateParams{CloudflaredState: state})
	require.NotNil(t, gate)

	ready, reason := gate.Readiness()
	require.False(t, ready)
	require.Equal(t, "cloudflared startup pending", reason)
	require.NotContains(t, reason, "secret-token")
}
