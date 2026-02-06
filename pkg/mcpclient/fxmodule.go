package mcpclient

import (
	"context"
	"fmt"
	"log/slog"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"go.openai.org/api/tunnel-client/pkg/config"
	tclog "go.openai.org/api/tunnel-client/pkg/log"
	tcmetrics "go.openai.org/api/tunnel-client/pkg/metrics"
	"go.openai.org/api/tunnel-client/pkg/tlsconfig"
	tctransport "go.openai.org/api/tunnel-client/pkg/transport"
	"go.openai.org/api/tunnel-client/pkg/types"
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
		newStdioCommandTransportFactoryProvider,
		newChannelStdioRuntimeInfoProvider,
		newChannelTransportFactory,
		fx.Annotate(newStreamableTransportProvider, fx.ResultTags(`group:"mcp_transport_providers"`)),
		fx.Annotate(newInjectableTransportProvider, fx.ResultTags(`group:"mcp_transport_providers"`)),
		fx.Annotate(newStdioTransportProvider, fx.ResultTags(`group:"mcp_transport_providers"`)),
	),
	fx.Invoke(probeMcpServer),
)

const defaultProbeTimeout = 2 * time.Second

type clientParams struct {
	fx.In

	Config           *config.MCPConfig
	Logging          *config.LoggingConfig
	Logger           *slog.Logger
	MeterProvider    *sdkmetric.MeterProvider
	TransportFactory *ChannelTransportFactory
}

type clientOutputs struct {
	fx.Out

	Client     *mcp.Client
	Transport  mcp.Transport
	HTTPClient *http.Client `name:"mcp_client"`
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
	if p.Logger == nil || p.Logging == nil || p.MeterProvider == nil || p.TransportFactory == nil {
		return clientOutputs{}, fmt.Errorf("mcpclient: logger, logging config, meter provider, and transport factory are required")
	}

	mcpClient := mcp.NewClient(&mcp.Implementation{Name: "tunnel-client", Version: version.Version}, nil)
	mainBinding := p.Config.MainChannelBinding()
	if mainBinding == nil {
		legacyBinding := config.MCPChannelBinding{
			Channel:       types.DefaultChannel,
			TransportKind: p.Config.TransportKind,
			ServerURL:     p.Config.ServerURL,
			Command:       p.Config.Command,
			CommandArgs:   p.Config.CommandArgs,
		}
		mainBinding = &legacyBinding
	}
	transportKind := mainBinding.TransportKind
	if transportKind == "" {
		transportKind = config.MCPTransportHTTPStreamable
		mainBinding.TransportKind = transportKind
	}
	if transportKind == config.MCPTransportHTTPStreamable && mainBinding.ServerURL == nil {
		return clientOutputs{}, fmt.Errorf("mcpclient: main channel binding is required")
	}
	if transportKind == config.MCPTransportStdio && len(mainBinding.CommandArgs) == 0 {
		return clientOutputs{}, fmt.Errorf("mcpclient: main channel binding is required")
	}
	mcpTransport, err := p.TransportFactory.Build(*mainBinding)
	if err != nil {
		return clientOutputs{}, err
	}
	httpClient, err := p.TransportFactory.HTTPClientForBinding(*mainBinding)
	if err != nil {
		return clientOutputs{}, err
	}

	return clientOutputs{
		Client:     mcpClient,
		Transport:  mcpTransport,
		HTTPClient: httpClient,
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

	p.Lifecycle.Append(fx.Hook{
		OnStart: func(context.Context) error {
			logger.InfoContext(ctx, "Probing MCP server",
				slog.String("transport", string(transportKind)),
				slog.String("target", transportTargetLabel(transportKind, p.Config.ServerURL)),
			)
			go func() {
				probeCtx, probeCancel := context.WithTimeout(ctx, defaultProbeTimeout)
				defer probeCancel()
				sess, err := p.Client.Connect(probeCtx, p.Transport, nil)
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

func buildMcpHTTPTransport(logger *slog.Logger, loggingCfg *config.LoggingConfig, meterProvider *sdkmetric.MeterProvider, tlsBundle *tlsconfig.Bundle, proxyURL *url.URL) (http.RoundTripper, error) {
	// Order matters (outermost to innermost):
	//   1. Forwarding injects headers before anything else touches the request.
	//   2. Logging wraps otel instrumentation so raw dumps include forwarded headers.
	//   3. otelhttp instrumentation sits closest to the network to record final calls.
	base, err := tctransport.CloneDefaultWithBundle(tlsBundle)
	if err != nil {
		return nil, fmt.Errorf("mcpclient: %w", err)
	}
	base, err = tctransport.ApplyProxy(base, proxyURL)
	if err != nil {
		return nil, fmt.Errorf("mcpclient: %w", err)
	}
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
	return internal.NewForwardingRoundTripper(base), nil
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
