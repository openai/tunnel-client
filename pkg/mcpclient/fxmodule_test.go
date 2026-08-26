package mcpclient

import (
	"bytes"
	"context"
	"errors"
	"log/slog"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/openai/tunnel-client/pkg/config"
	"github.com/openai/tunnel-client/pkg/headerscope"
	tclog "github.com/openai/tunnel-client/pkg/log"
	"github.com/openai/tunnel-client/pkg/mcpclient/internal"
	"github.com/openai/tunnel-client/pkg/types"
	sdkmetric "go.opentelemetry.io/otel/sdk/metric"
)

type fakeProbeSession struct {
	initResult mcp.InitializeResult
	closed     bool
}

func (s *fakeProbeSession) Close() error {
	s.closed = true
	return nil
}

func (s *fakeProbeSession) InitializeResult() *mcp.InitializeResult {
	return &s.initResult
}

type closeSignalingProbeSession struct {
	initResult mcp.InitializeResult
	closed     chan struct{}
	closeOnce  sync.Once
}

func newCloseSignalingProbeSession() *closeSignalingProbeSession {
	return &closeSignalingProbeSession{
		closed: make(chan struct{}),
	}
}

func (s *closeSignalingProbeSession) Close() error {
	s.closeOnce.Do(func() {
		close(s.closed)
	})
	return nil
}

func (s *closeSignalingProbeSession) InitializeResult() *mcp.InitializeResult {
	return &s.initResult
}

func TestNewMcpClient_DefaultTransport(t *testing.T) {
	params := clientParams{
		Config: &config.MCPConfig{
			ServerURL:             mustParseURL(t, "https://example.invalid"),
			MaxConcurrentRequests: 10,
			ChannelBindings: []config.MCPChannelBinding{
				{
					Channel:       types.DefaultChannel,
					TransportKind: config.MCPTransportHTTPStreamable,
					ServerURL:     mustParseURL(t, "https://example.invalid"),
				},
			},
		},
		Logging: &config.LoggingConfig{
			HTTPRawUnsafe: false,
		},
		Logger:           slog.New(slog.NewTextHandler(&bytes.Buffer{}, nil)),
		MeterProvider:    sdkmetric.NewMeterProvider(),
		TransportFactory: newTestChannelTransportFactory(t, mustParseURL(t, "https://example.invalid"), &config.LoggingConfig{HTTPRawUnsafe: false}, slog.New(slog.NewTextHandler(&bytes.Buffer{}, nil))),
	}
	outputs, err := newMcpClient(params)
	if err != nil {
		t.Fatalf("newMcpClient returned error: %v", err)
	}

	if outputs.Client == nil {
		t.Fatalf("expected client to be non-nil")
	}

	if _, ok := outputs.Transport.(*mcp.StreamableClientTransport); !ok {
		t.Fatalf("expected raw transport to be *mcp.StreamableClientTransport; got %T", outputs.Transport)
	}
}

func TestNewMcpClientLegacyUnixSocket(t *testing.T) {
	socketFile, err := os.CreateTemp("/tmp", "mcp-client-legacy-*.sock")
	if err != nil {
		t.Fatalf("create unix socket temp file: %v", err)
	}
	socketPath := socketFile.Name()
	_ = socketFile.Close()
	_ = os.Remove(socketPath)
	listener, err := net.Listen("unix", socketPath)
	if err != nil {
		t.Fatalf("listen unix: %v", err)
	}
	t.Cleanup(func() { _ = os.Remove(socketPath) })
	server := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusNoContent)
	}))
	server.Listener = listener
	server.Start()
	t.Cleanup(server.Close)

	serverURL := mustParseURL(t, "http://localhost/mcp")
	logging := &config.LoggingConfig{}
	logger := slog.New(slog.NewTextHandler(&bytes.Buffer{}, nil))
	params := clientParams{
		Config: &config.MCPConfig{
			ServerURL:      serverURL,
			UnixSocketPath: socketPath,
		},
		Logging:          logging,
		Logger:           logger,
		MeterProvider:    sdkmetric.NewMeterProvider(),
		TransportFactory: newTestChannelTransportFactory(t, serverURL, logging, logger),
	}
	outputs, err := newMcpClient(params)
	if err != nil {
		t.Fatalf("newMcpClient: %v", err)
	}
	resp, err := outputs.HTTPClient.Get(serverURL.String())
	if err != nil {
		t.Fatalf("legacy unix socket request: %v", err)
	}
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusNoContent {
		t.Fatalf("unexpected status %d", resp.StatusCode)
	}
}

