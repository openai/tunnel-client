package app

import (
	"errors"
	"net/http"
	"time"

	"go.uber.org/fx"

	"github.com/openai/tunnel-client/pkg/adminui"
	"github.com/openai/tunnel-client/pkg/config"
	controlplane "github.com/openai/tunnel-client/pkg/controlplane/fx"
	"github.com/openai/tunnel-client/pkg/dispatcher"
	"github.com/openai/tunnel-client/pkg/harpoon"
	"github.com/openai/tunnel-client/pkg/health"
	"github.com/openai/tunnel-client/pkg/log"
	"github.com/openai/tunnel-client/pkg/mcpclient"
	"github.com/openai/tunnel-client/pkg/metrics"
	"github.com/openai/tunnel-client/pkg/oauth"
	"github.com/openai/tunnel-client/pkg/process"
	"github.com/openai/tunnel-client/pkg/proxyhealth"
	"github.com/openai/tunnel-client/pkg/tlsconfig"
)

// RuntimeOptions controls optional app surfaces for embedders.
type RuntimeOptions struct {
	// DisableHealthAdmin omits the local health/admin HTTP listener. The normal
	// tunnel-client binary should leave this false; local test embedders can set
	// it when they only need the control-plane poller and MCP dispatcher.
	DisableHealthAdmin bool
}

// Options returns the Fx options required to wire the tunnel-client services.
// Additional Fx options can be provided to customize the runtime.
func Options(cfg *config.Config, opts ...fx.Option) []fx.Option {
	return OptionsWithRuntime(cfg, RuntimeOptions{}, opts...)
}

// OptionsWithRuntime returns the Fx options required to wire the tunnel-client
// services with explicit runtime surface controls.
func OptionsWithRuntime(cfg *config.Config, runtime RuntimeOptions, opts ...fx.Option) []fx.Option {
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
			&cfg.Harpoon,
			&cfg.ProxyHealth,
		),
		fx.Provide(func() *tlsconfig.Bundle { return cfg.TLS }),
		log.Module,
		dispatcher.Module,
		controlplane.Module,
		harpoon.Module,
		mcpclient.Module,
		metrics.MetricModule,
		oauth.Module,
		process.Module,
		proxyhealth.Module,
		fx.Invoke(tlsconfig.LogTrustReport),
	}

	if runtime.DisableHealthAdmin {
		base = append(base, disabledHealthAdminModule)
	} else {
		base = append(base,
			health.HealthMuxModule,
			adminui.Module,
			fx.Invoke(func(health.Service) {}),
		)
	}

	return append(base, opts...)
}

// New constructs a tunnel-client Fx app using the shared wiring plus any extra options.
func New(cfg *config.Config, opts ...fx.Option) *fx.App {
	return fx.New(Options(cfg, opts...)...)
}

// NewWithRuntime constructs a tunnel-client Fx app with explicit runtime
// surface controls plus any extra options.
func NewWithRuntime(cfg *config.Config, runtime RuntimeOptions, opts ...fx.Option) *fx.App {
	return fx.New(OptionsWithRuntime(cfg, runtime, opts...)...)
}

var disabledHealthAdminModule = fx.Module(
	"disabled_health_admin",
	fx.Provide(
		fx.Annotate(
			func() *http.ServeMux { return http.NewServeMux() },
			fx.ResultTags(`name:"admin_mux"`),
		),
		func() health.Service { return disabledHealthService{} },
	),
)

type disabledHealthService struct{}

func (disabledHealthService) Addr(time.Duration) (string, error) {
	return "", errors.New("tunnel-client health/admin listener disabled")
}
