package runtimeharpoon

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"os"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	sdkmetric "go.opentelemetry.io/otel/sdk/metric"
	"go.uber.org/fx"

	"github.com/openai/tunnel-client/pkg/healthurl"
	"github.com/openai/tunnel-client/pkg/httpguard"
	tclog "github.com/openai/tunnel-client/pkg/log"
	"github.com/openai/tunnel-client/pkg/proxy"
	"github.com/openai/tunnel-client/pkg/runtimeconfig"
	"github.com/openai/tunnel-client/pkg/runtimehealth"
	"github.com/openai/tunnel-client/pkg/tlsconfig"
	tctransport "github.com/openai/tunnel-client/pkg/transport"
)

// TargetRegistrar allows programmatic target registration during startup.
type TargetRegistrar func(*Registry) error

// WithTarget returns a registrar that registers the provided target.
func WithTarget(target Target) TargetRegistrar {
	return func(registry *Registry) error {
		return registry.RegisterTarget(target)
	}
}

// MCPServerProvider is the narrow contract needed by shared Harpoon wiring.
// Full-client adapters can add tools and call observers without duplicating
// registry, transport, lifecycle, or route-log behavior.
type MCPServerProvider interface {
	MCPServer() *mcp.Server
}

// ServiceFactory constructs the flavor-specific MCP server over the shared
// registry and transport options.
type ServiceFactory func(*Registry, *slog.Logger, []ServerOption) (MCPServerProvider, error)

// SharedServiceParams contains the runtime-safe dependencies shared by the
// runtime and full-client Harpoon graphs.
type SharedServiceParams struct {
	Lifecycle     fx.Lifecycle
	Logger        *slog.Logger
	MeterProvider *sdkmetric.MeterProvider
	Config        *runtimeconfig.HarpoonConfig
	Health        *runtimeconfig.HealthConfig
	HealthSvc     runtimehealth.Service
	TLSBundle     *tlsconfig.Bundle
	Registrars    []TargetRegistrar
	NewServer     ServiceFactory
}

// SharedServiceOutputs contains the common outputs produced for either
// Harpoon flavor.
type SharedServiceOutputs struct {
	Registry         *Registry
	HarpoonTransport mcp.Transport
}

// NewSharedService owns the common Harpoon registry, outbound transport, route
// logging, lifecycle, and in-memory MCP transport wiring.
func NewSharedService(p SharedServiceParams) (SharedServiceOutputs, error) {
	if p.Config == nil {
		return SharedServiceOutputs{}, errors.New("harpoon: config is required")
	}
	if p.Logger == nil {
		return SharedServiceOutputs{}, errors.New("harpoon: logger is required")
	}
	if p.Lifecycle == nil {
		return SharedServiceOutputs{}, errors.New("harpoon: lifecycle is required")
	}
	if p.NewServer == nil {
		return SharedServiceOutputs{}, errors.New("harpoon: server factory is required")
	}

	registry, err := NewRegistry(p.Logger, p.Config.AllowPlaintextHTTP, ConvertTargets(p.Config.Targets))
	if err != nil {
		return SharedServiceOutputs{}, err
	}
	for _, registrar := range p.Registrars {
		if registrar == nil {
			continue
		}
		if err := registrar(registry); err != nil {
			return SharedServiceOutputs{}, err
		}
	}

	serverOptions := make([]ServerOption, 0, 2)
	if p.MeterProvider != nil {
		serverOptions = append(serverOptions, WithMeter(p.MeterProvider.Meter("harpoon")))
	}
	httpTransport, err := tctransport.CloneDefaultWithBundle(p.TLSBundle)
	if err != nil {
		return SharedServiceOutputs{}, err
	}
	httpTransport, err = tctransport.ApplyProxy(httpTransport, p.Config.HTTPProxy)
	if err != nil {
		return SharedServiceOutputs{}, err
	}
	serverOptions = append(serverOptions, WithHTTPTransport(httpTransport))
	server, err := p.NewServer(registry, p.Logger, serverOptions)
	if err != nil {
		return SharedServiceOutputs{}, err
	}
	if server == nil {
		return SharedServiceOutputs{}, errors.New("harpoon: server factory returned nil")
	}

	mcpServer := server.MCPServer()
	ctx, cancel := context.WithCancel(context.Background())
	clientTransport := newRestartableInMemoryTransport(ctx, mcpServer, p.Logger)
	p.Lifecycle.Append(fx.Hook{
		OnStart: func(context.Context) error {
			targets := registry.Targets()
			transports := []string{"in_memory"}
			httpEndpoint := ""
			if p.Config.AdditionalTransportEnabled(runtimeconfig.HarpoonTransportHTTPStreamable) {
				transports = append(transports, "http_streamable")
				httpEndpoint = BuildHarpoonHTTPEndpoint(p.Health, p.HealthSvc, 2*time.Second)
			}
			logFields := []any{
				slog.Int("target_count", len(targets)),
				slog.Bool("allow_plaintext_http", p.Config.AllowPlaintextHTTP),
				slog.Any("transports", transports),
				slog.String("http_endpoint", httpEndpoint),
				slog.Any("targets", registry.SummarizeTargets()),
				slog.String(tclog.FieldComponent, tclog.ComponentHarpoon),
			}
			p.Logger.Info("harpoon enabled", logFields...)
			for _, target := range targets {
				route := proxy.ResolveRoute(proxy.RouteKindHarpoon, target.Label, target.BaseURL, p.Config.HTTPProxy, p.Config.HTTPProxySource, os.LookupEnv)
				if target.UnixSocketPath != "" {
					route = proxy.ResolveRoute(proxy.RouteKindHarpoon, target.Label, target.BaseURL, nil, runtimeconfig.ProxySourceIgnored, func(string) (string, bool) {
						return "", false
					})
					route.ProxySource = runtimeconfig.ProxySourceIgnored
				}
				routeFields := []any{
					slog.String("route_kind", string(route.Kind)),
					slog.String("route_name", route.Name),
					slog.String("target_host", route.TargetHostPort),
					slog.String(tclog.FieldComponent, tclog.ComponentHarpoon),
				}
				routeFields = append(routeFields, proxy.LogFields(route)...)
				p.Logger.Info("harpoon route resolved", routeFields...)
			}
			return nil
		},
		OnStop: func(context.Context) error {
			cancel()
			return nil
		},
	})

	return SharedServiceOutputs{
		Registry:         registry,
		HarpoonTransport: clientTransport,
	}, nil
}