func TestNewMcpClientSkipsConfiguredMainWhenDisabled(t *testing.T) {
	serverURL := mustParseURL(t, "https://example.invalid")
	logging := &config.LoggingConfig{}
	logger := slog.New(slog.NewTextHandler(&bytes.Buffer{}, nil))
	params := clientParams{
		Config: &config.MCPConfig{
			AllowNoMain: true,
			ChannelBindings: []config.MCPChannelBinding{{
				Channel:       types.DefaultChannel,
				TransportKind: config.MCPTransportHTTPStreamable,
				ServerURL:     serverURL,
			}},
		},
		Logging:          logging,
		Logger:           logger,
		MeterProvider:    sdkmetric.NewMeterProvider(),
		TransportFactory: newTestChannelTransportFactory(t, serverURL, logging, logger),
	}
	outputs, err := newMcpClient(params)
	if err != nil {
		t.Fatalf("newMcpClient: %v", err)
	}
	if outputs.Transport != nil {
		t.Fatalf("disabled main created transport %T", outputs.Transport)
	}
}

func TestNewMcpClient_LoggingTransport(t *testing.T) {
	buf := &bytes.Buffer{}
	logger := slog.New(slog.NewTextHandler(buf, &slog.HandlerOptions{Level: slog.LevelDebug}))

	params := clientParams{
		Config: &config.MCPConfig{
			ServerURL:             mustParseURL(t, "https://example.invalid"),
			MaxConcurrentRequests: 10,
			ChannelBindings: []config.MCPChannelBinding{
				{
					Channel:       types.DefaultChannel,
					TransportKind: config.MCPTransportHTTPStreamable,
					ServerURL:     mustParseURL(t, "https://example.invalid"),
				},
			},
		},
		Logging: &config.LoggingConfig{
			HTTPRawUnsafe: true,
			Level:         slog.LevelDebug,
		},
		Logger:           logger,
		MeterProvider:    sdkmetric.NewMeterProvider(),
		TransportFactory: newTestChannelTransportFactory(t, mustParseURL(t, "https://example.invalid"), &config.LoggingConfig{HTTPRawUnsafe: true, Level: slog.LevelDebug}, logger),
	}
	outputs, err := newMcpClient(params)
	if err != nil {
		t.Fatalf("newMcpClient returned error: %v", err)
	}

	loggingTransport, ok := outputs.Transport.(*mcp.LoggingTransport)
	if !ok {
		t.Fatalf("expected raw transport to be logging transport; got %T", outputs.Transport)
	}

	if _, ok := loggingTransport.Transport.(*mcp.StreamableClientTransport); !ok {
		t.Fatalf("expected underlying transport to be *mcp.StreamableClientTransport; got %T", loggingTransport.Transport)
	}

	writer, ok := loggingTransport.Writer.(slogWriter)
	if !ok {
		t.Fatalf("expected writer to be slogWriter; got %T", loggingTransport.Writer)
	}

	if writer.logger == nil {
		t.Fatalf("expected writer logger to be configured")
	}

	if _, err := loggingTransport.Writer.Write([]byte("read: {}")); err != nil {
		t.Fatalf("unexpected error writing log: %v", err)
	}

	output := buf.String()
	if !strings.Contains(output, "read: {}") {
		t.Fatalf("expected log output to contain message; got %q", output)
	}
	if !strings.Contains(output, tclog.FieldComponent+"="+tclog.ComponentMcpClient) {
		t.Fatalf("expected log output to contain component field; got %q", output)
	}
	if !strings.Contains(output, "transport=raw_http") {
		t.Fatalf("expected log output to include transport marker; got %q", output)
	}
}

