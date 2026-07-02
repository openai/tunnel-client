package mcpclient

import (
	"bytes"
	"context"
	"errors"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"go.openai.org/api/tunnel-client/pkg/config"
	"go.openai.org/api/tunnel-client/pkg/headerscope"
	tclog "go.openai.org/api/tunnel-client/pkg/log"
	"go.openai.org/api/tunnel-client/pkg/mcpclient/internal"
	"go.openai.org/api/tunnel-client/pkg/types"
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

func TestRunStartupProbeMarksFailureWhenConnectHangs(t *testing.T) {
	t.Parallel()

	state := NewProbeState()
	release := make(chan struct{})

	runStartupProbe(
		context.Background(),
		20*time.Millisecond,
		func(context.Context) (probeSession, error) {
			<-release
			return nil, nil
		},
		slog.New(slog.NewTextHandler(&bytes.Buffer{}, nil)),
		state,
	)
	close(release)

	_, err, ok := state.Wait(time.Second)
	if !ok {
		t.Fatalf("expected probe state to complete")
	}
	if err == nil || !strings.Contains(err.Error(), "mcp probe timed out after") {
		t.Fatalf("expected startup probe timeout, got %v", err)
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

func mustParseURL(t *testing.T, raw string) *url.URL {
	t.Helper()
	parsed, err := url.Parse(raw)
	if err != nil {
		t.Fatalf("parse URL %q: %v", raw, err)
	}
	return parsed
}
