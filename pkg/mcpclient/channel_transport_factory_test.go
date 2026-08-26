package mcpclient

import (
	"context"
	"crypto/rand"
	"crypto/rsa"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"errors"
	"io"
	"log/slog"
	"math/big"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	sdkmetric "go.opentelemetry.io/otel/sdk/metric"

	"github.com/openai/tunnel-client/pkg/config"
	"github.com/openai/tunnel-client/pkg/mcpclient/internal"
	"github.com/openai/tunnel-client/pkg/tlsconfig"
	"github.com/openai/tunnel-client/pkg/types"
)

func TestChannelTransportFactoryAppliesProxy(t *testing.T) {
	t.Parallel()

	targetCalled := make(chan struct{}, 1)
	targetServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		targetCalled <- struct{}{}
		http.Error(w, "unexpected direct request", http.StatusBadGateway)
	}))
	t.Cleanup(targetServer.Close)

	proxyCalled := make(chan struct{}, 1)
	proxyServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		proxyCalled <- struct{}{}
		w.WriteHeader(http.StatusOK)
	}))
	t.Cleanup(proxyServer.Close)

	proxyURL := mustParseURLFactoryTest(t, proxyServer.URL)
	binding := config.MCPChannelBinding{
		Channel:         types.DefaultChannel,
		TransportKind:   config.MCPTransportHTTPStreamable,
		ServerURL:       mustParseURLFactoryTest(t, targetServer.URL),
		HTTPProxy:       proxyURL,
		HTTPProxySource: config.ProxySource("mcp.server-url"),
	}
	cfg := &config.MCPConfig{
		ChannelBindings: []config.MCPChannelBinding{binding},
	}

	factory, err := newChannelTransportFactory(channelTransportFactoryParams{
		Config:        cfg,
		Logging:       &config.LoggingConfig{},
		Logger:        slog.New(slog.NewTextHandler(io.Discard, nil)),
		MeterProvider: sdkmetric.NewMeterProvider(),
	})
	if err != nil {
		t.Fatalf("newChannelTransportFactory failed: %v", err)
	}

	client, err := factory.HTTPClientForBinding(binding)
	if err != nil {
		t.Fatalf("HTTPClientForBinding failed: %v", err)
	}
	resp, err := client.Get(targetServer.URL)
	if err != nil {
		t.Fatalf("proxy request failed: %v", err)
	}
	_ = resp.Body.Close()

	select {
	case <-proxyCalled:
	default:
		t.Fatalf("expected proxy to receive request")
	}
	select {
	case <-targetCalled:
		t.Fatalf("expected target not to be called directly")
	default:
	}
}

func TestChannelTransportFactoryDialsUnixSocket(t *testing.T) {
	t.Parallel()

	socketFile, err := os.CreateTemp("/tmp", "mcp-client-*.sock")
	if err != nil {
		t.Fatalf("create unix socket temp file: %v", err)
	}
	socketPath := socketFile.Name()
	if err := socketFile.Close(); err != nil {
		t.Fatalf("close unix socket temp file: %v", err)
	}
	if err := os.Remove(socketPath); err != nil {
		t.Fatalf("remove unix socket temp file: %v", err)
	}

	listener, err := net.Listen("unix", socketPath)
	if err != nil {
		t.Fatalf("listen on unix socket: %v", err)
	}
	t.Cleanup(func() {
		_ = os.Remove(socketPath)
	})
	server := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/mcp" {
			t.Fatalf("unexpected unix socket request path %q", r.URL.Path)
		}
		w.WriteHeader(http.StatusNoContent)
	}))
	server.Listener = listener
	server.Start()
	t.Cleanup(server.Close)

	binding := config.MCPChannelBinding{
		Channel:        types.DefaultChannel,
		TransportKind:  config.MCPTransportHTTPStreamable,
		ServerURL:      mustParseURLFactoryTest(t, "http://localhost/mcp"),
		UnixSocketPath: socketPath,
	}
	cfg := &config.MCPConfig{ChannelBindings: []config.MCPChannelBinding{binding}}

	factory, err := newChannelTransportFactory(channelTransportFactoryParams{
		Config:        cfg,
		Logging:       &config.LoggingConfig{},
		Logger:        slog.New(slog.NewTextHandler(io.Discard, nil)),
		MeterProvider: sdkmetric.NewMeterProvider(),
	})
	if err != nil {
		t.Fatalf("newChannelTransportFactory failed: %v", err)
	}

	client, err := factory.HTTPClientForBinding(binding)
	if err != nil {
		t.Fatalf("HTTPClientForBinding failed: %v", err)
	}
	resp, err := client.Get(binding.ServerURL.String())
	if err != nil {
		t.Fatalf("unix socket request failed: %v", err)
	}
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusNoContent {
		t.Fatalf("unexpected unix socket status %d", resp.StatusCode)
	}
}