func TestNewMcpClient_LoggingTransportRequiresDebugLevel(t *testing.T) {
	params := clientParams{
		Config: &config.MCPConfig{
			ServerURL:             mustParseURL(t, "https://example.invalid"),
			MaxConcurrentRequests: 10,
			ChannelBindings: []config.MCPChannelBinding{
				{
					Channel:       types.DefaultChannel,
					TransportKind: config.MCPTransportHTTPStreamable,
					ServerURL:     mustParseURL(t, "https://example.invalid"),
				},
			},
		},
		Logging: &config.LoggingConfig{
			HTTPRawUnsafe: true,
			Level:         slog.LevelInfo,
		},
		Logger:           slog.New(slog.NewTextHandler(&bytes.Buffer{}, nil)),
		MeterProvider:    sdkmetric.NewMeterProvider(),
		TransportFactory: newTestChannelTransportFactory(t, mustParseURL(t, "https://example.invalid"), &config.LoggingConfig{HTTPRawUnsafe: true, Level: slog.LevelInfo}, slog.New(slog.NewTextHandler(&bytes.Buffer{}, nil))),
	}
	outputs, err := newMcpClient(params)
	if err != nil {
		t.Fatalf("newMcpClient returned error: %v", err)
	}

	if _, ok := outputs.Transport.(*mcp.StreamableClientTransport); !ok {
		t.Fatalf("expected raw transport to be streamable; got %T", outputs.Transport)
	}
}

func TestBuildMcpHTTPTransportRejectsInvalidExtraHeaders(t *testing.T) {
	t.Parallel()

	for _, tc := range []struct {
		name             string
		extraHeaders     map[string]string
		discoveryHeaders map[string]string
		wantErr          string
	}{
		{
			name: "runtime conflict",
			extraHeaders: map[string]string{
				"X-Proxy-Auth": "first",
				"x-proxy-auth": "second",
			},
			wantErr: "conflicting values for case-insensitive HTTP header",
		},
		{
			name: "discovery conflict",
			discoveryHeaders: map[string]string{
				"X-Proxy-Auth": "first",
				"x-proxy-auth": "second",
			},
			wantErr: "conflicting values for case-insensitive HTTP header",
		},
		{name: "runtime invalid name", extraHeaders: map[string]string{"Bad Header": "value"}, wantErr: "invalid HTTP header name"},
		{name: "discovery invalid value", discoveryHeaders: map[string]string{"X-Test": "bad\x00value"}, wantErr: "invalid HTTP header value"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			_, err := buildMcpHTTPTransport(
				slog.New(slog.NewTextHandler(&bytes.Buffer{}, nil)),
				&config.LoggingConfig{},
				sdkmetric.NewMeterProvider(),
				nil,
				nil,
				"",
				nil,
				mustParseURL(t, "https://example.invalid/mcp"),
				tc.extraHeaders,
				tc.discoveryHeaders,
			)
			if err == nil || !strings.Contains(err.Error(), tc.wantErr) {
				t.Fatalf("expected %q error, got %v", tc.wantErr, err)
			}
		})
	}
}

