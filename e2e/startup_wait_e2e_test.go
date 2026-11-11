package e2e_test

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"log/slog"
	"net"
	"net/http"
	"net/http/httptest"
	"net/http/httputil"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"testing"
	"time"

	"github.com/openai/tunnel-client/pkg/config"
	"github.com/openai/tunnel-client/pkg/controlplane/wiretypes"
	harnesspkg "github.com/openai/tunnel-client/testsupport/e2e"
	"github.com/openai/tunnel-client/testsupport/mocktunnelservice"
)

const retryingStartupProbeLog = "retrying MCP startup probe"

// A temporary startup failure must not leave a poller consuming OAuth work
// before its dynamically discovered Harpoon targets have been registered.
func TestStartupDiscoveryRecoveryDelaysPollingUntilHarpoonTargetsRegistered(t *testing.T) {
	t.Parallel()

	const requestID = "cmd-startup-oauth-token"
	const tokenResponse = `{"access_token":"fixture-token","token_type":"Bearer"}`
	var recovered atomic.Bool
	var tokenCalls atomic.Int32
	metadataSeen := make(chan struct{})
	var metadataOnce sync.Once
	mcpUnavailableSeen := make(chan struct{})
	var mcpUnavailableOnce sync.Once
	var mcpProxy *httputil.ReverseProxy
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		baseURL := "http://" + r.Host
		switch r.URL.Path {
		case "/.well-known/oauth-protected-resource/mcp", "/.well-known/oauth-protected-resource":
			w.Header().Set("Content-Type", "application/json")
			if !recovered.Load() {
				_, _ = w.Write([]byte(`{"resource":`))
				metadataOnce.Do(func() { close(metadataSeen) })
				return
			}
			_ = json.NewEncoder(w).Encode(map[string]any{
				"resource": baseURL + "/mcp", "authorization_servers": []string{baseURL},
			})
		case "/.well-known/oauth-authorization-server":
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(map[string]any{
				"issuer": baseURL, "authorization_endpoint": baseURL + "/authorize",
				"token_endpoint": baseURL + "/token",
			})
		case "/token":
			if r.Method != http.MethodPost {
				t.Errorf("token request method = %s, want POST", r.Method)
			}
			tokenCalls.Add(1)
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(tokenResponse))
		case "/mcp":
			if !recovered.Load() {
				http.Error(w, "Service Unavailable", http.StatusServiceUnavailable)
				mcpUnavailableOnce.Do(func() { close(mcpUnavailableSeen) })
				return
			}
			mcpProxy.ServeHTTP(w, r)
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(upstream.Close)

	command, err := json.Marshal(map[string]any{
		"command_type": "jsonrpc", "channel": "harpoon", "request_id": requestID,
		"created_at": time.Now().UTC().Format(time.RFC3339), "shard_token": requestID,
		"jsonrpc": map[string]any{
			"jsonrpc": "2.0", "id": requestID, "method": "tools/call",
			"params": map[string]any{
				"name": "call_target",
				"arguments": map[string]any{
					"label": "oauth-token-endpoint-0", "method": "POST",
					"body": "grant_type=authorization_code",
				},
				"_meta": map[string]any{
					"io.modelcontextprotocol/protocolVersion":    "2026-07-28",
					"io.modelcontextprotocol/clientInfo":         map[string]any{"name": "startup-test", "version": "1"},
					"io.modelcontextprotocol/clientCapabilities": map[string]any{},
				},
			},
		},
	})
	if err != nil {
		t.Fatalf("marshal Harpoon command: %v", err)
	}

	healthURLFile := filepath.Join(t.TempDir(), "health.url")
	h := harnesspkg.NewHarness(t,
		harnesspkg.WithPreserveClientURLs(),
		harnesspkg.WithScenarioTimeout(10*time.Second),
		harnesspkg.WithClientConfig(func(cfg *config.Config) {
			cfg.Logging.Level = slog.LevelDebug
			cfg.Health.URLFile = healthURLFile
			cfg.MCP.ServerURL = mustParseURL(t, upstream.URL+"/mcp")
			// Exercise the default MCP startup mode; OAuth startup recovery
			// must not require opting into the separate listener-wait feature.
			cfg.MCP.StartupWaitTimeout = 0
			cfg.Harpoon.AllowPlaintextHTTP = true
			cfg.Harpoon.HostClassifier.IncludeLoopback = true
			cfg.Harpoon.MaxResponseBytes = config.DefaultHarpoonMaxResponseBytes
			cfg.Harpoon.MaxRedirects = config.DefaultHarpoonMaxRedirects
		}),
		harnesspkg.WithControlPlaneOptions(mocktunnelservice.WithCommandResponses(mocktunnelservice.CommandResponse{
			Command: command,
			ExpectedResponses: []mocktunnelservice.ExpectedResponse{{
				RequestID: requestID,
				Assert: func(tb testing.TB, response mocktunnelservice.ReceivedResponse) {
					if tb == nil {
						tb = t
					}
					if response.ResponseCode != http.StatusOK || response.ResponseType != string(wiretypes.ResponsePayloadJSONRPC) {
						tb.Fatalf("token command response = (%d, %s), want JSON-RPC 200", response.ResponseCode, response.ResponseType)
					}
					var payload struct {
						Error  json.RawMessage `json:"error"`
						Result struct {
							IsError           bool `json:"isError"`
							StructuredContent struct {
								StatusCode int    `json:"status_code"`
								BodyBase64 string `json:"body_base64"`
							} `json:"structuredContent"`
						} `json:"result"`
					}
					if err := json.Unmarshal(response.JSONResponse, &payload); err != nil {
						tb.Fatalf("decode token command response: %v", err)
					}
					if len(payload.Error) != 0 || payload.Result.IsError || payload.Result.StructuredContent.StatusCode != http.StatusOK {
						tb.Fatalf("token target call failed after startup recovered: %s", response.JSONResponse)
					}
					if got := payload.Result.StructuredContent.BodyBase64; got != base64.StdEncoding.EncodeToString([]byte(tokenResponse)) {
						tb.Fatalf("token response body = %q, want encoded fixture response", got)
					}
				},
			}},
		})),
		harnesspkg.WithBeforeClientStart(func(h *harnesspkg.Harness) {
			mcpProxy = httputil.NewSingleHostReverseProxy(h.MCP.BaseURL())
		}),
		harnesspkg.WithAfterClientStart(func(h *harnesspkg.Harness) {
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			select {
			case <-metadataSeen:
			case <-ctx.Done():
				t.Fatal("startup did not request the malformed OAuth metadata")
			}
			select {
			case <-mcpUnavailableSeen:
			case <-ctx.Done():
				t.Fatal("startup did not encounter the MCP 503")
			}
			pollCtx, pollCancel := context.WithTimeout(ctx, 250*time.Millisecond)
			defer pollCancel()
			if err := h.ControlPlane.WaitForHTTPRequests(pollCtx, 1, func(request mocktunnelservice.IncomingHTTPRequest) bool {
				return request.Method == http.MethodGet && strings.HasSuffix(request.Path, "/poll")
			}); !errors.Is(err, context.DeadlineExceeded) {
				t.Fatalf("client polled while startup OAuth metadata was invalid: %v", err)
			}
			if got := len(h.ControlPlane.DeliveredCommands()); got != 0 {
				t.Fatalf("delivered %d commands before startup recovered, want 0", got)
			}
			recovered.Store(true)
		}),
		harnesspkg.WithBeforeClientStop(func(h *harnesspkg.Harness) {
			waitHealthStartup(t, h)
			client, base := healthTestClient(t, healthURLFile)
			assertLegacyHealthProbes(t, client, base)
		}),
	)
	h.ExecuteScenarious(t)
	if got := tokenCalls.Load(); got != 1 {
		t.Fatalf("token endpoint calls = %d, want exactly one after startup recovery", got)
	}
}

