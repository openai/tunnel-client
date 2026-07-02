package harpoon

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"os"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	sdkmetric "go.opentelemetry.io/otel/sdk/metric"
	"go.uber.org/fx"

	"github.com/openai/tunnel-client/pkg/config"
	"github.com/openai/tunnel-client/pkg/harpoon/hostbus"
	"github.com/openai/tunnel-client/pkg/health"
	"github.com/openai/tunnel-client/pkg/healthurl"
	"github.com/openai/tunnel-client/pkg/httpguard"
	tclog "github.com/openai/tunnel-client/pkg/log"
	"github.com/openai/tunnel-client/pkg/proxy"
	"github.com/openai/tunnel-client/pkg/tlsconfig"
	tctransport "github.com/openai/tunnel-client/pkg/transport"
)

// Module wires the harpoon MCP server.
var Module = fx.Module(
	"harpoon",
	fx.Provide(newHarpoonService, newHarpoonGuardedMux, newHostBusSubscriber, newHostBus),
	fx.Invoke(registerAdditionalTransport, startHostRegistration),
)

// TargetRegistrar allows programmatic target registration during startup.
type TargetRegistrar func(*Registry) error

// WithTarget returns a registrar that registers the provided target.
func WithTarget(target Target) TargetRegistrar {
	return func(registry *Registry) error {
		return registry.RegisterTarget(target)
	}
}

type harpoonParams struct {
	fx.In

	Lifecycle     fx.Lifecycle
	Logger        *slog.Logger
	MeterProvider *sdkmetric.MeterProvider `optional:"true"`
	Config        *config.HarpoonConfig
	Health        *config.HealthConfig
	HealthSvc     health.Service
	AdminMux      *http.ServeMux `name:"admin_mux"`
	AdminUIConfig *config.AdminUIConfig
	TLSBundle     *tlsconfig.Bundle
	Registrars    []TargetRegistrar `group:"harpoon_target_registrars"`
}

type harpoonOutputs struct {
	fx.Out

	Server           *Server
	Registry         *Registry
	CallBuffer       *CallBuffer
	HarpoonTransport mcp.Transport `name:"harpoon_in_memory_transport"`
}

func newHarpoonService(p harpoonParams) (harpoonOutputs, error) {
	if p.Config == nil {
		return harpoonOutputs{}, errors.New("harpoon: config is required")
	}
	logger := p.Logger
	if logger == nil {
		return harpoonOutputs{}, errors.New("harpoon: logger is required")
	}
	registry, err := NewRegistry(logger, p.Config.AllowPlaintextHTTP, convertTargets(p.Config.Targets))
	if err != nil {
		return harpoonOutputs{}, err
	}
	for _, registrar := range p.Registrars {
		if registrar == nil {
			continue
		}
		if err := registrar(registry); err != nil {
			return harpoonOutputs{}, err
		}
	}
	buffer := NewCallBuffer()
	serverOptions := make([]ServerOption, 0, 1)
	if p.MeterProvider != nil {
		serverOptions = append(serverOptions, WithMeter(p.MeterProvider.Meter("harpoon")))
	}
	httpTransport, err := tctransport.CloneDefaultWithBundle(p.TLSBundle)
	if err != nil {
		return harpoonOutputs{}, err
	}
	httpTransport, err = tctransport.ApplyProxy(httpTransport, p.Config.HTTPProxy)
	if err != nil {
		return harpoonOutputs{}, err
	}
	serverOptions = append(serverOptions, WithHTTPTransport(httpTransport))
	server, err := NewServer(p.Config, registry, buffer, logger, serverOptions...)
	if err != nil {
		return harpoonOutputs{}, err
	}
	mcpServer := server.MCPServer()
	serverTransport, clientTransport := mcp.NewInMemoryTransports()
	ctx, cancel := context.WithCancel(context.Background())
	p.Lifecycle.Append(fx.Hook{
		OnStart: func(context.Context) error {
			targets := registry.Targets()
			transports := []string{"in_memory"}
			httpEndpoint := ""
			if p.Config.AdditionalTransportEnabled(config.HarpoonTransportHTTPStreamable) {
				transports = append(transports, "http_streamable")
				httpEndpoint = buildHarpoonHTTPEndpoint(p.Health, p.HealthSvc, 2*time.Second)
			}
			logFields := []any{
				slog.Int("target_count", len(targets)),
				slog.Bool("allow_plaintext_http", p.Config.AllowPlaintextHTTP),
				slog.Any("transports", transports),
				slog.String("http_endpoint", httpEndpoint),
				slog.Any("targets", registry.SummarizeTargets()),
				slog.String(tclog.FieldComponent, tclog.ComponentHarpoon),
			}
			logger.Info("harpoon enabled", logFields...)
			for _, target := range targets {
				route := proxy.ResolveRoute(proxy.RouteKindHarpoon, target.Label, target.BaseURL, p.Config.HTTPProxy, p.Config.HTTPProxySource, os.LookupEnv)
				if target.UnixSocketPath != "" {
					route = proxy.ResolveRoute(proxy.RouteKindHarpoon, target.Label, target.BaseURL, nil, config.ProxySourceIgnored, func(string) (string, bool) {
						return "", false
					})
					route.ProxySource = config.ProxySourceIgnored
				}
				routeFields := []any{
					slog.String("route_kind", string(route.Kind)),
					slog.String("route_name", route.Name),
					slog.String("target_host", route.TargetHostPort),
					slog.String(tclog.FieldComponent, tclog.ComponentHarpoon),
				}
				routeFields = append(routeFields, proxy.LogFields(route)...)
				logger.Info("harpoon route resolved", routeFields...)
			}
			go func() {
				if err := mcpServer.Run(ctx, serverTransport); err != nil {
					logger.Error("harpoon server stopped", slog.String("error", err.Error()))
				}
			}()
			return nil
		},
		OnStop: func(context.Context) error {
			cancel()
			return nil
		},
	})

	return harpoonOutputs{
		Server:           server,
		Registry:         registry,
		CallBuffer:       buffer,
		HarpoonTransport: clientTransport,
	}, nil
}