func TestChannelHTTPClientScopesStaticAndForwardedAuthorizationHeaders(t *testing.T) {
	t.Parallel()

	seen := make(chan http.Header, 3)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		seen <- r.Header.Clone()
		w.WriteHeader(http.StatusNoContent)
	}))
	t.Cleanup(server.Close)

	serverURL := mustParseURL(t, server.URL+"/mcp")
	factory := newTestChannelTransportFactory(t, serverURL, &config.LoggingConfig{HTTPRawUnsafe: false}, slog.New(slog.NewTextHandler(&bytes.Buffer{}, nil)))
	factory.config.ExtraHeaders = map[string]string{
		"Authorization": "Bearer static-runtime",
		"X-Static":      "runtime",
		"X-Discovery":   "runtime",
	}
	factory.config.DiscoveryExtraHeaders = map[string]string{
		"X-Discovery": "discovery",
	}

	binding := config.MCPChannelBinding{
		Channel:       types.DefaultChannel,
		TransportKind: config.MCPTransportHTTPStreamable,
		ServerURL:     serverURL,
	}
	client, err := factory.HTTPClientForBinding(binding)
	if err != nil {
		t.Fatalf("HTTPClientForBinding returned error: %v", err)
	}

	runtimeCtx, _, err := internal.ContextWithHeaders(context.Background(), http.Header{
		"Authorization": {"Bearer connector-request"},
		"X-Connector":   {"forwarded"},
	})
	if err != nil {
		t.Fatalf("ContextWithHeaders returned error: %v", err)
	}
	mustDoRequest(t, client, http.MethodPost, server.URL+"/mcp", runtimeCtx)
	runtimeHeaders := mustReceiveHeaders(t, seen)
	if got := runtimeHeaders.Get("Authorization"); got != "Bearer connector-request" {
		t.Fatalf("runtime Authorization = %q, want connector request value", got)
	}
	if got := runtimeHeaders.Get("X-Static"); got != "runtime" {
		t.Fatalf("runtime X-Static = %q, want runtime", got)
	}
	if got := runtimeHeaders.Get("X-Connector"); got != "forwarded" {
		t.Fatalf("runtime X-Connector = %q, want forwarded", got)
	}
	if got := runtimeHeaders.Get("X-Discovery"); got != "runtime" {
		t.Fatalf("runtime X-Discovery = %q, want runtime", got)
	}

	discoveryCtx := headerscope.WithMCPDiscovery(context.Background())
	mustDoRequest(t, client, http.MethodGet, server.URL+"/.well-known/oauth-protected-resource/mcp", discoveryCtx)
	discoveryHeaders := mustReceiveHeaders(t, seen)
	if got := discoveryHeaders.Get("Authorization"); got != "Bearer static-runtime" {
		t.Fatalf("discovery Authorization = %q, want static runtime value", got)
	}
	if got := discoveryHeaders.Get("X-Discovery"); got != "discovery" {
		t.Fatalf("discovery X-Discovery = %q, want discovery", got)
	}

	mustDoRequest(t, client, http.MethodGet, server.URL+"/unrelated", context.Background())
	unrelatedHeaders := mustReceiveHeaders(t, seen)
	if got := unrelatedHeaders.Get("Authorization"); got != "" {
		t.Fatalf("unrelated Authorization = %q, want empty", got)
	}
	if got := unrelatedHeaders.Get("X-Static"); got != "" {
		t.Fatalf("unrelated X-Static = %q, want empty", got)
	}
}

func TestOriginScopedRoundTripperScopesClientCertificateAcrossOAuthRequests(t *testing.T) {
	t.Parallel()

	var withCertificateCalls []string
	var withoutCertificateCalls []string
	rt := &originScopedRoundTripper{
		serverURL: mustParseURL(t, "https://mcp.example.com/mcp"),
		withClientCertificate: testRoundTripperFunc(func(req *http.Request) (*http.Response, error) {
			withCertificateCalls = append(withCertificateCalls, req.URL.String())
			statusCode := http.StatusNoContent
			headers := make(http.Header)
			if req.URL.Path == "/redirect" {
				statusCode = http.StatusFound
				headers.Set("Location", "https://auth.example.com/redirected")
			}
			return &http.Response{
				StatusCode: statusCode,
				Header:     headers,
				Body:       http.NoBody,
				Request:    req,
			}, nil
		}),
		withoutClientCertificate: testRoundTripperFunc(func(req *http.Request) (*http.Response, error) {
			withoutCertificateCalls = append(withoutCertificateCalls, req.URL.String())
			return &http.Response{
				StatusCode: http.StatusNoContent,
				Header:     make(http.Header),
				Body:       http.NoBody,
				Request:    req,
			}, nil
		}),
	}

	client := &http.Client{Transport: rt}
	for _, rawURL := range []string{
		"https://mcp.example.com/.well-known/oauth-protected-resource/mcp",
		"https://auth.example.com/.well-known/oauth-authorization-server",
		"https://mcp.example.com/redirect",
	} {
		resp, err := client.Get(rawURL)
		if err != nil {
			t.Fatalf("GET %q failed: %v", rawURL, err)
		}
		_ = resp.Body.Close()
	}

	if got, want := strings.Join(withCertificateCalls, ","), "https://mcp.example.com/.well-known/oauth-protected-resource/mcp,https://mcp.example.com/redirect"; got != want {
		t.Fatalf("with-client-certificate calls = %q, want %q", got, want)
	}
	if got, want := strings.Join(withoutCertificateCalls, ","), "https://auth.example.com/.well-known/oauth-authorization-server,https://auth.example.com/redirected"; got != want {
		t.Fatalf("without-client-certificate calls = %q, want %q", got, want)
	}
}

