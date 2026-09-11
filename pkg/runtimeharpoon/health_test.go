package runtimeharpoon

import (
	"encoding/json"
	"errors"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/openai/tunnel-client/pkg/healthstate"
	"github.com/openai/tunnel-client/pkg/runtimeharpoon/hostbus"
)

func TestHealthUsesNonblockingCatalogReceiptAndCount(t *testing.T) {
	t.Parallel()
	for _, failed := range []bool{false, true} {
		state := hostbus.NewStartupCatalogState()
		h := NewHealth()
		AttachHealth(healthParams{Health: h, Registry: &Registry{targets: map[string]Target{"secret": {}}}, Catalog: state})
		require.Equal(t, healthstate.StatusUnknown, h.Snapshot(time.Now()).Status)
		var failure error
		if failed {
			failure = errors.New("secret credential")
		}
		state.Complete(failure)
		snapshot := h.Snapshot(time.Now())
		expected := healthstate.StatusOK
		if failed {
			expected = healthstate.StatusDegraded
		}
		require.Equal(t, expected, snapshot.Status)
		require.Equal(t, 1, snapshot.Details.(healthstate.HarpoonDetails).TargetCount)
		encoded, err := json.Marshal(snapshot)
		require.NoError(t, err)
		require.NotContains(t, string(encoded), "secret")
		state.Complete(errors.New("ignored"))
		require.Equal(t, expected, h.Snapshot(time.Now()).Status)
	}
}
