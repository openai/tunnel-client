// Package cloudflared composes the narrow runtime graph with the approved
// bundled cloudflared supervisor.
package cloudflared

import (
	"errors"
	"time"

	cloudflaredruntime "github.com/openai/tunnel-client/pkg/cloudflared/runtime"
	"github.com/openai/tunnel-client/pkg/runtimeapp"
	"github.com/openai/tunnel-client/pkg/runtimeconfig"
	"github.com/openai/tunnel-client/pkg/runtimehealth"
	"go.uber.org/fx"
)

// Options returns the Fx options for the cloudflared runtime flavor.
func Options(cfg *runtimeconfig.CloudflaredConfig, opts ...fx.Option) []fx.Option {
	if cfg == nil {
		return append([]fx.Option{fx.Error(errors.New("tunnel-client runtime-cloudflared config is nil"))}, opts...)
	}
	base := runtimeapp.Options(&cfg.Runtime,
		fx.Supply(&cfg.Cloudflared),
		cloudflaredruntime.Module,
		fx.Provide(
			fx.Annotate(
				func(state *cloudflaredruntime.State) runtimehealth.ReadinessGate { return state },
				fx.ResultTags(`group:"runtime_readiness_gates"`),
			),
		),
	)
	base = append(base, fx.StartTimeout(cloudflaredStartTimeout(cfg)))
	return append(base, opts...)
}

// New constructs a runtime-cloudflared tunnel-client Fx app.
func New(cfg *runtimeconfig.CloudflaredConfig, opts ...fx.Option) *fx.App {
	return fx.New(Options(cfg, opts...)...)
}

func cloudflaredStartTimeout(cfg *runtimeconfig.CloudflaredConfig) time.Duration {
	if cfg == nil {
		return fx.DefaultTimeout
	}
	return cloudflaredruntime.StartupTimeout(fx.DefaultTimeout, cfg.Cloudflared, cfg.Runtime.ControlPlane)
}
