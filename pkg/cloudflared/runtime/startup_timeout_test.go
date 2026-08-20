package runtime

import (
	"testing"
	"time"

	"github.com/openai/tunnel-client/pkg/runtimeconfig"
)

func TestStartupTimeoutSaturates(t *testing.T) {
	t.Parallel()

	const maxDuration = time.Duration(1<<63 - 1)
	got := StartupTimeout(
		maxDuration-time.Second,
		runtimeconfig.CloudflaredSettings{
			Managed:      true,
			ReadyTimeout: 2 * time.Second,
		},
		runtimeconfig.ControlPlaneConfig{},
	)
	if got != maxDuration {
		t.Fatalf("expected saturated duration %s, got %s", maxDuration, got)
	}
}