func TestRunStartupProbeMarksSuccess(t *testing.T) {
	t.Parallel()

	state := NewProbeState()
	session := &fakeProbeSession{
		initResult: mcp.InitializeResult{
			ProtocolVersion: "2025-03-26",
			ServerInfo:      &mcp.Implementation{Name: "fixture", Version: "1.0.0"},
		},
	}

	runStartupProbe(
		context.Background(),
		50*time.Millisecond,
		func(context.Context) (probeSession, error) {
			return session, nil
		},
		slog.New(slog.NewTextHandler(&bytes.Buffer{}, nil)),
		state,
	)

	_, err, ok := state.Wait(time.Second)
	if !ok {
		t.Fatalf("expected probe state to complete")
	}
	if err != nil {
		t.Fatalf("expected nil probe error, got %v", err)
	}
	if !session.closed {
		t.Fatalf("expected probe session to be closed")
	}
}

func TestRunStartupProbeMarksFailure(t *testing.T) {
	t.Parallel()

	state := NewProbeState()

	runStartupProbe(
		context.Background(),
		50*time.Millisecond,
		func(context.Context) (probeSession, error) {
			return nil, errors.New("boom")
		},
		slog.New(slog.NewTextHandler(&bytes.Buffer{}, nil)),
		state,
	)

	_, err, ok := state.Wait(time.Second)
	if !ok {
		t.Fatalf("expected probe state to complete")
	}
	if err == nil || err.Error() != "boom" {
		t.Fatalf("expected probe error boom, got %v", err)
	}
}

func TestConnectStartupProbeCarriesHTTPStatus(t *testing.T) {
	t.Parallel()

	const probeMessage = "received:401, unathenticated"
	session, err := connectStartupProbe(
		context.Background(),
		func(ctx context.Context) (probeSession, error) {
			if !headerscope.IsMCPDiscovery(ctx) {
				t.Fatal("expected probe context to be marked for discovery")
			}
			carrier := internal.CarrierFromContext(ctx)
			if carrier == nil {
				t.Fatal("expected probe context to carry HTTP response metadata")
			}
			carrier.StoreResponse(http.StatusUnauthorized, nil)
			return nil, errors.New(probeMessage)
		},
	)

	if session != nil {
		t.Fatalf("expected nil probe session, got %T", session)
	}
	if err == nil || err.Error() != probeMessage {
		t.Fatalf("expected probe error %q, got %v", probeMessage, err)
	}
	if !IsAuthRequiredProbeError(err) {
		t.Fatalf("expected HTTP %d probe error to require auth", http.StatusUnauthorized)
	}
}

func TestConnectStartupProbePreservesCapturedTransportError(t *testing.T) {
	t.Parallel()

	transportErr := &net.OpError{Op: "dial", Net: "tcp", Err: syscall.ECONNREFUSED}
	_, err := connectStartupProbe(
		context.Background(),
		func(ctx context.Context) (probeSession, error) {
			req, reqErr := http.NewRequestWithContext(ctx, http.MethodPost, "http://127.0.0.1:9090/mcp", nil)
			if reqErr != nil {
				t.Fatalf("new request: %v", reqErr)
			}
			rt := internal.NewForwardingRoundTripper(testRoundTripperFunc(func(*http.Request) (*http.Response, error) {
				return nil, transportErr
			}), req.URL)
			if _, roundTripErr := rt.RoundTrip(req); !errors.Is(roundTripErr, transportErr) {
				t.Fatalf("round trip error = %v, want %v", roundTripErr, transportErr)
			}
			// The MCP SDK currently flattens the transport error with %%v while
			// formatting its initialize failure. connectStartupProbe must restore
			// the captured transport cause for exact retry classification.
			return nil, errors.New("calling initialize: rejected: sending initialize: dial tcp: connection refused")
		},
	)
	if !errors.Is(err, syscall.ECONNREFUSED) {
		t.Fatalf("probe error did not preserve ECONNREFUSED: %v", err)
	}
	if !isRetryableStartupProbeError(err, false) {
		t.Fatalf("probe error should be retryable after captured transport cause: %v", err)
	}
}

