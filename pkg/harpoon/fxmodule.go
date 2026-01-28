package harpoon

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"go.uber.org/fx"

	"go.openai.org/api/tunnel-client/pkg/config"
	"go.openai.org/api/tunnel-client/pkg/health"
	tclog "go.openai.org/api/tunnel-client/pkg/log"
)

// Module wires the harpoon MCP server.
var Module = fx.Module(
	"harpoon",
	fx.Provide(newHarpoonService),
	fx.Invoke(registerAdditionalTransport),
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
	Config        *config.HarpoonConfig
	Health        *config.HealthConfig
	HealthSvc     health.Service
	AdminMux      *http.ServeMux `name:"admin_mux"`
	AdminUIConfig *config.AdminUIConfig
	Registrars    []TargetRegistrar `group:"harpoon_target_registrars"`
}

type harpoonOutputs struct {
	fx.Out

	Server           *Server
	Registry         *Registry
	HarpoonTransport mcp.Transport `name:"harpoon_in_memory_transport"`
}

func newHarpoonService(p harpoonParams) (harpoonOutputs, error) {
	if p.Config == nil {
		return harpoonOutputs{}, errors.New("harpoon: config is required")
	}
	registry, err := NewRegistry(p.Config.AllowPlaintextHTTP, convertTargets(p.Config.Targets))
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
	logger := p.Logger
	if logger == nil {
		logger = slog.Default()
	}
	server, err := NewServer(p.Config, registry, logger)
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
			logger.Info("harpoon enabled",
				slog.Int("target_count", len(targets)),
				slog.Bool("allow_plaintext_http", p.Config.AllowPlaintextHTTP),
				slog.Any("transports", transports),
				slog.String("http_endpoint", httpEndpoint),
				slog.Any("targets", registry.SummarizeTargets()),
				slog.String(tclog.FieldComponent, tclog.ComponentHarpoon),
			)
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
		HarpoonTransport: clientTransport,
	}, nil
}

type additionalTransportParams struct {
	fx.In

	AdminMux      *http.ServeMux `name:"admin_mux"`
	AdminUIConfig *config.AdminUIConfig
	Config        *config.HarpoonConfig
	Server        *Server
	Logger        *slog.Logger
}

func registerAdditionalTransport(p additionalTransportParams) error {
	if p.Config == nil || p.Server == nil || p.AdminMux == nil {
		return nil
	}
	if !p.Config.AdditionalTransportEnabled(config.HarpoonTransportHTTPStreamable) {
		// No log here to avoid noise when the transport is intentionally disabled.
		return nil
	}
	logger := p.Logger
	if logger == nil {
		logger = slog.Default()
	}
	streamServer := p.Server.MCPServer()
	handler := mcp.NewStreamableHTTPHandler(func(*http.Request) *mcp.Server {
		return streamServer
	}, nil)
	guarded := guardHandler(handler, p.AdminUIConfig)
	p.AdminMux.Handle("/harpoon/mcp", guarded)
	logger.Info("harpoon streamable transport enabled", slog.String("path", "/harpoon/mcp"), slog.String(tclog.FieldComponent, tclog.ComponentHarpoon))
	return nil
}

func guardHandler(next http.Handler, cfg *config.AdminUIConfig) http.Handler {
	if cfg != nil && cfg.AllowRemote {
		return next
	}
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !isLoopbackRequest(r) {
			http.Error(w, "harpoon transport is restricted to loopback; set --allow-remote-ui to override", http.StatusForbidden)
			return
		}
		next.ServeHTTP(w, r)
	})
}

func isLoopbackRequest(r *http.Request) bool {
	if r == nil {
		return false
	}
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		return false
	}
	ip := net.ParseIP(host)
	if ip == nil {
		return false
	}
	return ip.IsLoopback()
}

func buildHarpoonHTTPEndpoint(healthCfg *config.HealthConfig, svc health.Service, timeout time.Duration) string {
	if svc != nil {
		if addr, err := svc.Addr(timeout); err == nil && addr != "" {
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
			Label:       target.Label,
			Description: target.Description,
			BaseURL:     target.BaseURL,
		})
	}
	return out
}
