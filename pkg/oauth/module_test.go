package oauth

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/modelcontextprotocol/go-sdk/oauthex"
	"go.uber.org/fx"

	"github.com/openai/tunnel-client/pkg/config"
	"github.com/openai/tunnel-client/pkg/harpoon/hostbus"
	"github.com/openai/tunnel-client/pkg/mcpclient"
)

type recordingBus struct {
	mu        sync.Mutex
	notify    chan struct{}
	notifyOne sync.Once
	bundles   []hostbus.URLBundle
}

func (b *recordingBus) Publish(ctx context.Context, bundle hostbus.URLBundle) error {
	b.mu.Lock()
	b.bundles = append(b.bundles, bundle)
	b.mu.Unlock()
	b.notifyOne.Do(func() { close(b.notify) })
	return nil
}

func (b *recordingBus) Close() error { return nil }

func TestLogDiscoveredURLsRedactsSensitiveURLParts(t *testing.T) {
	sensitiveURL, err := url.Parse("https://client:credential@auth.internal/oauth/token?client_id=identifier#state=fragment-value")
	if err != nil {
		t.Fatalf("parse sensitive url: %v", err)
	}
	sensitiveURL.RawFragment = "state%3Dfragment-value"
	sensitiveOriginal := sensitiveURL.String()

	forceQueryURL, err := url.Parse("https://auth.internal/oauth/authorize?")
	if err != nil {
		t.Fatalf("parse force-query url: %v", err)
	}
	forceQueryOriginal := forceQueryURL.String()

	var logBuffer bytes.Buffer
	logger := slog.New(slog.NewJSONHandler(&logBuffer, nil))
	logDiscoveredURLs(logger, hostbus.URLBundle{
		URLs: []hostbus.URLRecord{
			{URL: sensitiveURL},
			{URL: forceQueryURL},
			{URL: nil},
		},
	})

	var logRecord map[string]any
	if err := json.NewDecoder(&logBuffer).Decode(&logRecord); err != nil {
		t.Fatalf("decode log record: %v", err)
	}
	if got := logRecord["msg"]; got != "OAuth discovery URLs published" {
		t.Fatalf("unexpected log message: got %v", got)
	}
	if got := logRecord["url_0"]; got != "https://auth.internal" {
		t.Fatalf("unexpected redacted sensitive url: got %v", got)
	}
	if got := logRecord["url_1"]; got != "https://auth.internal" {
		t.Fatalf("unexpected redacted force-query url: got %v", got)
	}
	if got := logRecord["url_2"]; got != "" {
		t.Fatalf("unexpected nil url: got %v", got)
	}
	if got := sensitiveURL.String(); got != sensitiveOriginal {
		t.Fatalf("sensitive url was mutated: got %q want %q", got, sensitiveOriginal)
	}
	if got := forceQueryURL.String(); got != forceQueryOriginal {
		t.Fatalf("force-query url was mutated: got %q want %q", got, forceQueryOriginal)
	}
}