func TestRunStartupProbeMarksFailureWhenConnectHangs(t *testing.T) {
	t.Parallel()

	state := NewProbeState()
	release := make(chan struct{})
	session := newCloseSignalingProbeSession()

	runStartupProbe(
		context.Background(),
		20*time.Millisecond,
		func(context.Context) (probeSession, error) {
			<-release
			return session, nil
		},
		slog.New(slog.NewTextHandler(&bytes.Buffer{}, nil)),
		state,
	)
	close(release)

	select {
	case <-session.closed:
	case <-time.After(time.Second):
		t.Fatal("expected late startup probe session to close after timeout")
	}

	_, err, ok := state.Wait(time.Second)
	if !ok {
		t.Fatalf("expected probe state to complete")
	}
	if err == nil || !strings.Contains(err.Error(), "mcp probe timed out after") {
		t.Fatalf("expected startup probe timeout, got %v", err)
	}
}

func TestRunStartupProbeAttemptClosesLateSessionAfterDeadline(t *testing.T) {
	t.Parallel()

	release := make(chan struct{})
	var releaseOnce sync.Once
	releaseConnect := func() {
		releaseOnce.Do(func() {
			close(release)
		})
	}
	t.Cleanup(releaseConnect)
	started := make(chan struct{})
	session := newCloseSignalingProbeSession()
	resultCh := make(chan startupProbeResult, 1)
	settledCh := make(chan bool, 1)

	go func() {
		res, settled := runStartupProbeAttempt(
			context.Background(),
			20*time.Millisecond,
			func(context.Context) (probeSession, error) {
				close(started)
				<-release
				return session, nil
			},
		)
		resultCh <- res
		settledCh <- settled
	}()

	<-started
	var res startupProbeResult
	select {
	case res = <-resultCh:
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for startup probe attempt deadline")
	}
	if settled := <-settledCh; !settled {
		t.Fatal("expected probe attempt deadline to settle")
	}
	if !IsTimeoutProbeError(res.err) {
		t.Fatalf("expected startup probe timeout, got %v", res.err)
	}

	releaseConnect()
	select {
	case <-session.closed:
	case <-time.After(time.Second):
		t.Fatal("expected late retry probe session to close after deadline")
	}
}

func TestDeliverStartupProbeResultClosesSessionAfterCancellation(t *testing.T) {
	t.Parallel()

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	session := newCloseSignalingProbeSession()

	deliverStartupProbeResult(
		ctx,
		make(chan startupProbeResult),
		startupProbeResult{session: session},
	)

	select {
	case <-session.closed:
	case <-time.After(time.Second):
		t.Fatal("expected canceled probe result delivery to close session")
	}
}

func TestDeliverStartupProbeResultDoesNotCloseTypedNilFailedSession(t *testing.T) {
	t.Parallel()

	var sdkSession *mcp.ClientSession
	session, err := connectStartupProbe(
		context.Background(),
		func(context.Context) (probeSession, error) {
			return sdkSession, errors.New("boom")
		},
	)
	if err == nil {
		t.Fatal("expected failed startup probe")
	}

	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	// Before failed connects normalized their session result to nil, the
	// typed-nil SDK session above reached this late-result cleanup path and
	// panicked in (*mcp.ClientSession).Close.
	deliverStartupProbeResult(
		ctx,
		make(chan startupProbeResult),
		startupProbeResult{session: session, err: err},
	)
}