func TestChannelTransportFactoryScopesStaticAuthorizationPerBinding(t *testing.T) {
	t.Parallel()

	serverAAuth := make(chan string, 2)
	serverA := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		serverAAuth <- r.Header.Get("Authorization")
		w.WriteHeader(http.StatusNoContent)
	}))
	t.Cleanup(serverA.Close)

	serverBAuth := make(chan string, 2)
	serverB := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		serverBAuth <- r.Header.Get("Authorization")
		w.WriteHeader(http.StatusNoContent)
	}))
	t.Cleanup(serverB.Close)

	defaultBinding := config.MCPChannelBinding{
		Channel:       types.DefaultChannel,
		TransportKind: config.MCPTransportHTTPStreamable,
		ServerURL:     mustParseURLFactoryTest(t, serverA.URL+"/mcp"),
	}
	connectorBinding := config.MCPChannelBinding{
		Channel:       types.Channel("connector-b"),
		TransportKind: config.MCPTransportHTTPStreamable,
		ServerURL:     mustParseURLFactoryTest(t, serverB.URL+"/mcp"),
	}
	cfg := &config.MCPConfig{
		ChannelBindings: []config.MCPChannelBinding{defaultBinding, connectorBinding},
		ExtraHeaders:    map[string]string{"Authorization": "Bearer static-mcp-token"},
	}

	factory, err := newChannelTransportFactory(channelTransportFactoryParams{
		Config:        cfg,
		Logging:       &config.LoggingConfig{},
		Logger:        slog.New(slog.NewTextHandler(io.Discard, nil)),
		MeterProvider: sdkmetric.NewMeterProvider(),
	})
	if err != nil {
		t.Fatalf("newChannelTransportFactory failed: %v", err)
	}

	defaultClient, err := factory.HTTPClientForBinding(defaultBinding)
	if err != nil {
		t.Fatalf("HTTPClientForBinding(default) failed: %v", err)
	}
	resp, err := defaultClient.Get(serverA.URL + "/mcp")
	if err != nil {
		t.Fatalf("default client request to default server failed: %v", err)
	}
	_ = resp.Body.Close()
	requireHeaderValue(t, serverAAuth, "Bearer static-mcp-token")

	resp, err = defaultClient.Get(serverB.URL + "/mcp")
	if err != nil {
		t.Fatalf("default client request to connector server failed: %v", err)
	}
	_ = resp.Body.Close()
	requireHeaderValue(t, serverBAuth, "")

	connectorClient, err := factory.HTTPClientForBinding(connectorBinding)
	if err != nil {
		t.Fatalf("HTTPClientForBinding(connector) failed: %v", err)
	}
	resp, err = connectorClient.Get(serverB.URL + "/mcp")
	if err != nil {
		t.Fatalf("connector client request to connector server failed: %v", err)
	}
	_ = resp.Body.Close()
	requireHeaderValue(t, serverBAuth, "Bearer static-mcp-token")
}

func TestChannelTransportFactoryConnectorAuthorizationOverridesStaticHeader(t *testing.T) {
	t.Parallel()

	seenHeaders := make(chan http.Header, 1)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		seenHeaders <- r.Header.Clone()
		w.Header().Set(HeaderSessionID, "session-from-mcp")
		w.WriteHeader(http.StatusAccepted)
	}))
	t.Cleanup(server.Close)

	binding := config.MCPChannelBinding{
		Channel:       types.DefaultChannel,
		TransportKind: config.MCPTransportHTTPStreamable,
		ServerURL:     mustParseURLFactoryTest(t, server.URL+"/mcp"),
	}
	cfg := &config.MCPConfig{
		ChannelBindings:       []config.MCPChannelBinding{binding},
		ExtraHeaders:          map[string]string{"Authorization": "Bearer static-mcp-token"},
		DiscoveryExtraHeaders: map[string]string{"X-Discovery-Auth": "discovery-only"},
	}

	factory, err := newChannelTransportFactory(channelTransportFactoryParams{
		Config:        cfg,
		Logging:       &config.LoggingConfig{},
		Logger:        slog.New(slog.NewTextHandler(io.Discard, nil)),
		MeterProvider: sdkmetric.NewMeterProvider(),
	})
	if err != nil {
		t.Fatalf("newChannelTransportFactory failed: %v", err)
	}

	client, err := factory.HTTPClientForBinding(binding)
	if err != nil {
		t.Fatalf("HTTPClientForBinding failed: %v", err)
	}

	ctx, carrier, err := internal.ContextWithHeaders(context.Background(), http.Header{
		"Authorization": {"Bearer connector-user-token"},
	})
	if err != nil {
		t.Fatalf("ContextWithHeaders failed: %v", err)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, server.URL+"/mcp", nil)
	if err != nil {
		t.Fatalf("NewRequestWithContext failed: %v", err)
	}
	resp, err := client.Do(req)
	if err != nil {
		t.Fatalf("connector-authorized request failed: %v", err)
	}
	_ = resp.Body.Close()

	gotHeaders := <-seenHeaders
	if got := gotHeaders.Get("Authorization"); got != "Bearer connector-user-token" {
		t.Fatalf("Authorization header = %q, want connector token to override static token", got)
	}
	if got := gotHeaders.Get("X-Discovery-Auth"); got != "" {
		t.Fatalf("runtime request unexpectedly received discovery header %q", got)
	}
	status, responseHeaders := carrier.ResponseStatusAndHeaders()
	if status != http.StatusAccepted {
		t.Fatalf("captured response status = %d, want %d", status, http.StatusAccepted)
	}
	if got := responseHeaders.Get(HeaderSessionID); got != "session-from-mcp" {
		t.Fatalf("captured %s = %q, want session-from-mcp", HeaderSessionID, got)
	}
}