func TestOAuthDiscoveryPublishesPRMDBundle(t *testing.T) {
	mux := http.NewServeMux()
	server := httptest.NewServer(mux)
	defer server.Close()

	authIssuer := server.URL + "/auth-internal"
	payload, err := json.Marshal(oauthex.ProtectedResourceMetadata{
		Resource: server.URL + "/resource",
		AuthorizationServers: []string{
			authIssuer,
		},
	})
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	mux.HandleFunc("/.well-known/oauth-protected-resource", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write(payload)
	})
	metaPayload, err := json.Marshal(map[string]any{
		"issuer":                 authIssuer,
		"authorization_endpoint": authIssuer + "/authorize",
		"token_endpoint":         authIssuer + "/token",
		"jwks_uri":               authIssuer + "/jwks",
		"introspection_endpoint": authIssuer + "/introspect",
		"registration_endpoint":  authIssuer + "/register",
		"revocation_endpoint":    authIssuer + "/revoke",
	})
	if err != nil {
		t.Fatalf("marshal metadata: %v", err)
	}
	mux.HandleFunc("/.well-known/oauth-authorization-server/auth-internal", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write(metaPayload)
	})

	serverURL, err := url.Parse(server.URL)
	if err != nil {
		t.Fatalf("parse url: %v", err)
	}
	const mcpUnixSocketPath = "/tmp/appgarden-dcr.sock"

	bus := &recordingBus{notify: make(chan struct{})}
	app := fx.New(
		fx.Provide(
			func() *config.MCPConfig {
				return &config.MCPConfig{
					ServerURL:      serverURL,
					TransportKind:  config.MCPTransportHTTPStreamable,
					UnixSocketPath: mcpUnixSocketPath,
				}
			},
			fx.Annotate(
				func() *http.Client { return server.Client() },
				fx.ResultTags(`name:"mcp_client"`),
			),
			func() hostbus.HostRegistrationBus { return bus },
			func() *slog.Logger { return slog.New(slog.NewTextHandler(io.Discard, nil)) },
			NewDiscoveryState,
		),
		fx.Invoke(startOAuthDiscovery),
	)
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	if err := app.Start(ctx); err != nil {
		t.Fatalf("start: %v", err)
	}
	defer func() {
		_ = app.Stop(context.Background())
	}()

	select {
	case <-bus.notify:
	case <-time.After(500 * time.Millisecond):
		t.Fatalf("expected published OAuth discovery bundle")
	}

	bus.mu.Lock()
	defer bus.mu.Unlock()
	if len(bus.bundles) != 1 {
		t.Fatalf("expected 1 bundle, got %d", len(bus.bundles))
	}
	if len(bus.bundles[0].URLs) != 10 {
		t.Fatalf("expected 10 urls, got %d", len(bus.bundles[0].URLs))
	}
	roles := make(map[string]bool, len(bus.bundles[0].URLs))
	for _, record := range bus.bundles[0].URLs {
		if record.UnixSocketPath != mcpUnixSocketPath {
			t.Fatalf("unexpected unix socket path for role %q: got %q want %q", tagValue(record.Tags, hostbus.TagKeyRole), record.UnixSocketPath, mcpUnixSocketPath)
		}
		for _, tag := range record.Tags {
			if tag.Key == hostbus.TagKeyRole {
				roles[tag.Value] = true
			}
		}
	}
	for _, expected := range []string{
		"prmd-resource",
		"prmd-auth-server",
		"prmd-source",
		"auth-server-metadata",
		"issuer",
		"token-endpoint",
		"jwks-uri",
		"introspection-endpoint",
		"registration-endpoint",
		"revocation-endpoint",
	} {
		if !roles[expected] {
			t.Fatalf("expected role %q in published bundle", expected)
		}
	}
}

func TestOAuthDiscoveryDisabledWhenMainChannelNotEnabled(t *testing.T) {
	requestSeen := make(chan struct{})
	var requestOnce sync.Once
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		requestOnce.Do(func() { close(requestSeen) })
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"resource":"http://127.0.0.1/secret"}`))
	}))
	defer server.Close()

	serverURL, err := url.Parse(server.URL + "/mcp")
	if err != nil {
		t.Fatalf("parse server URL: %v", err)
	}

	state := NewDiscoveryState()
	bus := &recordingBus{notify: make(chan struct{})}
	app := fx.New(
		fx.Provide(
			func() *config.MCPConfig {
				return &config.MCPConfig{
					AllowNoMain:   true,
					ServerURL:     serverURL,
					TransportKind: config.MCPTransportHTTPStreamable,
				}
			},
			fx.Annotate(
				func() *http.Client { return server.Client() },
				fx.ResultTags(`name:"mcp_client"`),
			),
			func() hostbus.HostRegistrationBus { return bus },
			func() *slog.Logger { return slog.New(slog.NewTextHandler(io.Discard, nil)) },
			func() *DiscoveryState { return state },
		),
		fx.Invoke(startOAuthDiscovery),
	)
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	if err := app.Start(ctx); err != nil {
		t.Fatalf("start: %v", err)
	}
	defer func() {
		_ = app.Stop(context.Background())
	}()

	_, _, _, discoveryErr, done := state.Wait(time.Second)
	if !done {
		t.Fatal("OAuth discovery did not settle")
	}
	if discoveryErr != nil {
		t.Fatalf("disabled OAuth discovery returned error: %v", discoveryErr)
	}
	select {
	case <-requestSeen:
		t.Fatal("disabled main MCP endpoint received an OAuth discovery request")
	default:
	}

	bus.mu.Lock()
	defer bus.mu.Unlock()
	if len(bus.bundles) != 0 {
		t.Fatalf("disabled main published %d OAuth bundle(s)", len(bus.bundles))
	}
}

func TestOAuthDiscoveryRequiresBus(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"resource":"https://resource.internal/"}`))
	}))
	defer server.Close()

	serverURL, err := url.Parse(server.URL)
	if err != nil {
		t.Fatalf("parse url: %v", err)
	}

	app := fx.New(
		fx.Provide(
			func() *config.MCPConfig {
				return &config.MCPConfig{ServerURL: serverURL, TransportKind: config.MCPTransportHTTPStreamable}
			},
			fx.Annotate(
				func() *http.Client { return server.Client() },
				fx.ResultTags(`name:"mcp_client"`),
			),
			func() *slog.Logger { return slog.New(slog.NewTextHandler(io.Discard, nil)) },
			NewDiscoveryState,
		),
		fx.Invoke(startOAuthDiscovery),
	)
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()

	if err := app.Start(ctx); err == nil {
		t.Fatalf("expected start error when host bus is missing")
	}
}

