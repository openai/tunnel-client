package app

import (
	"errors"

	"go.uber.org/fx"

	"go.openai.org/api/tunnel-client/pkg/adminui"
	"go.openai.org/api/tunnel-client/pkg/config"
	controlplane "go.openai.org/api/tunnel-client/pkg/controlplane/fx"
	"go.openai.org/api/tunnel-client/pkg/dispatcher"
	"go.openai.org/api/tunnel-client/pkg/health"
	"go.openai.org/api/tunnel-client/pkg/log"
	"go.openai.org/api/tunnel-client/pkg/mcpclient"
	"go.openai.org/api/tunnel-client/pkg/metrics"
	"go.openai.org/api/tunnel-client/pkg/process"
)

// Options returns the Fx options required to wire the tunnel-client services.
// Additional Fx options can be provided to customize the runtime.
func Options(cfg *config.Config, opts ...fx.Option) []fx.Option {
	if cfg == nil {
		return append([]fx.Option{fx.Error(errors.New("tunnel-client config is nil"))}, opts...)
	}

	base := []fx.Option{
		fx.Supply(
			cfg,
			&cfg.ControlPlane,
			&cfg.Logging,
			&cfg.Health,
			&cfg.Process,
			&cfg.MCP,
			&cfg.AdminUI,
		),
		log.Module,
		dispatcher.Module,
		controlplane.Module,
		mcpclient.Module,
		metrics.MetricModule,
		process.Module,
		health.HealthMuxModule,
		fx.Invoke(func(health.Service) {}),
	}

	if cfg.AdminUI.Enabled {
		base = append(base, adminui.Module)
	}
	return append(base, opts...)
}

// New constructs a tunnel-client Fx app using the shared wiring plus any extra options.
func New(cfg *config.Config, opts ...fx.Option) *fx.App {
	return fx.New(Options(cfg, opts...)...)
}