func TestChannelHTTPClientScopesForwardedHeadersAcrossRedirects(t *testing.T) {
	t.Parallel()

	untrustedOriginHeaders := make(chan http.Header, 1)
	untrustedOriginServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		untrustedOriginHeaders <- r.Header.Clone()
		w.WriteHeader(http.StatusNoContent)
	}))
	t.Cleanup(untrustedOriginServer.Close)

	sameOriginHeaders := make(chan http.Header, 1)
	mcpServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/same-origin-redirect":
			http.Redirect(w, r, "/same-origin-target", http.StatusTemporaryRedirect)
		case "/same-origin-target":
			sameOriginHeaders <- r.Header.Clone()
			w.WriteHeader(http.StatusNoContent)
		case "/cross-origin-redirect":
			http.Redirect(w, r, untrustedOriginServer.URL+"/target", http.StatusTemporaryRedirect)
		default:
			t.Fatalf("unexpected MCP request path %q", r.URL.Path)
		}
	}))
	t.Cleanup(mcpServer.Close)

	serverURL := mustParseURLFactoryTest(t, mcpServer.URL+"/mcp")
	factory := newTestChannelTransportFactory(
		t,
		serverURL,
		&config.LoggingConfig{},
		slog.New(slog.NewTextHandler(io.Discard, nil)),
	)
	binding := config.MCPChannelBinding{
		Channel:       types.DefaultChannel,
		TransportKind: config.MCPTransportHTTPStreamable,
		ServerURL:     serverURL,
	}
	client, err := factory.HTTPClientForBinding(binding)
	if err != nil {
		t.Fatalf("HTTPClientForBinding failed: %v", err)
	}

	forwardedHeaders := http.Header{
		"Authorization": {"Bearer connector-token"},
		"X-Api-Key":     {"connector-api-key"},
		HeaderSessionID: {"session-id"},
	}

	for _, testCase := range []struct {
		name        string
		path        string
		seenHeaders <-chan http.Header
		wantHeaders bool
	}{
		{
			name:        "same origin",
			path:        "/same-origin-redirect",
			seenHeaders: sameOriginHeaders,
			wantHeaders: true,
		},
		{
			name:        "cross origin",
			path:        "/cross-origin-redirect",
			seenHeaders: untrustedOriginHeaders,
		},
	} {
		testCase := testCase
		t.Run(testCase.name, func(t *testing.T) {
			ctx, carrier, err := internal.ContextWithHeaders(context.Background(), forwardedHeaders)
			if err != nil {
				t.Fatalf("ContextWithHeaders failed: %v", err)
			}
			mustDoRequest(t, client, http.MethodPost, mcpServer.URL+testCase.path, ctx)

			got := mustReceiveHeaders(t, testCase.seenHeaders)
			for header, values := range forwardedHeaders {
				want := ""
				if testCase.wantHeaders {
					want = values[0]
				}
				if value := got.Get(header); value != want {
					t.Fatalf("redirected %s = %q, want %q", header, value, want)
				}
			}
			status, _ := carrier.ResponseStatusAndHeaders()
			if status != http.StatusNoContent {
				t.Fatalf("captured response status = %d, want %d", status, http.StatusNoContent)
			}
		})
	}
}

func TestRuntimeMCPHTTPClientRejectsCrossOriginRedirects(t *testing.T) {
	t.Parallel()

	for _, statusCode := range []int{
		http.StatusMovedPermanently,
		http.StatusFound,
		http.StatusSeeOther,
		http.StatusTemporaryRedirect,
		http.StatusPermanentRedirect,
	} {
		statusCode := statusCode
		t.Run(http.StatusText(statusCode), func(t *testing.T) {
			t.Parallel()

			var sinkCalls atomic.Int32
			sink := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
				sinkCalls.Add(1)
			}))
			t.Cleanup(sink.Close)

			origin := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				w.Header().Set("Location", sink.URL+"/capture")
				w.WriteHeader(statusCode)
			}))
			t.Cleanup(origin.Close)

			configuredURL := localhostURLFactoryTest(t, origin.URL+"/mcp")
			client := newRuntimeHTTPClientFactoryTest(t, configuredURL, nil)
			ctx, _, err := internal.ContextWithHeaders(context.Background(), http.Header{
				"Authorization":       {"Bearer redirect-proof-secret"},
				"Proxy-Authorization": {"Basic redirect-proof-secret"},
				"Cookie":              {"session=redirect-proof-secret"},
				HeaderSessionID:       {"session-redirect-proof"},
				"X-Connector-Canary":  {"redirect-proof-secret"},
			})
			if err != nil {
				t.Fatalf("ContextWithHeaders failed: %v", err)
			}
			req, err := http.NewRequestWithContext(
				ctx,
				http.MethodPost,
				configuredURL.String(),
				strings.NewReader(`{"jsonrpc":"2.0","method":"redirect-proof"}`),
			)
			if err != nil {
				t.Fatalf("NewRequestWithContext failed: %v", err)
			}

			resp, err := client.Do(req)
			if resp != nil && resp.Body != nil {
				_ = resp.Body.Close()
			}
			if !errors.Is(err, errCrossOriginRuntimeMCPRedirect) {
				t.Fatalf("runtime request error = %v, want cross-origin redirect rejection", err)
			}
			if got := sinkCalls.Load(); got != 0 {
				t.Fatalf("cross-origin redirect sink calls = %d, want 0", got)
			}
		})
	}
}

