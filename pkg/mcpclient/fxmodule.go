package mcpclient

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"net/netip"
	"net/url"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/openai/tunnel-client/pkg/headerscope"
	tclog "github.com/openai/tunnel-client/pkg/log"
	tcmetrics "github.com/openai/tunnel-client/pkg/metrics"
	"github.com/openai/tunnel-client/pkg/runtimeconfig"
	"github.com/openai/tunnel-client/pkg/tlsconfig"
	tctransport "github.com/openai/tunnel-client/pkg/transport"
	"github.com/openai/tunnel-client/pkg/types"
	"github.com/openai/tunnel-client/pkg/version"
	"go.opentelemetry.io/contrib/instrumentation/net/http/otelhttp"
	sdkmetric "go.opentelemetry.io/otel/sdk/metric"
	"go.uber.org/fx"

	"github.com/openai/tunnel-client/pkg/mcpclient/internal"
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

const (
	defaultStartupProbeBackoffMin = 50 * time.Millisecond
	defaultStartupProbeBackoffMax = time.Second
)

type clientParams struct {
	fx.In

	Config           *runtimeconfig.MCPConfig
	Logging          *runtimeconfig.LoggingConfig
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

	Config     *runtimeconfig.MCPConfig
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
	if p.Config.AllowNoMain {
		// Explicit Harpoon-only clients have no main MCP transport to construct.
		// Keep the named HTTP client available for Fx consumers; OAuth remains
		// unreachable because no main channel is registered.
		return clientOutputs{Client: mcpClient, HTTPClient: &http.Client{}}, nil
	}
	if mainBinding == nil {
		legacyBinding := runtimeconfig.MCPChannelBinding{
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
		transportKind = runtimeconfig.MCPTransportHTTPStreamable
		mainBinding.TransportKind = transportKind
	}
	if transportKind == runtimeconfig.MCPTransportHTTPStreamable && mainBinding.ServerURL == nil {
		return clientOutputs{}, fmt.Errorf("mcpclient: main channel binding is required")
	}
	if transportKind == runtimeconfig.MCPTransportStdio && len(mainBinding.CommandArgs) == 0 {
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
	if p.Config.AllowNoMain {
		if p.ProbeState != nil {
			p.ProbeState.Set(nil)
		}
		if p.Logger != nil {
			p.Logger.Info("Skipping MCP probe: main channel is not configured")
		}
		return nil
	}
	transportKind := runtimeconfig.MCPTransportHTTPStreamable
	if p.Config.TransportKind != "" {
		transportKind = p.Config.TransportKind
	}
	if transportKind != runtimeconfig.MCPTransportHTTPStreamable {
		if p.ProbeState != nil {
			p.ProbeState.Set(nil)
		}
		if p.Logger != nil {
			p.Logger.Info("Skipping MCP probe for transport", slog.String("transport", string(transportKind)))
		}
		return nil
	}
	if transportKind == runtimeconfig.MCPTransportHTTPStreamable && p.Config.ServerURL == nil {
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
				connect := func(probeCtx context.Context) (probeSession, error) {
					return connectStartupProbe(probeCtx, func(probeCtx context.Context) (probeSession, error) {
						return p.Client.Connect(probeCtx, p.Transport, nil)
					})
				}
				if p.Config.StartupWaitTimeout > 0 {
					runStartupProbeWithRetry(
						ctx,
						startupProbeRetryOptions{
							waitTimeout:           p.Config.StartupWaitTimeout,
							probeTimeout:          defaultProbeTimeout,
							backoffMin:            defaultStartupProbeBackoffMin,
							backoffMax:            defaultStartupProbeBackoffMax,
							retryUnixSocketENOENT: p.Config.UnixSocketPath != "",
						},
						connect,
						logger,
						p.ProbeState,
					)
					return
				}
				runStartupProbe(ctx, defaultProbeTimeout, connect, logger, p.ProbeState)
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

func connectStartupProbe(ctx context.Context, connect func(context.Context) (probeSession, error)) (probeSession, error) {
	probeCtx := headerscope.WithMCPDiscovery(ctx)
	probeCtx, carrier, err := internal.ContextWithHeaders(probeCtx, nil)
	if err != nil {
		return nil, err
	}

	session, err := connect(probeCtx)
	if err == nil {
		return session, nil
	}
	statusCode, _ := carrier.ResponseStatusAndHeaders()
	if transportErr := carrier.TransportError(); statusCode == 0 && transportErr != nil {
		err = &probeTransportError{
			message: err.Error(),
			cause:   transportErr,
		}
	}
	// Failed connects never return a usable session. Do not propagate the
	// interface value: mcp.Client.Connect can return a typed nil
	// *mcp.ClientSession, which would otherwise look non-nil to late-result
	// cleanup and panic when Close is called.
	return nil, NewProbeHTTPStatusError(statusCode, err)
}

// probeTransportError retains the SDK's user-facing probe message while
// restoring the underlying transport error chain that the MCP SDK flattens.
type probeTransportError struct {
	message string
	cause   error
}

func (e *probeTransportError) Error() string {
	if e == nil {
		return ""
	}
	return e.message
}

func (e *probeTransportError) Unwrap() error {
	if e == nil {
		return nil
	}
	return e.cause
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

	resultCh := make(chan startupProbeResult)
	go func() {
		sess, err := connect(probeCtx)
		deliverStartupProbeResult(probeCtx, resultCh, startupProbeResult{session: sess, err: err})
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
		recordStartupProbeResult(ctx, logger, probeState, res)
	}
}

type startupProbeResult struct {
	session probeSession
	err     error
}

type startupProbeRetryWaiter func(context.Context, time.Duration) error

type startupProbeRetryOptions struct {
	waitTimeout           time.Duration
	probeTimeout          time.Duration
	backoffMin            time.Duration
	backoffMax            time.Duration
	retryUnixSocketENOENT bool
	wait                  startupProbeRetryWaiter
}

// runStartupProbeWithRetry keeps ProbeState pending while the opt-in listener
// wait sees retryable pre-connect failures. It never retries an HTTP response
// or an application-level MCP failure, so already-polled commands are never
// replayed by this startup path.
func runStartupProbeWithRetry(
	ctx context.Context,
	options startupProbeRetryOptions,
	connect func(context.Context) (probeSession, error),
	logger *slog.Logger,
	probeState *ProbeState,
) {
	if options.waitTimeout <= 0 {
		runStartupProbe(ctx, options.probeTimeout, connect, logger, probeState)
		return
	}
	if options.probeTimeout <= 0 {
		options.probeTimeout = defaultProbeTimeout
	}
	if options.backoffMin <= 0 {
		options.backoffMin = defaultStartupProbeBackoffMin
	}
	if options.backoffMax < options.backoffMin {
		options.backoffMax = options.backoffMin
	}
	if options.wait == nil {
		options.wait = waitForStartupProbeRetry
	}

	waitCtx, cancel := context.WithTimeout(ctx, options.waitTimeout)
	defer cancel()

	backoff := options.backoffMin
	attempt := 0
	var lastErr error
	for {
		if err := waitCtx.Err(); err != nil {
			if errors.Is(err, context.Canceled) {
				return
			}
			recordStartupWaitTimeout(ctx, logger, probeState, options.waitTimeout, lastErr)
			return
		}

		attempt++
		res, settled := runStartupProbeAttempt(waitCtx, options.probeTimeout, connect)
		if !settled {
			if errors.Is(waitCtx.Err(), context.Canceled) {
				return
			}
			recordStartupWaitTimeout(ctx, logger, probeState, options.waitTimeout, lastErr)
			return
		}
		if res.err == nil || !isRetryableStartupProbeError(res.err, options.retryUnixSocketENOENT) {
			recordStartupProbeResult(ctx, logger, probeState, res)
			return
		}

		lastErr = res.err
		if logger != nil {
			logger.WarnContext(ctx, "retrying MCP startup probe",
				slog.Int("attempt", attempt),
				slog.Duration("backoff", backoff),
				slog.String("error", tclog.ErrorForLog(res.err)),
			)
		}
		if err := options.wait(waitCtx, backoff); err != nil {
			if errors.Is(err, context.Canceled) || errors.Is(waitCtx.Err(), context.Canceled) {
				return
			}
			recordStartupWaitTimeout(ctx, logger, probeState, options.waitTimeout, lastErr)
			return
		}
		backoff = nextStartupProbeBackoff(backoff, options.backoffMax)
	}
}

func runStartupProbeAttempt(
	ctx context.Context,
	timeout time.Duration,
	connect func(context.Context) (probeSession, error),
) (startupProbeResult, bool) {
	probeCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	resultCh := make(chan startupProbeResult)
	go func() {
		sess, err := connect(probeCtx)
		deliverStartupProbeResult(probeCtx, resultCh, startupProbeResult{session: sess, err: err})
	}()

	select {
	case <-probeCtx.Done():
		if ctx.Err() != nil {
			return startupProbeResult{}, false
		}
		return startupProbeResult{err: NewProbeTimeoutError(timeout, probeCtx.Err())}, true
	case res := <-resultCh:
		return res, true
	}
}

// deliverStartupProbeResult transfers ownership of a completed probe session
// to the receiver. If the probe deadline wins first, no receiver remains, so
// close any late session instead of stranding it in a buffered channel.
func deliverStartupProbeResult(ctx context.Context, resultCh chan<- startupProbeResult, res startupProbeResult) {
	select {
	case resultCh <- res:
	case <-ctx.Done():
		if res.session != nil {
			_ = res.session.Close()
		}
	}
}

func waitForStartupProbeRetry(ctx context.Context, backoff time.Duration) error {
	timer := time.NewTimer(backoff)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}

func nextStartupProbeBackoff(current, maximum time.Duration) time.Duration {
	if current <= 0 {
		return defaultStartupProbeBackoffMin
	}
	if current >= maximum || current > maximum/2 {
		return maximum
	}
	return current * 2
}

func isRetryableStartupProbeError(err error, retryUnixSocketENOENT bool) bool {
	if err == nil {
		return false
	}
	var statusErr *ProbeHTTPStatusError
	if errors.As(err, &statusErr) && statusErr.StatusCode != 0 {
		return false
	}
	if errors.Is(err, syscall.ECONNREFUSED) {
		return true
	}
	return retryUnixSocketENOENT && errors.Is(err, syscall.ENOENT)
}

func recordStartupWaitTimeout(
	ctx context.Context,
	logger *slog.Logger,
	probeState *ProbeState,
	timeout time.Duration,
	lastErr error,
) {
	if lastErr == nil {
		lastErr = context.DeadlineExceeded
	}
	err := NewStartupWaitTimeoutError(timeout, lastErr)
	if probeState != nil {
		probeState.Set(err)
	}
	if logger != nil {
		logger.ErrorContext(ctx, "MCP startup wait timed out",
			slog.Duration("timeout", timeout),
			slog.String("error", tclog.ErrorForLog(err)),
		)
	}
}

func recordStartupProbeResult(
	ctx context.Context,
	logger *slog.Logger,
	probeState *ProbeState,
	res startupProbeResult,
) {
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

func buildMcpHTTPTransport(logger *slog.Logger, loggingCfg *runtimeconfig.LoggingConfig, meterProvider *sdkmetric.MeterProvider, tlsBundle *tlsconfig.Bundle, clientCertificate *tlsconfig.ClientCertificate, unixSocketPath string, proxyURL *url.URL, serverURL *url.URL, extraHeaders map[string]string, discoveryExtraHeaders map[string]string) (http.RoundTripper, error) {
	// Order matters (outermost to innermost):
	//   1. Static headers apply operator headers to the configured MCP origin.
	//   2. Forwarding injects per-request connector headers last so they win conflicts.
	//   3. Logging wraps otel instrumentation so raw dumps include final headers.
	//   4. otelhttp instrumentation and its route labeler sit close to the network to record final calls.
	extraHeaders, err := runtimeconfig.NormalizeExtraHeaders("MCP extra headers", extraHeaders)
	if err != nil {
		return nil, fmt.Errorf("mcpclient: %w", err)
	}
	discoveryExtraHeaders, err = runtimeconfig.NormalizeExtraHeaders("MCP discovery extra headers", discoveryExtraHeaders)
	if err != nil {
		return nil, fmt.Errorf("mcpclient: %w", err)
	}
	base, err := tctransport.CloneDefaultWithBundle(tlsBundle)
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
	if clientCertificate != nil {
		mtlsBase, err := tctransport.ApplyClientCertificate(base, clientCertificate)
		if err != nil {
			return nil, fmt.Errorf("mcpclient: %w", err)
		}
		base = &originScopedRoundTripper{
			serverURL:                serverURL,
			withClientCertificate:    mtlsBase,
			withoutClientCertificate: base,
		}
	}
	base = tcmetrics.WithHTTPClientMetricAttributes(base)
	base = otelhttp.NewTransport(
		base,
		otelhttp.WithMeterProvider(meterProvider),
	)
	forwardingLogger := logger.With(
		slog.String(tclog.FieldComponent, tclog.ComponentMcpClient),
		slog.String("transport", "forwarding_rt"),
	)
	base = tclog.NewRoundTripper(base, forwardingLogger, loggingCfg, tclog.ComponentMcpClient)
	base = internal.NewForwardingRoundTripper(base, serverURL)
	return internal.NewStaticHeadersRoundTripper(base, serverURL, extraHeaders, discoveryExtraHeaders), nil
}

// originScopedRoundTripper keeps an MCP client certificate scoped to the
// configured MCP origin. Redirects issue a new RoundTrip, so cross-origin
// redirect destinations use the transport without the client certificate.
type originScopedRoundTripper struct {
	serverURL                *url.URL
	withClientCertificate    http.RoundTripper
	withoutClientCertificate http.RoundTripper
}

func (t *originScopedRoundTripper) RoundTrip(req *http.Request) (*http.Response, error) {
	if req != nil && sameURLOrigin(req.URL, t.serverURL) {
		return t.withClientCertificate.RoundTrip(req)
	}
	return t.withoutClientCertificate.RoundTrip(req)
}

func sameURLOrigin(left *url.URL, right *url.URL) bool {
	if left == nil || right == nil {
		return false
	}
	leftHost := normalizedURLHostname(left)
	rightHost := normalizedURLHostname(right)
	if left.Scheme == "" || right.Scheme == "" || leftHost == "" || rightHost == "" {
		return false
	}
	return strings.EqualFold(left.Scheme, right.Scheme) &&
		leftHost == rightHost &&
		effectiveURLPort(left) == effectiveURLPort(right)
}

// normalizedURLHostname defines the hostname portion of the runtime MCP origin
// without consulting DNS. The redirect policy must compare the URL authority
// before dialing, so DNS resolution cannot widen the configured boundary.
func normalizedURLHostname(raw *url.URL) string {
	if raw == nil {
		return ""
	}
	// Preserve DNS trailing dots and IPv6 zone spelling: both can change where
	// the next request is dialed. DNS label case alone is origin-insensitive.
	host := strings.TrimSpace(raw.Hostname())
	if addr, err := netip.ParseAddr(host); err == nil {
		return addr.String()
	}
	return strings.ToLower(host)
}

// effectiveURLPort makes implicit HTTP(S) ports compare equal to their
// explicit spellings while keeping non-standard ports distinct.
func effectiveURLPort(raw *url.URL) string {
	if raw == nil {
		return ""
	}
	if port := raw.Port(); port != "" {
		if parsedPort, err := strconv.Atoi(port); err == nil {
			return strconv.Itoa(parsedPort)
		}
		return port
	}
	switch strings.ToLower(raw.Scheme) {
	case "http":
		return "80"
	case "https":
		return "443"
	default:
		return ""
	}
}

func transportTargetLabel(kind runtimeconfig.MCPTransportKind, serverURL *url.URL) string {
	if kind == runtimeconfig.MCPTransportHTTPStreamable && serverURL != nil {
		return serverURL.String()
	}
	if kind == "" {
		return string(runtimeconfig.MCPTransportHTTPStreamable)
	}
	return string(kind)
}