// AdditionalTransportParams contains the shared inputs for registering the
// optional loopback-only HTTP streamable Harpoon transport.
type AdditionalTransportParams struct {
	Lifecycle  fx.Lifecycle
	GuardedMux httpguard.GuardedMux
	Config     *runtimeconfig.HarpoonConfig
	Server     MCPServerProvider
	Logger     *slog.Logger
}

// RegisterAdditionalTransport owns the common HTTP streamable transport
// registration for both runtime and full-client Harpoon graphs.
func RegisterAdditionalTransport(p AdditionalTransportParams) error {
	if p.Config == nil || p.Server == nil {
		return nil
	}
	if !p.Config.AdditionalTransportEnabled(runtimeconfig.HarpoonTransportHTTPStreamable) {
		return nil
	}
	if p.Lifecycle == nil {
		return fmt.Errorf("harpoon: lifecycle is required for http-streamable transport")
	}
	if p.Logger == nil {
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
	statefulHandler := mcp.NewStreamableHTTPHandler(func(*http.Request) *mcp.Server {
		return streamServer
	}, nil)
	statelessHandler := mcp.NewStreamableHTTPHandler(func(*http.Request) *mcp.Server {
		return streamServer
	}, &mcp.StreamableHTTPOptions{
		// Harpoon's tools do not depend on MCP session state. Keep the optional
		// loopback Streamable HTTP surface aligned with the tunneled Harpoon
		// channel so 2026-07-28 self-contained requests can use it directly.
		Stateless: true,
	})
	var handler http.Handler = http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		legacy, err := isLegacyHarpoonStreamableRequest(req)
		if err != nil {
			var maxBytesErr *http.MaxBytesError
			if errors.As(err, &maxBytesErr) {
				http.Error(w, "request body too large", http.StatusRequestEntityTooLarge)
				return
			}
			http.Error(w, "failed to read request body", http.StatusBadRequest)
			return
		}
		if legacy {
			statefulHandler.ServeHTTP(w, req)
			return
		}
		statelessHandler.ServeHTTP(w, req)
	})
	handler = httpguard.WithShutdownContext(handler, streamCtx)
	p.GuardedMux.Handle("/harpoon/mcp", handler)
	p.Logger.Info("harpoon streamable transport enabled", slog.String("path", "/harpoon/mcp"), slog.String(tclog.FieldComponent, tclog.ComponentHarpoon))
	return nil
}

// isLegacyHarpoonStreamableRequest preserves the pre-v1.7 HTTP session surface
// while allowing new self-contained requests to use the SDK's stateless mode.
// A session ID, GET, or DELETE is necessarily legacy. An initialize POST must
// also stay stateful so its subsequent standalone SSE GET and DELETE can find
// the session that it created.
func isLegacyHarpoonStreamableRequest(req *http.Request) (bool, error) {
	if req.Method != http.MethodPost || req.Header.Get("Mcp-Session-Id") != "" {
		return true, nil
	}

	// Match the SDK handlers' default Streamable HTTP limit while classifying;
	// this prevents the compatibility shim from buffering a body the selected
	// handler would reject anyway.
	body, readErr := io.ReadAll(io.LimitReader(req.Body, mcp.DefaultMaxRequestBodyBytes+1))
	closeErr := req.Body.Close()
	if readErr != nil {
		return false, readErr
	}
	if len(body) > mcp.DefaultMaxRequestBodyBytes {
		return false, &http.MaxBytesError{Limit: mcp.DefaultMaxRequestBodyBytes}
	}
	if closeErr != nil {
		return false, closeErr
	}
	req.Body = io.NopCloser(bytes.NewReader(body))

	type methodEnvelope struct {
		Method string `json:"method"`
	}
	var request methodEnvelope
	if err := json.Unmarshal(body, &request); err == nil {
		return isLegacyHarpoonStreamableMethod(request.Method), nil
	}

	var batch []methodEnvelope
	if err := json.Unmarshal(body, &batch); err == nil {
		for _, request := range batch {
			if isLegacyHarpoonStreamableMethod(request.Method) {
				return true, nil
			}
		}
	}
	return false, nil
}

func isLegacyHarpoonStreamableMethod(method string) bool {
	return method == "initialize" || method == "notifications/initialized"
}

// NewGuardedMux creates the loopback-only Harpoon HTTP transport mux.
func NewGuardedMux(adminMux *http.ServeMux) httpguard.GuardedMux {
	return httpguard.NewGuardedMux(
		adminMux,
		false,
		"harpoon transport is restricted to loopback",
	)
}

// BuildHarpoonHTTPEndpoint resolves the operator-facing streamable endpoint.
func BuildHarpoonHTTPEndpoint(healthCfg *runtimeconfig.HealthConfig, svc runtimehealth.Service, timeout time.Duration) string {
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

// ConvertTargets projects config targets into registered runtime targets.
func ConvertTargets(targets []runtimeconfig.HarpoonTarget) []Target {
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