func TestRuntimeMCPHTTPClientAllowsSameOriginRedirect(t *testing.T) {
	t.Parallel()

	type observedRequest struct {
		method string
		header http.Header
		body   string
	}
	seen := make(chan observedRequest, 1)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/redirect":
			w.Header().Set("Location", "/mcp")
			w.WriteHeader(http.StatusTemporaryRedirect)
		case "/mcp":
			body, err := io.ReadAll(r.Body)
			if err != nil {
				t.Errorf("read redirected body: %v", err)
				return
			}
			seen <- observedRequest{
				method: r.Method,
				header: r.Header.Clone(),
				body:   string(body),
			}
			w.WriteHeader(http.StatusNoContent)
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(server.Close)

	configuredURL := mustParseURLFactoryTest(t, server.URL+"/mcp")
	client := newRuntimeHTTPClientFactoryTest(t, configuredURL, nil)
	ctx, _, err := internal.ContextWithHeaders(context.Background(), http.Header{
		"Authorization":      {"Bearer same-origin-secret"},
		"X-Connector-Canary": {"same-origin"},
	})
	if err != nil {
		t.Fatalf("ContextWithHeaders failed: %v", err)
	}
	const wantBody = `{"jsonrpc":"2.0","method":"same-origin"}`
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, server.URL+"/redirect", strings.NewReader(wantBody))
	if err != nil {
		t.Fatalf("NewRequestWithContext failed: %v", err)
	}
	resp, err := client.Do(req)
	if err != nil {
		t.Fatalf("same-origin redirect failed: %v", err)
	}
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusNoContent {
		t.Fatalf("same-origin redirect status = %d, want %d", resp.StatusCode, http.StatusNoContent)
	}

	got := <-seen
	if got.method != http.MethodPost {
		t.Fatalf("redirected method = %s, want POST", got.method)
	}
	if got.body != wantBody {
		t.Fatalf("redirected body = %q, want %q", got.body, wantBody)
	}
	if got.header.Get("Authorization") != "Bearer same-origin-secret" {
		t.Fatalf("redirected Authorization = %q, want forwarded credential", got.header.Get("Authorization"))
	}
	if got.header.Get("X-Connector-Canary") != "same-origin" {
		t.Fatalf("redirected connector canary = %q, want same-origin", got.header.Get("X-Connector-Canary"))
	}
}

func TestRuntimeMCPHTTPClientRevalidatesEveryRedirectHop(t *testing.T) {
	t.Parallel()

	var sinkCalls atomic.Int32
	sink := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		sinkCalls.Add(1)
	}))
	t.Cleanup(sink.Close)

	origin := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/first":
			w.Header().Set("Location", "/second")
			w.WriteHeader(http.StatusTemporaryRedirect)
		case "/second":
			w.Header().Set("Location", sink.URL+"/capture")
			w.WriteHeader(http.StatusTemporaryRedirect)
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(origin.Close)

	configuredURL := localhostURLFactoryTest(t, origin.URL+"/mcp")
	client := newRuntimeHTTPClientFactoryTest(t, configuredURL, nil)
	req, err := http.NewRequest(http.MethodGet, strings.Replace(configuredURL.String(), "/mcp", "/first", 1), nil)
	if err != nil {
		t.Fatalf("NewRequest failed: %v", err)
	}
	resp, err := client.Do(req)
	if resp != nil && resp.Body != nil {
		_ = resp.Body.Close()
	}
	if !errors.Is(err, errCrossOriginRuntimeMCPRedirect) {
		t.Fatalf("multi-hop runtime request error = %v, want cross-origin redirect rejection", err)
	}
	if got := sinkCalls.Load(); got != 0 {
		t.Fatalf("multi-hop cross-origin redirect sink calls = %d, want 0", got)
	}
}

