package runtime

import (
	"strings"
	"time"

	"github.com/openai/tunnel-client/pkg/runtimeconfig"
)

// StartupTimeout returns the Fx startup budget needed by the optional
// cloudflared companion. Managed mode without a configured token first fetches
// one from the control plane, so it receives one full poll deadline in addition
// to the cloudflared readiness budget.
func StartupTimeout(
	base time.Duration,
	cloudflared runtimeconfig.CloudflaredSettings,
	controlPlane runtimeconfig.ControlPlaneConfig,
) time.Duration {
	if !cloudflared.Enabled() || cloudflared.ReadyTimeout <= 0 {
		return base
	}

	timeout := addStartupTimeout(base, cloudflared.ReadyTimeout)
	if cloudflared.Managed && strings.TrimSpace(cloudflared.Token) == "" {
		timeout = addStartupTimeout(timeout, controlPlane.PollDeadlineTimeoutOrDefault())
	}
	return timeout
}

func addStartupTimeout(base, extra time.Duration) time.Duration {
	const maxDuration = time.Duration(1<<63 - 1)
	if extra <= 0 {
		return base
	}
	if base >= maxDuration-extra {
		return maxDuration
	}
	return base + extra
}
