package oauth

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/url"
	"sync"
	"testing"
	"time"

	"github.com/modelcontextprotocol/go-sdk/oauthex"
	"go.uber.org/fx"

	"github.com/openai/tunnel-client/pkg/config"
	"github.com/openai/tunnel-client/pkg/harpoon/hostbus"
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