func TestRuntimeMCPHTTPClientRejectsCrossOriginSessionTerminationRedirect(t *testing.T) {
	t.Parallel()

	for _, statusCode := range []int{http.StatusTemporaryRedirect, http.StatusPermanentRedirect} {
		statusCode := statusCode
		t.Run(http.StatusText(statusCode), func(t *testing.T) {
			t.Parallel()

			var sinkCalls atomic.Int32
			sink := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
				sinkCalls.Add(1)
			}))
			t.Cleanup(sink.Close)

			origin := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.Method != http.MethodDelete {
					t.Errorf("termination method = %s, want DELETE", r.Method)
				}
				w.Header().Set("Location", sink.URL+"/capture")
				w.WriteHeader(statusCode)
			}))
			t.Cleanup(origin.Close)

			configuredURL := localhostURLFactoryTest(t, origin.URL+"/mcp")
			client := newRuntimeHTTPClientFactoryTest(t, configuredURL, nil)
			streamable := &mcp.StreamableClientTransport{
				Endpoint:   configuredURL.String(),
				HTTPClient: client,
			}
			terminator := NewForwardingTransport(streamable).(SessionTerminatingTransport)
			status, headers, err := terminator.TerminateSession(context.Background(), http.Header{
				"Authorization": {"Bearer termination-secret"},
				HeaderSessionID: {"session-termination"},
			})
			if !errors.Is(err, errCrossOriginRuntimeMCPRedirect) {
				t.Fatalf("TerminateSession error = %v, want cross-origin redirect rejection", err)
			}
			if status != 0 || headers != nil {
				t.Fatalf("TerminateSession response = (%d, %v), want (0, nil)", status, headers)
			}
			if got := sinkCalls.Load(); got != 0 {
				t.Fatalf("termination cross-origin redirect sink calls = %d, want 0", got)
			}
		})
	}
}

func TestRuntimeMCPHTTPClientRejectsCrossOriginRedirectBeforeConfiguredProxy(t *testing.T) {
	t.Parallel()

	var proxyCalls atomic.Int32
	proxyServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		if proxyCalls.Add(1) == 1 {
			w.Header().Set("Location", "http://169.254.169.254/latest/meta-data")
			w.WriteHeader(http.StatusTemporaryRedirect)
			return
		}
		w.WriteHeader(http.StatusNoContent)
	}))
	t.Cleanup(proxyServer.Close)

	configuredURL := mustParseURLFactoryTest(t, "http://mcp.example.test/mcp")
	proxyURL := mustParseURLFactoryTest(t, proxyServer.URL)
	client := newRuntimeHTTPClientFactoryTest(t, configuredURL, proxyURL)
	req, err := http.NewRequest(http.MethodPost, configuredURL.String(), strings.NewReader(`{"jsonrpc":"2.0"}`))
	if err != nil {
		t.Fatalf("NewRequest failed: %v", err)
	}
	resp, err := client.Do(req)
	if resp != nil && resp.Body != nil {
		_ = resp.Body.Close()
	}
	if !errors.Is(err, errCrossOriginRuntimeMCPRedirect) {
		t.Fatalf("proxied runtime request error = %v, want cross-origin redirect rejection", err)
	}
	if got := proxyCalls.Load(); got != 1 {
		t.Fatalf("proxy calls = %d, want only the configured-origin request", got)
	}
}

func TestSameURLOriginCanonicalizesRuntimeMCPOrigin(t *testing.T) {
	t.Parallel()

	testCases := []struct {
		name  string
		left  string
		right string
		want  bool
	}{
		{name: "hostname case and default port", left: "HTTP://MCP.Example.Test/mcp", right: "http://mcp.example.test:80/redirect", want: true},
		{name: "hostname trailing dot remains distinct", left: "http://mcp.example.test./mcp", right: "http://mcp.example.test:80/redirect", want: false},
		{name: "hostname matching trailing dots", left: "http://MCP.Example.Test./mcp", right: "http://mcp.example.test.:80/redirect", want: true},
		{name: "https explicit default port", left: "https://mcp.example.test/mcp", right: "https://mcp.example.test:443/redirect", want: true},
		{name: "different scheme", left: "http://mcp.example.test/mcp", right: "https://mcp.example.test/mcp", want: false},
		{name: "different port", left: "https://mcp.example.test:8443/mcp", right: "https://mcp.example.test:443/mcp", want: false},
		{name: "ipv4 default port", left: "http://127.0.0.1/mcp", right: "http://127.0.0.1:80/redirect", want: true},
		{name: "ipv6 canonical spelling", left: "http://[2001:db8::1]/mcp", right: "http://[2001:0db8:0:0:0:0:0:1]:80/redirect", want: true},
		{name: "ipv6 zone case remains distinct", left: "http://[fe80::1%25ETH0]/mcp", right: "http://[fe80::1%25eth0]:80/redirect", want: false},
		{name: "ipv6 zone canonical address spelling", left: "http://[FE80:0:0:0:0:0:0:1%25ETH0]/mcp", right: "http://[fe80::1%25ETH0]:80/redirect", want: true},
		{name: "dns alias does not widen origin", left: "http://mcp.example.test/mcp", right: "http://alias.example.test/mcp", want: false},
		{name: "loopback destination", left: "https://mcp.example.test/mcp", right: "http://127.0.0.1/mcp", want: false},
		{name: "private destination", left: "https://mcp.example.test/mcp", right: "http://10.0.0.1/mcp", want: false},
		{name: "link local metadata destination", left: "https://mcp.example.test/mcp", right: "http://169.254.169.254/latest/meta-data", want: false},
		{name: "ipv6 link local destination", left: "https://mcp.example.test/mcp", right: "http://[fe80::1]/mcp", want: false},
	}
	for _, tc := range testCases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			left := mustParseURLFactoryTest(t, tc.left)
			right := mustParseURLFactoryTest(t, tc.right)
			if got := sameURLOrigin(left, right); got != tc.want {
				t.Fatalf("sameURLOrigin(%q, %q) = %t, want %t", tc.left, tc.right, got, tc.want)
			}
		})
	}
}