func TestWaitForMCPStartupProbeDisabledPreservesLegacyBehavior(t *testing.T) {
	t.Parallel()

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := waitForMCPStartupProbe(ctx, &config.MCPConfig{}, mcpclient.NewProbeState()); err != nil {
		t.Fatalf("disabled startup wait returned error: %v", err)
	}
}

func TestWaitForMCPStartupProbeWaitsForGate(t *testing.T) {
	t.Parallel()

	state := mcpclient.NewProbeState()
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	err := waitForMCPStartupProbe(
		ctx,
		&config.MCPConfig{StartupWaitTimeout: time.Second},
		state,
	)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("wait error = %v, want context canceled", err)
	}

	state.Set(nil)
	if err := waitForMCPStartupProbe(
		context.Background(),
		&config.MCPConfig{StartupWaitTimeout: time.Second},
		state,
	); err != nil {
		t.Fatalf("completed startup wait returned error: %v", err)
	}
}

func TestWaitForMCPStartupProbeRequiresStateWhenEnabled(t *testing.T) {
	t.Parallel()

	err := waitForMCPStartupProbe(
		context.Background(),
		&config.MCPConfig{StartupWaitTimeout: time.Second},
		nil,
	)
	if err == nil || !strings.Contains(err.Error(), "MCP probe state is required") {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestOAuthDiscoveryWaitsForMCPStartupProbeBeforeRequest(t *testing.T) {
	t.Parallel()

	serverURL, err := url.Parse("https://mcp.example.test/mcp")
	if err != nil {
		t.Fatalf("parse server url: %v", err)
	}
	probeState := mcpclient.NewProbeState()
	waitLog := newOAuthSignalWriter("waiting for MCP startup probe before OAuth discovery")
	requested := make(chan struct{})
	requestOnce := sync.Once{}
	httpClient := &http.Client{Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
		requestOnce.Do(func() { close(requested) })
		return &http.Response{
			StatusCode: http.StatusNotFound,
			Header:     make(http.Header),
			Body:       io.NopCloser(strings.NewReader("")),
			Request:    req,
		}, nil
	})}
	bus := &recordingBus{notify: make(chan struct{})}

	app := fx.New(
		fx.Provide(
			func() *config.MCPConfig {
				return &config.MCPConfig{
					ServerURL:          serverURL,
					TransportKind:      config.MCPTransportHTTPStreamable,
					StartupWaitTimeout: time.Second,
				}
			},
			fx.Annotate(
				func() *http.Client { return httpClient },
				fx.ResultTags(`name:"mcp_client"`),
			),
			func() hostbus.HostRegistrationBus { return bus },
			func() *slog.Logger { return slog.New(slog.NewTextHandler(waitLog, nil)) },
			func() *mcpclient.ProbeState { return probeState },
			NewDiscoveryState,
		),
		fx.Invoke(startOAuthDiscovery),
	)
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if err := app.Start(ctx); err != nil {
		t.Fatalf("start: %v", err)
	}
	defer func() {
		_ = app.Stop(context.Background())
	}()

	select {
	case <-waitLog.Seen():
	case <-time.After(time.Second):
		t.Fatal("OAuth discovery did not enter MCP startup wait")
	}
	select {
	case <-requested:
		t.Fatal("OAuth discovery made a request before MCP startup probe completed")
	default:
	}

	probeState.Set(nil)
	select {
	case <-requested:
	case <-time.After(time.Second):
		t.Fatal("OAuth discovery did not request metadata after MCP startup probe completed")
	}
}

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(req *http.Request) (*http.Response, error) {
	return f(req)
}

type oauthSignalWriter struct {
	needle string
	seen   chan struct{}
	once   sync.Once
}

func newOAuthSignalWriter(needle string) *oauthSignalWriter {
	return &oauthSignalWriter{needle: needle, seen: make(chan struct{})}
}

func (w *oauthSignalWriter) Write(p []byte) (int, error) {
	if strings.Contains(string(p), w.needle) {
		w.once.Do(func() { close(w.seen) })
	}
	return len(p), nil
}

func (w *oauthSignalWriter) Seen() <-chan struct{} {
	return w.seen
}
