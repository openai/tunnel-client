package oauth

import (
	"encoding/json"
	"errors"
	"net/http"
	"net/url"
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