func newRuntimeHTTPClientFactoryTest(t *testing.T, serverURL *url.URL, proxyURL *url.URL) *http.Client {
	t.Helper()
	binding := config.MCPChannelBinding{
		Channel:       types.DefaultChannel,
		TransportKind: config.MCPTransportHTTPStreamable,
		ServerURL:     serverURL,
		HTTPProxy:     proxyURL,
	}
	factory, err := newChannelTransportFactory(channelTransportFactoryParams{
		Config:             &config.MCPConfig{ChannelBindings: []config.MCPChannelBinding{binding}},
		Logging:            &config.LoggingConfig{},
		Logger:             slog.New(slog.NewTextHandler(io.Discard, nil)),
		MeterProvider:      sdkmetric.NewMeterProvider(),
		TransportProviders: []TransportProvider{newStreamableTransportProvider()},
	})
	if err != nil {
		t.Fatalf("newChannelTransportFactory failed: %v", err)
	}
	transport, err := factory.Build(binding)
	if err != nil {
		t.Fatalf("Build failed: %v", err)
	}
	streamable, ok := unwrapStreamableClientTransport(transport)
	if !ok || streamable.HTTPClient == nil {
		t.Fatalf("Build transport = %T, want streamable transport with HTTP client", transport)
	}
	return streamable.HTTPClient
}

func localhostURLFactoryTest(t *testing.T, raw string) *url.URL {
	t.Helper()
	parsed := mustParseURLFactoryTest(t, raw)
	parsed.Host = net.JoinHostPort("localhost", parsed.Port())
	return parsed
}

func requireHeaderValue(t *testing.T, ch <-chan string, want string) {
	t.Helper()
	select {
	case got := <-ch:
		if got != want {
			t.Fatalf("Authorization header = %q, want %q", got, want)
		}
	case <-time.After(time.Second):
		t.Fatalf("timed out waiting for request with Authorization header %q", want)
	}
}

func TestChannelTransportFactoryMTLS(t *testing.T) {
	t.Parallel()

	material := newMTLSTestMaterial(t)

	hit := make(chan struct{}, 1)
	server := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hit <- struct{}{}
		w.WriteHeader(http.StatusOK)
	}))
	server.TLS = &tls.Config{
		Certificates: []tls.Certificate{material.serverCertificate},
		ClientAuth:   tls.RequireAndVerifyClientCert,
		ClientCAs:    material.caPool,
		MinVersion:   tls.VersionTLS12,
	}
	server.StartTLS()
	t.Cleanup(server.Close)

	bundle := &tlsconfig.Bundle{RootCAs: material.caPool}

	t.Run("request without client certificate fails", func(t *testing.T) {
		binding := config.MCPChannelBinding{
			Channel:       types.DefaultChannel,
			TransportKind: config.MCPTransportHTTPStreamable,
			ServerURL:     mustParseURLFactoryTest(t, server.URL),
		}
		cfg := &config.MCPConfig{ChannelBindings: []config.MCPChannelBinding{binding}}

		factory, err := newChannelTransportFactory(channelTransportFactoryParams{
			Config:        cfg,
			Logging:       &config.LoggingConfig{},
			Logger:        slog.New(slog.NewTextHandler(io.Discard, nil)),
			MeterProvider: sdkmetric.NewMeterProvider(),
			TLSBundle:     bundle,
		})
		if err != nil {
			t.Fatalf("newChannelTransportFactory failed: %v", err)
		}

		client, err := factory.HTTPClientForBinding(binding)
		if err != nil {
			t.Fatalf("HTTPClientForBinding failed: %v", err)
		}
		resp, err := client.Get(server.URL)
		if err == nil {
			_ = resp.Body.Close()
			t.Fatalf("expected request to fail without client certificate")
		}
	})

	t.Run("request with client certificate succeeds", func(t *testing.T) {
		binding := config.MCPChannelBinding{
			Channel:           types.DefaultChannel,
			TransportKind:     config.MCPTransportHTTPStreamable,
			ServerURL:         mustParseURLFactoryTest(t, server.URL),
			ClientCertificate: material.clientCertificate,
			HTTPProxy:         nil,
			HTTPProxySource:   config.ProxySourceNone,
		}
		cfg := &config.MCPConfig{ChannelBindings: []config.MCPChannelBinding{binding}}

		factory, err := newChannelTransportFactory(channelTransportFactoryParams{
			Config:        cfg,
			Logging:       &config.LoggingConfig{},
			Logger:        slog.New(slog.NewTextHandler(io.Discard, nil)),
			MeterProvider: sdkmetric.NewMeterProvider(),
			TLSBundle:     bundle,
		})
		if err != nil {
			t.Fatalf("newChannelTransportFactory failed: %v", err)
		}

		client, err := factory.HTTPClientForBinding(binding)
		if err != nil {
			t.Fatalf("HTTPClientForBinding failed: %v", err)
		}
		resp, err := client.Get(server.URL)
		if err != nil {
			t.Fatalf("request failed: %v", err)
		}
		_ = resp.Body.Close()
		if resp.StatusCode != http.StatusOK {
			t.Fatalf("expected status 200, got %d", resp.StatusCode)
		}
		select {
		case <-hit:
		default:
			t.Fatalf("expected server handler to be called")
		}
	})
}