func TestRunStartupProbeWithRetryKeepsStatePendingUntilListenerReachable(t *testing.T) {
	t.Parallel()

	state := NewProbeState()
	retryObserved := make(chan struct{})
	releaseRetry := make(chan struct{})
	done := make(chan struct{})
	session := &fakeProbeSession{initResult: mcp.InitializeResult{ProtocolVersion: "2025-03-26"}}
	var attempts int
	var logs bytes.Buffer

	go func() {
		defer close(done)
		runStartupProbeWithRetry(
			context.Background(),
			startupProbeRetryOptions{
				waitTimeout:  time.Second,
				probeTimeout: time.Second,
				backoffMin:   time.Millisecond,
				backoffMax:   time.Millisecond,
				wait: func(ctx context.Context, _ time.Duration) error {
					close(retryObserved)
					select {
					case <-ctx.Done():
						return ctx.Err()
					case <-releaseRetry:
						return nil
					}
				},
			},
			func(context.Context) (probeSession, error) {
				attempts++
				if attempts == 1 {
					return nil, &net.OpError{Op: "dial", Net: "tcp", Err: syscall.ECONNREFUSED}
				}
				return session, nil
			},
			slog.New(slog.NewTextHandler(&logs, nil)),
			state,
		)
	}()

	<-retryObserved
	if state.IsDone() {
		t.Fatal("probe state completed before listener became reachable")
	}
	close(releaseRetry)
	<-done

	_, err, ok := state.Wait(time.Second)
	if !ok {
		t.Fatal("expected probe state to complete")
	}
	if err != nil {
		t.Fatalf("expected successful probe after retry, got %v", err)
	}
	if attempts != 2 {
		t.Fatalf("attempt count = %d, want 2", attempts)
	}
	if !session.closed {
		t.Fatal("expected successful retry session to be closed")
	}
	if !strings.Contains(logs.String(), "retrying MCP startup probe") {
		t.Fatalf("expected retry log, got %q", logs.String())
	}
}

func TestRunStartupProbeWithRetryDoesNotRetryHTTPResponse(t *testing.T) {
	t.Parallel()

	state := NewProbeState()
	var attempts int
	runStartupProbeWithRetry(
		context.Background(),
		startupProbeRetryOptions{
			waitTimeout:  time.Second,
			probeTimeout: time.Second,
			wait: func(context.Context, time.Duration) error {
				t.Fatal("HTTP response must not retry")
				return nil
			},
		},
		func(context.Context) (probeSession, error) {
			attempts++
			return nil, NewProbeHTTPStatusError(http.StatusUnauthorized, syscall.ECONNREFUSED)
		},
		slog.New(slog.NewTextHandler(&bytes.Buffer{}, nil)),
		state,
	)

	_, err, ok := state.Wait(time.Second)
	if !ok {
		t.Fatal("expected probe state to complete")
	}
	if !IsAuthRequiredProbeError(err) {
		t.Fatalf("expected auth-required probe error, got %v", err)
	}
	if attempts != 1 {
		t.Fatalf("attempt count = %d, want 1", attempts)
	}
}

func TestRunStartupProbeWithRetryTimeoutRemainsReadinessFailure(t *testing.T) {
	t.Parallel()

	state := NewProbeState()
	runStartupProbeWithRetry(
		context.Background(),
		startupProbeRetryOptions{
			waitTimeout:  time.Second,
			probeTimeout: time.Second,
			wait: func(context.Context, time.Duration) error {
				return context.DeadlineExceeded
			},
		},
		func(context.Context) (probeSession, error) {
			return nil, &net.OpError{Op: "dial", Net: "tcp", Err: syscall.ECONNREFUSED}
		},
		slog.New(slog.NewTextHandler(&bytes.Buffer{}, nil)),
		state,
	)

	_, err, ok := state.Wait(time.Second)
	if !ok {
		t.Fatal("expected probe state to complete")
	}
	if !IsStartupWaitTimeoutError(err) {
		t.Fatalf("expected startup wait timeout error, got %v", err)
	}
	if IsTimeoutProbeError(err) {
		t.Fatalf("startup wait timeout must not be classified as legacy probe timeout: %v", err)
	}
}

func TestRunStartupProbeWithRetryStopsOnCancellation(t *testing.T) {
	t.Parallel()

	state := NewProbeState()
	ctx, cancel := context.WithCancel(context.Background())
	retryObserved := make(chan struct{})
	done := make(chan struct{})

	go func() {
		defer close(done)
		runStartupProbeWithRetry(
			ctx,
			startupProbeRetryOptions{
				waitTimeout:  time.Second,
				probeTimeout: time.Second,
				wait: func(ctx context.Context, _ time.Duration) error {
					close(retryObserved)
					<-ctx.Done()
					return ctx.Err()
				},
			},
			func(context.Context) (probeSession, error) {
				return nil, &net.OpError{Op: "dial", Net: "tcp", Err: syscall.ECONNREFUSED}
			},
			slog.New(slog.NewTextHandler(&bytes.Buffer{}, nil)),
			state,
		)
	}()

	<-retryObserved
	cancel()
	<-done
	if state.IsDone() {
		t.Fatal("canceled startup retry must not publish a probe result")
	}
}

