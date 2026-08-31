package runtimeharpoon

import (
	"log/slog"
	"net/http"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	sdkmetric "go.opentelemetry.io/otel/sdk/metric"
	"go.uber.org/fx"

	"github.com/openai/tunnel-client/pkg/httpguard"
	"github.com/openai/tunnel-client/pkg/runtimeconfig"
	"github.com/openai/tunnel-client/pkg/runtimehealth"
	"github.com/openai/tunnel-client/pkg/tlsconfig"
)

// Module wires the harpoon MCP server.
var Module = fx.Module(
	"harpoon",
	fx.Provide(newHarpoonService, newRegistryCounter, newHarpoonGuardedMux, NewHostBusSubscriber, NewHostBus, NewStartupCatalogDigestState),
	fx.Invoke(registerAdditionalTransport, StartHostRegistration, StartCatalogDigestLogging),
)

func newRegistryCounter(registry *Registry) RegistryCounter {
	return registry
}

type harpoonParams struct {
	fx.In

	Lifecycle     fx.Lifecycle
	Logger        *slog.Logger
	MeterProvider *sdkmetric.MeterProvider `optional:"true"`
	Config        *runtimeconfig.HarpoonConfig
	Health        *runtimeconfig.HealthConfig
	HealthSvc     runtimehealth.Service
	AdminMux      *http.ServeMux `name:"admin_mux"`
	TLSBundle     *tlsconfig.Bundle
	Registrars    []TargetRegistrar `group:"harpoon_target_registrars"`
}

type harpoonOutputs struct {
	fx.Out

	Server           *Server
	Registry         *Registry
	HarpoonTransport mcp.Transport `name:"harpoon_in_memory_transport"`
}

func newHarpoonService(p harpoonParams) (harpoonOutputs, error) {
	var server *Server
	shared, err := NewSharedService(SharedServiceParams{
		Lifecycle:     p.Lifecycle,
		Logger:        p.Logger,
		MeterProvider: p.MeterProvider,
		Config:        p.Config,
		Health:        p.Health,
		HealthSvc:     p.HealthSvc,
		TLSBundle:     p.TLSBundle,
		Registrars:    p.Registrars,
		NewServer: func(registry *Registry, logger *slog.Logger, opts []ServerOption) (MCPServerProvider, error) {
			var err error
			server, err = NewServer(p.Config, registry, logger, opts...)
			return server, err
		},
	})
	if err != nil {
		return harpoonOutputs{}, err
	}
	return harpoonOutputs{
		Server:           server,
		Registry:         shared.Registry,
		HarpoonTransport: shared.HarpoonTransport,
	}, nil
}

type additionalTransportParams struct {
	fx.In

	Lifecycle  fx.Lifecycle
	GuardedMux httpguard.GuardedMux
	Config     *runtimeconfig.HarpoonConfig
	Server     *Server
	Logger     *slog.Logger
}

func registerAdditionalTransport(p additionalTransportParams) error {
	return RegisterAdditionalTransport(AdditionalTransportParams{
		Lifecycle:  p.Lifecycle,
		GuardedMux: p.GuardedMux,
		Config:     p.Config,
		Server:     p.Server,
		Logger:     p.Logger,
	})
}

type guardedMuxParams struct {
	fx.In

	AdminMux *http.ServeMux `name:"admin_mux"`
}

func newHarpoonGuardedMux(p guardedMuxParams) httpguard.GuardedMux {
	return NewGuardedMux(p.AdminMux)
}