type additionalTransportParams struct {
	fx.In

	Lifecycle  fx.Lifecycle
	GuardedMux httpguard.GuardedMux
	Config     *config.HarpoonConfig
	Server     *Server
	Logger     *slog.Logger
}

type guardedMuxParams struct {
	fx.In

	AdminMux *http.ServeMux `name:"admin_mux"`
	Config   *config.AdminUIConfig
}

func newHarpoonGuardedMux(p guardedMuxParams) httpguard.GuardedMux {
	return httpguard.NewGuardedMux(
		p.AdminMux,
		false,
		"harpoon transport is restricted to loopback",
	)
}

func newHostBusSubscriber() hostBusSubscriberOut {
	return hostBusSubscriberOut{Subscriber: make(chan hostbus.URLBundle, 16)}
}

func registerAdditionalTransport(p additionalTransportParams) error {
	if p.Config == nil || p.Server == nil {
		return nil
	}
	if !p.Config.AdditionalTransportEnabled(config.HarpoonTransportHTTPStreamable) {
		// No log here to avoid noise when the transport is intentionally disabled.
		return nil
	}
	if p.Lifecycle == nil {
		return fmt.Errorf("harpoon: lifecycle is required for http-streamable transport")
	}
	logger := p.Logger
	if logger == nil {
		return fmt.Errorf("harpoon: logger is required for http-streamable transport")
	}
	streamCtx, streamCancel := context.WithCancel(context.Background())
	p.Lifecycle.Append(fx.Hook{
		OnStop: func(context.Context) error {
			streamCancel()
			return nil
		},
	})
	streamServer := p.Server.MCPServer()
	var handler http.Handler = mcp.NewStreamableHTTPHandler(func(*http.Request) *mcp.Server {
		return streamServer
	}, nil)
	handler = httpguard.WithShutdownContext(handler, streamCtx)
	p.GuardedMux.Handle("/harpoon/mcp", handler)
	logger.Info("harpoon streamable transport enabled", slog.String("path", "/harpoon/mcp"), slog.String(tclog.FieldComponent, tclog.ComponentHarpoon))
	return nil
}

func buildHarpoonHTTPEndpoint(healthCfg *config.HealthConfig, svc health.Service, timeout time.Duration) string {
	if svc != nil {
		if addr, err := svc.Addr(timeout); err == nil && addr != "" {
			if healthCfg != nil && healthCfg.UnixSocket != "" {
				return healthurl.BuildUnixBaseURL(healthCfg.UnixSocket) + "/harpoon/mcp"
			}
			return fmt.Sprintf("http://%s/harpoon/mcp", addr)
		}
	}
	if healthCfg == nil || healthCfg.ListenAddr == "" {
		return ""
	}
	host, port, err := net.SplitHostPort(healthCfg.ListenAddr)
	if err != nil || port == "" || port == "0" {
		return ""
	}
	if host == "" {
		host = "127.0.0.1"
	}
	return fmt.Sprintf("http://%s/harpoon/mcp", net.JoinHostPort(host, port))
}

func convertTargets(targets []config.HarpoonTarget) []Target {
	out := make([]Target, 0, len(targets))
	for _, target := range targets {
		out = append(out, Target{
			Label:          target.Label,
			Description:    target.Description,
			Category:       "config",
			Source:         "config",
			Tags:           nil,
			BaseURL:        target.BaseURL,
			UnixSocketPath: target.UnixSocketPath,
		})
	}
	return out
}