func TestIsRetryableStartupProbeError(t *testing.T) {
	t.Parallel()

	testCases := []struct {
		name            string
		err             error
		retryUnixENOENT bool
		want            bool
	}{
		{
			name: "tcp connection refused",
			err:  &net.OpError{Op: "dial", Net: "tcp", Err: syscall.ECONNREFUSED},
			want: true,
		},
		{
			name:            "unix socket missing",
			err:             &net.OpError{Op: "dial", Net: "unix", Err: syscall.ENOENT},
			retryUnixENOENT: true,
			want:            true,
		},
		{
			name: "missing path without unix binding",
			err:  &net.OpError{Op: "dial", Net: "unix", Err: syscall.ENOENT},
			want: false,
		},
		{
			name: "http response wins over wrapped refusal",
			err:  NewProbeHTTPStatusError(http.StatusServiceUnavailable, syscall.ECONNREFUSED),
			want: false,
		},
		{
			name: "non-retryable failure",
			err:  errors.New("tls handshake failed"),
			want: false,
		},
	}

	for _, testCase := range testCases {
		testCase := testCase
		t.Run(testCase.name, func(t *testing.T) {
			t.Parallel()
			if got := isRetryableStartupProbeError(testCase.err, testCase.retryUnixENOENT); got != testCase.want {
				t.Fatalf("isRetryableStartupProbeError() = %t, want %t", got, testCase.want)
			}
		})
	}
}

func newTestChannelTransportFactory(t *testing.T, serverURL *url.URL, logging *config.LoggingConfig, logger *slog.Logger) *ChannelTransportFactory {
	t.Helper()
	cfg := &config.MCPConfig{
		ServerURL: serverURL,
		ChannelBindings: []config.MCPChannelBinding{
			{
				Channel:       types.DefaultChannel,
				TransportKind: config.MCPTransportHTTPStreamable,
				ServerURL:     serverURL,
			},
		},
	}
	factory, err := newChannelTransportFactory(channelTransportFactoryParams{
		Config:        cfg,
		Logging:       logging,
		Logger:        logger,
		MeterProvider: sdkmetric.NewMeterProvider(),
		TransportProviders: []TransportProvider{
			newStreamableTransportProvider(),
		},
		TLSBundle: nil,
	})
	if err != nil {
		t.Fatalf("newChannelTransportFactory returned error: %v", err)
	}
	return factory
}

func mustDoRequest(t *testing.T, client *http.Client, method string, rawURL string, ctx context.Context) {
	t.Helper()
	req, err := http.NewRequestWithContext(ctx, method, rawURL, nil)
	if err != nil {
		t.Fatalf("NewRequestWithContext(%q): %v", rawURL, err)
	}
	resp, err := client.Do(req)
	if err != nil {
		t.Fatalf("client.Do(%q): %v", rawURL, err)
	}
	defer func() {
		if err := resp.Body.Close(); err != nil {
			t.Fatalf("closing response body: %v", err)
		}
	}()
	if resp.StatusCode != http.StatusNoContent {
		t.Fatalf("client.Do(%q) status = %d, want %d", rawURL, resp.StatusCode, http.StatusNoContent)
	}
}

func mustReceiveHeaders(t *testing.T, ch <-chan http.Header) http.Header {
	t.Helper()
	select {
	case headers := <-ch:
		return headers
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for test server request")
		return nil
	}
}

type testRoundTripperFunc func(*http.Request) (*http.Response, error)

func (f testRoundTripperFunc) RoundTrip(req *http.Request) (*http.Response, error) {
	return f(req)
}

func mustParseURL(t *testing.T, raw string) *url.URL {
	t.Helper()
	parsed, err := url.Parse(raw)
	if err != nil {
		t.Fatalf("parse URL %q: %v", raw, err)
	}
	return parsed
}
