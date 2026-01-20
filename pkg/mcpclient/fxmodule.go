package mcpclient

import (
	"context"
	"fmt"
	"log/slog"
	"net/http"
	"net/url"
	"strings"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"go.openai.org/api/tunnel-client/pkg/config"
	tclog "go.openai.org/api/tunnel-client/pkg/log"
	tcmetrics "go.openai.org/api/tunnel-client/pkg/metrics"
	tctransport "go.openai.org/api/tunnel-client/pkg/transport"
	"go.openai.org/api/tunnel-client/pkg/version"
	"go.opentelemetry.io/contrib/instrumentation/net/http/otelhttp"
	sdkmetric "go.opentelemetry.io/otel/sdk/metric"
	"go.uber.org/fx"

	"go.openai.org/api/tunnel-client/pkg/mcpclient/internal"
)

var Module = fx.Module(
	"mcpclient",
	fx.Provide(
		newMcpClient,
		fx.Annotate(newStreamableTransportProvider, fx.ResultTags(`group:"mcp_transport_providers"`)),
		fx.Annotate(newInjectableTransportProvider, fx.ResultTags(`group:"mcp_transport_providers"`)),
		fx.Annotate(newStdioTransportProvider, fx.ResultTags(`group:"mcp_transport_providers"`)),
	),
	fx.Invoke(probeMcpServer),
)

type clientParams struct {
	fx.In

	Config             *config.MCPConfig
	Logging            *config.LoggingConfig
	Logger             *slog.Logger
	MeterProvider      *sdkmetric.MeterProvider
	TransportProviders []TransportProvider `group:"mcp_transport_providers"`
}

type clientOutputs struct {
	fx.Out

	Client              *mcp.Client
	Transport           mcp.Transport
	ForwardingTransport ForwardingTransport
	HTTPClient          *http.Client `name:"mcp_client"`
}

type runnerParams struct {
	fx.In

	Config    *config.MCPConfig
	Client    *mcp.Client
	Transport mcp.Transport
	Lifecycle fx.Lifecycle
	Logger    *slog.Logger
}

func newMcpClient(p clientParams) (clientOutputs, error) {
	if p.Config == nil {
		return clientOutputs{}, fmt.Errorf("mcpclient: mcp config is required")
	}
	if p.Logger == nil || p.Logging == nil || p.MeterProvider == nil {
		return clientOutputs{}, fmt.Errorf("mcpclient: logger, logging config, and meter provider are required")
	}

	mcpClient := mcp.NewClient(&mcp.Implementation{Name: "tunnel-client", Version: version.Version}, nil)

	httpClient := &http.Client{Transport: buildMcpHTTPTransport(p.Logger, p.Logging, p.MeterProvider)}
	transportKind := config.MCPTransportHTTPStreamable
	if p.Config != nil && p.Config.TransportKind != "" {
		transportKind = p.Config.TransportKind
	}
	provider, err := selectTransportProvider(transportKind, p.TransportProviders)
	if err != nil {
		return clientOutputs{}, err
	}
	mcpTransport, err := provider.Build(TransportBuildParams{
		Config:     p.Config,
		HTTPClient: httpClient,
	})
	if err != nil {
		return clientOutputs{}, err
	}

	if p.Logging.HTTPRawUnsafe && p.Logging.Level <= slog.LevelDebug {
		logger := p.Logger.With(tclog.FieldComponent, tclog.ComponentMcpClient, "transport", "raw_http")
		mcpTransport = &mcp.LoggingTransport{
			Transport: mcpTransport,
			Writer:    slogWriter{logger: logger},
		}
	}

	return clientOutputs{
		Client:              mcpClient,
		Transport:           mcpTransport,
		ForwardingTransport: NewForwardingTransport(mcpTransport),
		HTTPClient:          httpClient,
	}, nil
}

// probeMcpServer performs a one-time discovery handshake to confirm connectivity and record server metadata.
func probeMcpServer(p runnerParams) error {
	if p.Config == nil {
		return fmt.Errorf("mcpclient: mcp config is required")
	}
	transportKind := config.MCPTransportHTTPStreamable
	if p.Config.TransportKind != "" {
		transportKind = p.Config.TransportKind
	}
	if transportKind != config.MCPTransportHTTPStreamable {
		if p.Logger != nil {
			p.Logger.Info("Skipping MCP probe for transport", slog.String("transport", string(transportKind)))
		}
		return nil
	}
	if transportKind == config.MCPTransportHTTPStreamable && p.Config.ServerURL == nil {
		return fmt.Errorf("mcpclient: server URL is required for %s transport", transportKind)
	}

	logger := p.Logger.With(tclog.FieldComponent, tclog.ComponentMcpClient)
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})

	p.Lifecycle.Append(fx.Hook{
		OnStart: func(context.Context) error {
			logger.InfoContext(ctx, "Probing MCP server",
				slog.String("transport", string(transportKind)),
				slog.String("target", transportTargetLabel(transportKind, p.Config.ServerURL)),
			)
			go func() {
				defer close(done)
				sess, err := p.Client.Connect(ctx, p.Transport, nil)
				if err != nil {
					logger.ErrorContext(ctx, "failed to connect to mcp", slog.String("error", err.Error()))
					return
				}
				defer func() {
					if err := sess.Close(); err != nil {
						logger.WarnContext(ctx, "failed to close mcp session", slog.String("error", err.Error()))
					}
				}()
				initRes := sess.InitializeResult()
				logFields := []any{
					slog.String("protocol_version", initRes.ProtocolVersion),
				}
				if initRes.ServerInfo != nil {
					logFields = append(logFields, slog.String("server_name", initRes.ServerInfo.Name))
					if initRes.ServerInfo.Version != "" {
						logFields = append(logFields, slog.String("server_version", initRes.ServerInfo.Version))
					}
				}
				logger.InfoContext(ctx, "mcp session initialized", logFields...)
			}()
			return nil
		},
		OnStop: func(context.Context) error {
			cancel()
			<-done
			return nil
		},
	})

	return nil
}

type slogWriter struct {
	logger *slog.Logger
}

func (w slogWriter) Write(p []byte) (int, error) {
	if w.logger == nil {
		return len(p), nil
	}
	msg := strings.TrimRight(string(p), "\n")
	w.logger.Debug(msg)
	return len(p), nil
}

func buildMcpHTTPTransport(logger *slog.Logger, loggingCfg *config.LoggingConfig, meterProvider *sdkmetric.MeterProvider) http.RoundTripper {
	// Order matters (outermost to innermost):
	//   1. Forwarding injects headers before anything else touches the request.
	//   2. Logging wraps otel instrumentation so raw dumps include forwarded headers.
	//   3. otelhttp instrumentation sits closest to the network to record final calls.
	base := tctransport.CloneDefault()
	base = otelhttp.NewTransport(
		base,
		otelhttp.WithMeterProvider(meterProvider),
		tcmetrics.WithHTTPClientMetricAttributesFn(),
	)
	forwardingLogger := logger.With(
		slog.String(tclog.FieldComponent, tclog.ComponentMcpClient),
		slog.String("transport", "forwarding_rt"),
	)
	base = tclog.NewRoundTripper(base, forwardingLogger, loggingCfg, tclog.ComponentMcpClient)
	return internal.NewForwardingRoundTripper(base)
}

func transportTargetLabel(kind config.MCPTransportKind, serverURL *url.URL) string {
	if kind == config.MCPTransportHTTPStreamable && serverURL != nil {
		return serverURL.String()
	}
	if kind == "" {
		return string(config.MCPTransportHTTPStreamable)
	}
	return string(kind)
}
