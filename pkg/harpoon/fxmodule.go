package harpoon

import (
	"log/slog"
	"net/http"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	sdkmetric "go.opentelemetry.io/otel/sdk/metric"
	"go.uber.org/fx"

	"github.com/openai/tunnel-client/pkg/config"
	"github.com/openai/tunnel-client/pkg/health"
	"github.com/openai/tunnel-client/pkg/httpguard"
	"github.com/openai/tunnel-client/pkg/runtimeconfig"
	"github.com/openai/tunnel-client/pkg/runtimeharpoon"
	"github.com/openai/tunnel-client/pkg/tlsconfig"
)

// Module keeps only the full-client Harpoon extensions while the registry,
// transport, lifecycle, and host-registration wiring live in runtimeharpoon.
var Module = fx.Module(
	"harpoon",
	fx.Provide(
		newRuntimeConfig,
		newHarpoonService,
		newRuntimeRegistryCounter,
		newHarpoonGuardedMux,
		runtimeharpoon.NewHostBusSubscriber,
		runtimeharpoon.NewHostBus,
		runtimeharpoon.NewStartupCatalogDigestState,
	),
	fx.Invoke(registerAdditionalTransport, runtimeharpoon.StartHostRegistration, runtimeharpoon.StartCatalogDigestLogging),
)

func newRuntimeRegistryCounter(registry *Registry) runtimeharpoon.RegistryCounter {
	return registry
}

// TargetRegistrar is the shared runtime-core registration contract.
type TargetRegistrar = runtimeharpoon.TargetRegistrar

// WithTarget returns a registrar that registers the provided target.
func WithTarget(target Target) TargetRegistrar {
	return runtimeharpoon.WithTarget(target)
}

type harpoonParams struct {
	fx.In

	Lifecycle                fx.Lifecycle
	Logger                   *slog.Logger
	MeterProvider            *sdkmetric.MeterProvider `optional:"true"`
	Config                   *config.HarpoonConfig
	RuntimeConfig            *runtimeconfig.HarpoonConfig
	Health                   *config.HealthConfig
	HealthSvc                health.Service
	AdminMux                 *http.ServeMux `name:"admin_mux"`
	TLSBundle                *tlsconfig.Bundle
	Registrars               []TargetRegistrar `group:"harpoon_target_registrars"`
	LegacyProtocolForTesting bool              `name:"legacy_harpoon_protocol_for_testing" optional:"true"`
}

type harpoonOutputs struct {
	fx.Out

	Server           *Server
	Registry         *Registry
	CallBuffer       *CallBuffer
	HarpoonTransport mcp.Transport `name:"harpoon_in_memory_transport"`
}

func newRuntimeConfig(cfg *config.HarpoonConfig) *runtimeconfig.HarpoonConfig {
	return runtimeConfig(cfg)
}

func newHarpoonService(p harpoonParams) (harpoonOutputs, error) {
	buffer := NewCallBuffer()
	var server *Server
	shared, err := runtimeharpoon.NewSharedService(runtimeharpoon.SharedServiceParams{
		Lifecycle:                p.Lifecycle,
		Logger:                   p.Logger,
		MeterProvider:            p.MeterProvider,
		Config:                   p.RuntimeConfig,
		Health:                   p.Health,
		HealthSvc:                p.HealthSvc,
		TLSBundle:                p.TLSBundle,
		Registrars:               p.Registrars,
		LegacyProtocolForTesting: p.LegacyProtocolForTesting,
		NewServer: func(registry *runtimeharpoon.Registry, logger *slog.Logger, opts []runtimeharpoon.ServerOption) (runtimeharpoon.MCPServerProvider, error) {
			var err error
			server, err = NewServer(p.Config, registry, buffer, logger, opts...)
			return server, err
		},
	})
	if err != nil {
		return harpoonOutputs{}, err
	}
	return harpoonOutputs{
		Server:           server,
		Registry:         shared.Registry,
		CallBuffer:       buffer,
		HarpoonTransport: shared.HarpoonTransport,
	}, nil
}

type additionalTransportParams struct {
	fx.In

	Lifecycle     fx.Lifecycle
	GuardedMux    httpguard.GuardedMux
	RuntimeConfig *runtimeconfig.HarpoonConfig
	Server        *Server
	Logger        *slog.Logger
}

func registerAdditionalTransport(p additionalTransportParams) error {
	return runtimeharpoon.RegisterAdditionalTransport(runtimeharpoon.AdditionalTransportParams{
		Lifecycle:  p.Lifecycle,
		GuardedMux: p.GuardedMux,
		Config:     p.RuntimeConfig,
		Server:     p.Server,
		Logger:     p.Logger,
	})
}

type guardedMuxParams struct {
	fx.In

	AdminMux *http.ServeMux `name:"admin_mux"`
}

func newHarpoonGuardedMux(p guardedMuxParams) httpguard.GuardedMux {
	return runtimeharpoon.NewGuardedMux(p.AdminMux)
}