type blockingTransportProvider struct {
	started chan struct{}
	release chan struct{}
	count   atomic.Int32
}

func (p *blockingTransportProvider) Kind() config.MCPTransportKind {
	return config.MCPTransportHTTPStreamable
}

func (p *blockingTransportProvider) Build(TransportBuildParams) (mcp.Transport, error) {
	p.count.Add(1)
	select {
	case p.started <- struct{}{}:
	default:
	}
	<-p.release
	return &stubTransport{}, nil
}

func TestChannelTransportFactoryBuildSingleInstanceUnderConcurrency(t *testing.T) {
	t.Parallel()

	binding := config.MCPChannelBinding{
		Channel:       types.DefaultChannel,
		TransportKind: config.MCPTransportHTTPStreamable,
		ServerURL:     mustParseURLFactoryTest(t, "https://example.com"),
	}
	cfg := &config.MCPConfig{
		ChannelBindings: []config.MCPChannelBinding{binding},
	}
	provider := &blockingTransportProvider{
		started: make(chan struct{}, 1),
		release: make(chan struct{}),
	}

	factory, err := newChannelTransportFactory(channelTransportFactoryParams{
		Config:             cfg,
		Logging:            &config.LoggingConfig{},
		Logger:             slog.New(slog.NewTextHandler(io.Discard, nil)),
		MeterProvider:      sdkmetric.NewMeterProvider(),
		TransportProviders: []TransportProvider{provider},
	})
	if err != nil {
		t.Fatalf("newChannelTransportFactory failed: %v", err)
	}

	const callers = 8
	results := make([]mcp.Transport, callers)
	var wg sync.WaitGroup
	wg.Add(callers)
	for i := 0; i < callers; i++ {
		index := i
		go func() {
			defer wg.Done()
			transport, err := factory.Build(binding)
			if err != nil {
				t.Errorf("Build failed: %v", err)
				return
			}
			results[index] = transport
		}()
	}

	<-provider.started
	close(provider.release)
	wg.Wait()

	if got := provider.count.Load(); got != 1 {
		t.Fatalf("expected provider to build once, got %d", got)
	}
	if results[0] == nil {
		t.Fatal("expected transport result, got nil")
	}
	if _, ok := results[0].(*stubTransport); !ok {
		t.Fatalf("expected *stubTransport, got %T", results[0])
	}
	for i := 1; i < callers; i++ {
		if results[i] == nil {
			t.Fatalf("expected transport result at %d, got nil", i)
		}
		if results[i] != results[0] {
			t.Fatalf("expected shared transport instance, index %d differed", i)
		}
	}
}

type mtlsTestMaterial struct {
	caPool            *x509.CertPool
	serverCertificate tls.Certificate
	clientCertificate *tlsconfig.ClientCertificate
}

func newMTLSTestMaterial(t *testing.T) mtlsTestMaterial {
	t.Helper()

	caCert, caKey, caPEM := generateCA(t)
	serverCert := generateSignedLeaf(t, caCert, caKey, "mcp-server", []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth})
	clientCert, clientCertPath, clientKeyPath := generateSignedClientCertificate(t, caCert, caKey)

	pool := x509.NewCertPool()
	if ok := pool.AppendCertsFromPEM(caPEM); !ok {
		t.Fatalf("failed to append CA cert to pool")
	}

	return mtlsTestMaterial{
		caPool:            pool,
		serverCertificate: serverCert,
		clientCertificate: &tlsconfig.ClientCertificate{
			CertPath:    clientCertPath,
			KeyPath:     clientKeyPath,
			Certificate: clientCert,
		},
	}
}

