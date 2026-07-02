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
	"go.openai.org/api/tunnel-client/pkg/headerscope"
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
		NewProbeState,
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

	Config     *config.MCPConfig
	Client     *mcp.Client
	Transport  mcp.Transport
	Lifecycle  fx.Lifecycle
	Logger     *slog.Logger
	ProbeState *ProbeState
}

type probeSession interface {
	Close() error
	InitializeResult() *mcp.InitializeResult
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
			Channel:        types.DefaultChannel,
			TransportKind:  p.Config.TransportKind,
			ServerURL:      p.Config.ServerURL,
			UnixSocketPath: p.Config.UnixSocketPath,
			Command:        p.Config.Command,
			CommandArgs:    p.Config.CommandArgs,
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
		if p.ProbeState != nil {
			p.ProbeState.Set(nil)
		}
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
				runStartupProbe(
					ctx,
					defaultProbeTimeout,
					func(probeCtx context.Context) (probeSession, error) {
						probeCtx = headerscope.WithMCPDiscovery(probeCtx)
						return p.Client.Connect(probeCtx, p.Transport, nil)
					},
					logger,
					p.ProbeState,
				)
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

func runStartupProbe(
	ctx context.Context,
	timeout time.Duration,
	connect func(context.Context) (probeSession, error),
	logger *slog.Logger,
	probeState *ProbeState,
) {
	probeCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	type result struct {
		session probeSession
		err     error
	}

	resultCh := make(chan result, 1)
	go func() {
		sess, err := connect(probeCtx)
		resultCh <- result{session: sess, err: err}
	}()

	select {
	case <-probeCtx.Done():
		err := NewProbeTimeoutError(timeout, probeCtx.Err())
		if probeState != nil {
			probeState.Set(err)
		}
		if logger != nil {
			logger.ErrorContext(ctx, "mcp probe timed out", slog.Duration("timeout", timeout), slog.String("error", err.Error()))
		}
	case res := <-resultCh:
		if res.err != nil {
			if probeState != nil {
				probeState.Set(res.err)
			}
			if logger != nil {
				logger.ErrorContext(ctx, "failed to connect to mcp", slog.String("error", res.err.Error()))
			}
			return
		}
		if probeState != nil {
			probeState.Set(nil)
		}
		if res.session == nil {
			if logger != nil {
				logger.WarnContext(ctx, "mcp probe returned nil session")
			}
			return
		}
		defer func() {
			if err := res.session.Close(); err != nil && logger != nil {
				logger.WarnContext(ctx, "failed to close mcp session", slog.String("error", err.Error()))
			}
		}()
		initRes := res.session.InitializeResult()
		logFields := []any{
			slog.String("protocol_version", initRes.ProtocolVersion),
		}
		if initRes.ServerInfo != nil {
			logFields = append(logFields, slog.String("server_name", initRes.ServerInfo.Name))
			if initRes.ServerInfo.Version != "" {
				logFields = append(logFields, slog.String("server_version", initRes.ServerInfo.Version))
			}
		}
		if logger != nil {
			logger.InfoContext(ctx, "mcp session initialized", logFields...)
		}
	}
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

func buildMcpHTTPTransport(logger *slog.Logger, loggingCfg *config.LoggingConfig, meterProvider *sdkmetric.MeterProvider, tlsBundle *tlsconfig.Bundle, clientCertificate *tlsconfig.ClientCertificate, unixSocketPath string, proxyURL *url.URL, serverURL *url.URL, extraHeaders map[string]string, discoveryExtraHeaders map[string]string) (http.RoundTripper, error) {
	// Order matters (outermost to innermost):
	//   1. Static headers apply operator headers to the configured MCP origin.
	//   2. Forwarding injects per-request connector headers last so they win conflicts.
	//   3. Logging wraps otel instrumentation so raw dumps include final headers.
	//   4. otelhttp instrumentation sits closest to the network to record final calls.
	base, err := tctransport.CloneDefaultWithBundle(tlsBundle)
	if err != nil {
		return nil, fmt.Errorf("mcpclient: %w", err)
	}
	base, err = tctransport.ApplyClientCertificate(base, clientCertificate)
	if err != nil {
		return nil, fmt.Errorf("mcpclient: %w", err)
	}
	base, err = tctransport.ApplyProxy(base, proxyURL)
	if err != nil {
		return nil, fmt.Errorf("mcpclient: %w", err)
	}
	base, err = tctransport.ApplyUnixSocketPath(base, unixSocketPath)
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
	base = internal.NewForwardingRoundTripper(base)
	return internal.NewStaticHeadersRoundTripper(base, serverURL, extraHeaders, discoveryExtraHeaders), nil
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