// TestStartupWaitDelaysPollingUntilMCPDialSucceeds covers sidecar startup when
// TCP dials are initially refused. The mock keeps its listener bound throughout.
func TestStartupWaitDelaysPollingUntilMCPDialSucceeds(t *testing.T) {
	// Keep serial because this test temporarily replaces http.DefaultTransport.
	defaultTransport, ok := http.DefaultTransport.(*http.Transport)
	if !ok {
		t.Fatalf("default HTTP transport has type %T, want *http.Transport", http.DefaultTransport)
	}
	gatedTransport := defaultTransport.Clone()
	dial := gatedTransport.DialContext
	if dial == nil {
		dial = (&net.Dialer{}).DialContext
	}
	var mcpAddr atomic.Pointer[string]
	var mcpAvailable atomic.Bool
	var refusedDials atomic.Int32
	gatedTransport.DialContext = func(ctx context.Context, network, addr string) (net.Conn, error) {
		if target := mcpAddr.Load(); target != nil && addr == *target && !mcpAvailable.Load() {
			if err := ctx.Err(); err != nil {
				return nil, err
			}
			refusedDials.Add(1)
			return nil, &net.OpError{Op: "dial", Net: network, Err: syscall.ECONNREFUSED}
		}
		return dial(ctx, network, addr)
	}
	http.DefaultTransport = gatedTransport
	t.Cleanup(func() {
		http.DefaultTransport = defaultTransport
		gatedTransport.CloseIdleConnections()
	})
	retryLog := newSubstringSignalWriter(retryingStartupProbeLog)

	h := runSimpleToolScenarioWithHarnessOptions(
		t,
		[]harnesspkg.HarnessOption{
			harnesspkg.WithLogWriter(retryLog),
			harnesspkg.WithClientConfig(func(cfg *config.Config) {
				cfg.Logging.Level = slog.LevelDebug
				cfg.MCP.StartupWaitTimeout = 10 * time.Second
			}),
			harnesspkg.WithBeforeClientStart(func(h *harnesspkg.Harness) {
				if h.MCP.BaseURL() == nil {
					t.Fatal("mock MCP server URL is nil")
				}
				addr := h.MCP.BaseURL().Host
				mcpAddr.Store(&addr)
			}),
			harnesspkg.WithAfterClientStart(func(h *harnesspkg.Harness) {
				waitCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
				defer cancel()
				select {
				case <-retryLog.Seen():
				case <-waitCtx.Done():
					t.Fatalf("did not observe %q while MCP dial was refused: %v", retryingStartupProbeLog, waitCtx.Err())
				}
				if refusedDials.Load() == 0 {
					t.Fatal("startup retry did not observe a refused MCP dial")
				}

				if h.MCPProbeState == nil {
					t.Fatal("MCP probe state was not populated")
				}
				if h.MCPProbeState.IsDone() {
					t.Fatal("MCP probe completed while MCP dial was refused")
				}

				client := h.PrimaryClient()
				if client == nil {
					t.Fatal("primary tunnel-client was not started")
				}
				if got := client.PollCount(); got != 0 {
					t.Fatalf("poll count while MCP dial was refused = %d, want 0", got)
				}
				if got := len(h.ControlPlane.DeliveredCommands()); got != 0 {
					t.Fatalf("delivered commands while MCP dial was refused = %d, want 0", got)
				}
				if got := len(h.ControlPlane.ReceivedResponses(mocktunnelservice.ResponseMatchAll)); got != 0 {
					t.Fatalf("responses while MCP dial was refused = %d, want 0", got)
				}

				mcpAvailable.Store(true)
			}),
		},
		nil,
	)

	for _, response := range h.ControlPlane.ReceivedResponses(mocktunnelservice.ResponseMatchAll) {
		if response.ResponseCode == http.StatusBadGateway {
			t.Fatalf("received early terminal 502 for request %q", response.RequestID)
		}
	}
}

type substringSignalWriter struct {
	needle []byte
	seen   chan struct{}
	once   sync.Once
	mu     sync.Mutex
	buf    []byte
}

func newSubstringSignalWriter(needle string) *substringSignalWriter {
	return &substringSignalWriter{
		needle: []byte(needle),
		seen:   make(chan struct{}),
	}
}

func (w *substringSignalWriter) Write(p []byte) (int, error) {
	w.mu.Lock()
	defer w.mu.Unlock()

	w.buf = append(w.buf, p...)
	if bytes.Contains(w.buf, w.needle) {
		w.once.Do(func() { close(w.seen) })
	}
	if keep := len(w.needle) - 1; keep > 0 && len(w.buf) > keep {
		w.buf = append(w.buf[:0], w.buf[len(w.buf)-keep:]...)
	}
	return len(p), nil
}

func (w *substringSignalWriter) Seen() <-chan struct{} {
	return w.seen
}