func generateCA(t *testing.T) (*x509.Certificate, *rsa.PrivateKey, []byte) {
	t.Helper()

	caKey, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatalf("generate CA key: %v", err)
	}
	caTemplate := &x509.Certificate{
		SerialNumber: big.NewInt(11),
		Subject: pkix.Name{
			CommonName: "test-ca",
		},
		NotBefore:             time.Now().Add(-time.Hour),
		NotAfter:              time.Now().Add(2 * time.Hour),
		KeyUsage:              x509.KeyUsageCertSign | x509.KeyUsageCRLSign,
		BasicConstraintsValid: true,
		IsCA:                  true,
	}
	caDER, err := x509.CreateCertificate(rand.Reader, caTemplate, caTemplate, &caKey.PublicKey, caKey)
	if err != nil {
		t.Fatalf("create CA certificate: %v", err)
	}
	caCert, err := x509.ParseCertificate(caDER)
	if err != nil {
		t.Fatalf("parse CA certificate: %v", err)
	}
	caPEM := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: caDER})
	if caPEM == nil {
		t.Fatalf("encode CA certificate PEM")
	}
	return caCert, caKey, caPEM
}

func generateSignedLeaf(t *testing.T, caCert *x509.Certificate, caKey *rsa.PrivateKey, commonName string, extKeyUsage []x509.ExtKeyUsage) tls.Certificate {
	t.Helper()

	leafKey, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatalf("generate leaf key: %v", err)
	}
	leafTemplate := &x509.Certificate{
		SerialNumber: big.NewInt(12),
		Subject:      pkix.Name{CommonName: commonName},
		NotBefore:    time.Now().Add(-time.Hour),
		NotAfter:     time.Now().Add(2 * time.Hour),
		KeyUsage:     x509.KeyUsageDigitalSignature | x509.KeyUsageKeyEncipherment,
		ExtKeyUsage:  extKeyUsage,
		DNSNames:     []string{"localhost"},
		IPAddresses:  []net.IP{net.ParseIP("127.0.0.1"), net.ParseIP("::1")},
	}
	leafDER, err := x509.CreateCertificate(rand.Reader, leafTemplate, caCert, &leafKey.PublicKey, caKey)
	if err != nil {
		t.Fatalf("create leaf certificate: %v", err)
	}
	leafPEM := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: leafDER})
	if leafPEM == nil {
		t.Fatalf("encode leaf certificate PEM")
	}
	keyPEM := pem.EncodeToMemory(&pem.Block{Type: "RSA PRIVATE KEY", Bytes: x509.MarshalPKCS1PrivateKey(leafKey)})
	if keyPEM == nil {
		t.Fatalf("encode leaf key PEM")
	}
	pair, err := tls.X509KeyPair(leafPEM, keyPEM)
	if err != nil {
		t.Fatalf("load leaf key pair: %v", err)
	}
	return pair
}

func generateSignedClientCertificate(t *testing.T, caCert *x509.Certificate, caKey *rsa.PrivateKey) (tls.Certificate, string, string) {
	t.Helper()

	clientKey, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatalf("generate client key: %v", err)
	}
	clientTemplate := &x509.Certificate{
		SerialNumber: big.NewInt(13),
		Subject:      pkix.Name{CommonName: "test-client"},
		NotBefore:    time.Now().Add(-time.Hour),
		NotAfter:     time.Now().Add(2 * time.Hour),
		KeyUsage:     x509.KeyUsageDigitalSignature | x509.KeyUsageKeyEncipherment,
		ExtKeyUsage:  []x509.ExtKeyUsage{x509.ExtKeyUsageClientAuth},
	}
	clientDER, err := x509.CreateCertificate(rand.Reader, clientTemplate, caCert, &clientKey.PublicKey, caKey)
	if err != nil {
		t.Fatalf("create client certificate: %v", err)
	}
	clientPEM := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: clientDER})
	if clientPEM == nil {
		t.Fatalf("encode client certificate PEM")
	}
	keyPEM := pem.EncodeToMemory(&pem.Block{Type: "RSA PRIVATE KEY", Bytes: x509.MarshalPKCS1PrivateKey(clientKey)})
	if keyPEM == nil {
		t.Fatalf("encode client key PEM")
	}
	clientPair, err := tls.X509KeyPair(clientPEM, keyPEM)
	if err != nil {
		t.Fatalf("load client key pair: %v", err)
	}
	dir := t.TempDir()
	clientCertPath := filepath.Join(dir, "client.crt")
	clientKeyPath := filepath.Join(dir, "client.key")
	if err := os.WriteFile(clientCertPath, clientPEM, 0o600); err != nil {
		t.Fatalf("write client cert: %v", err)
	}
	if err := os.WriteFile(clientKeyPath, keyPEM, 0o600); err != nil {
		t.Fatalf("write client key: %v", err)
	}
	return clientPair, clientCertPath, clientKeyPath
}

func mustParseURLFactoryTest(t *testing.T, raw string) *url.URL {
	t.Helper()
	parsed, err := url.Parse(raw)
	if err != nil {
		t.Fatalf("parse url: %v", err)
	}
	return parsed
}
