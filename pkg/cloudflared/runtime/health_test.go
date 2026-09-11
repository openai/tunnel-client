package runtime

import (
	"encoding/json"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/openai/tunnel-client/pkg/healthstate"
)

func TestComponentHealthReportsTransitionsWithoutReasons(t *testing.T) {
	t.Parallel()
	require.Equal(t, healthstate.StatusDisabled, NewState(nil).Snapshot(time.Now()).Status)
	state := &State{enabled: true}
	require.Equal(t, healthstate.StatusUnknown, state.Snapshot(time.Now()).Status)
	state.setNotReady("secret token and private path")
	snapshot := state.Snapshot(time.Now())
	require.Equal(t, healthstate.StatusDegraded, snapshot.Status)
	encoded, err := json.Marshal(snapshot)
	require.NoError(t, err)
	require.NotContains(t, string(encoded), "secret")
	state.setReady()
	require.Equal(t, healthstate.StatusOK, state.Snapshot(time.Now()).Status)
	*state.Snapshot(time.Now()).ObservedAt = time.Time{}
	require.False(t, state.Snapshot(time.Now()).ObservedAt.IsZero())
}
