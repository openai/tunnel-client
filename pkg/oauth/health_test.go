package oauth

import (
	"encoding/json"
	"errors"
	"net/http"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/openai/tunnel-client/pkg/healthstate"
	"github.com/openai/tunnel-client/pkg/runtimeconfig"
)

func TestComponentHealthClassifiesWithoutDiscoveryPayloads(t *testing.T) {
	t.Parallel()
	u, err := url.Parse("https://private.example/mcp?token=secret")
	require.NoError(t, err)
	for _, tc := range []struct {
		name   string
		err    error
		result *DiscoveryResult
		status healthstate.Status
		state  string
	}{
		{name: "complete", status: healthstate.StatusOK, state: "complete"},
		{name: "optional", err: errors.New("secret"), result: &DiscoveryResult{Attempts: []DiscoveryAttempt{{Tried: true, StatusCode: http.StatusNotFound}}}, status: healthstate.StatusOK, state: "not_advertised"},
		{name: "failed", err: errors.New("secret"), status: healthstate.StatusDegraded, state: "failed"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			state := NewDiscoveryState()
			component := newComponentHealth(state, &runtimeconfig.MCPConfig{ServerURL: u})
			require.Equal(t, healthstate.StatusUnknown, component.Snapshot(time.Now()).Status)
			state.Set(tc.result, tc.err, nil, []string{u.String()})
			snapshot := component.Snapshot(time.Now())
			require.Equal(t, tc.status, snapshot.Status)
			require.Equal(t, tc.state, snapshot.State)
			encoded, err := json.Marshal(snapshot)
			require.NoError(t, err)
			require.NotContains(t, string(encoded), "secret")
			require.NotContains(t, string(encoded), "private.example")
		})
	}
	disabled := newComponentHealth(NewDiscoveryState(), &runtimeconfig.MCPConfig{TransportKind: runtimeconfig.MCPTransportStdio, ServerURL: u})
	require.Equal(t, healthstate.StatusDisabled, disabled.Snapshot(time.Now()).Status)
}

func TestComponentHealthIncludesBoundedProbeEvidence(t *testing.T) {
	t.Parallel()
	state := newProbeTestState()
	headers := http.Header{
		"X-Request-Id":     {strings.Repeat("a", maxResponseRequestIDSize)},
		"Authorization":    {"secret-authorization"},
		"Set-Cookie":       {"secret-cookie"},
		"Www-Authenticate": {`Bearer resource_metadata="https://private.example/secret"`},
	}
	for range maxRecentProbes {
		receipt := state.beginProbe(probeTypeAuthorizationServerMetadata, http.MethodGet)
		receipt.observeResponse(999, headers)
		receipt.finish(probeDispositionRetry)
	}
	for range maxActiveProbes {
		receipt := state.beginProbe(probeTypeWWWAuthenticate, http.MethodPost)
		receipt.observeResponse(http.StatusUnsupportedMediaType, headers)
	}
	u, err := url.Parse("https://private.example/mcp?token=secret")
	require.NoError(t, err)
	component := newComponentHealth(state, &runtimeconfig.MCPConfig{ServerURL: u})
	snapshot := component.Snapshot(time.Now())
	require.Equal(t, healthstate.StatusUnknown, snapshot.Status)
	require.Equal(t, "pending", snapshot.State)
	require.False(t, snapshot.Limited)
	details, ok := snapshot.Details.(healthstate.OAuthDetails)
	require.True(t, ok)
	require.Len(t, details.ActiveProbes, maxActiveProbes)
	require.Len(t, details.RecentProbes, maxRecentProbes)
	require.Equal(t, 999, details.RecentProbes[0].StatusCode)
	encoded, err := json.Marshal(snapshot)
	require.NoError(t, err)
	require.Less(t, len(encoded), healthstate.MaxComponentBytes)
	require.NotContains(t, string(encoded), "secret")
	require.NotContains(t, string(encoded), "private.example")
	require.NotContains(t, string(encoded), "Authorization")
	require.NotContains(t, string(encoded), "Set-Cookie")

	state.Set(nil, nil, nil, nil)
	snapshot = component.Snapshot(time.Now())
	require.Equal(t, healthstate.StatusOK, snapshot.Status)
	require.Equal(t, "complete", snapshot.State)
	details, ok = snapshot.Details.(healthstate.OAuthDetails)
	require.True(t, ok)
	require.True(t, details.DiscoveryComplete)
	require.Len(t, details.ActiveProbes, maxActiveProbes)
	require.Len(t, details.RecentProbes, maxRecentProbes)
}
