package e2e_test

import (
	"bytes"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	"github.com/modelcontextprotocol/go-sdk/oauthex"

	"go.openai.org/api/tunnel-client/pkg/config"
	"go.openai.org/api/tunnel-client/pkg/controlplane/wiretypes"
	harnesspkg "go.openai.org/api/tunnel-client/testsupport/e2e"
	"go.openai.org/api/tunnel-client/testsupport/mockmcpserver"
	"go.openai.org/api/tunnel-client/testsupport/mocktunnelservice"
)

func TestMCPStaticHeadersE2E(t *testing.T) {
	t.Run("fails without configured static headers", func(t *testing.T) {
		t.Parallel()

		const requestID = "cmd-oauth-static-required"
		h := harnesspkg.NewHarness(
			t,
			harnesspkg.WithScenarioTimeout(300*time.Millisecond),
			harnesspkg.WithControlPlaneOptions(
				mocktunnelservice.WithAllowPendingCommands(),
				mocktunnelservice.WithCommandResponses(mocktunnelservice.CommandResponse{
					Command: mocktunnelservice.NewOAuthDiscoveryCommand(requestID, nil),
					ExpectedResponses: []mocktunnelservice.ExpectedResponse{{
						RequestID: requestID,
					}},
				}),
			),
			harnesspkg.WithMCPOptions(
				mockmcpserver.WithOAuthDiscoveryResources(),
				mockmcpserver.WithRequiredHeader("X-MCP-Static", "mcp-static"),
			),
		)

		if err := h.ExecuteScenario(t); err == nil {
			t.Fatalf("expected scenario to fail without MCP static headers")
		}
	})

	t.Run("sends scoped runtime and discovery static headers", func(t *testing.T) {
		t.Parallel()

		const (
			discoveryID = "cmd-oauth-static-pass"
			toolID      = "cmd-tool-static-pass"
			callID      = "tool-static-call"
		)

		var (
			authServerMu      sync.Mutex
			authServerHeaders []http.Header
		)
		authServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			authServerMu.Lock()
			authServerHeaders = append(authServerHeaders, r.Header.Clone())
			authServerMu.Unlock()
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(map[string]any{
				"issuer":                 authServerURLForRequest(r),
				"authorization_endpoint": authServerURLForRequest(r) + "/authorize",
				"token_endpoint":         authServerURLForRequest(r) + "/token",
			})
		}))
		defer authServer.Close()

		oauthCommand := mocktunnelservice.CommandResponse{
			Command: mocktunnelservice.NewOAuthDiscoveryCommand(discoveryID, nil),
			ExpectedResponses: []mocktunnelservice.ExpectedResponse{{
				RequestID: discoveryID,
				Assert: func(tb testing.TB, resp mocktunnelservice.ReceivedResponse) {
					if tb != nil {
						tb.Helper()
					}
					target := tb
					if target == nil {
						target = t
					}
					if resp.ResponseType != string(wiretypes.ResponsePayloadOAuth) {
						target.Fatalf("oauth discovery response type = %q", resp.ResponseType)
					}
					if resp.ResponseCode != http.StatusOK {
						target.Fatalf("oauth discovery status = %d", resp.ResponseCode)
					}
				},
			}},
		}

		toolHeaders := http.Header{
			"X-Internal-Auth": []string{"forwarded-runtime"},
		}
		toolCommand := mocktunnelservice.CommandResponse{
			Command: mocktunnelservice.NewCommand(
				toolID,
				json.RawMessage(`{
					"jsonrpc":"2.0",
					"id":"`+callID+`",
					"method":"tools/call",
					"params":{"name":"echo","arguments":{"name":"static"}}
				}`),
				toolHeaders,
			),
			ExpectedResponses: []mocktunnelservice.ExpectedResponse{{
				RequestID: toolID,
				Assert: func(tb testing.TB, resp mocktunnelservice.ReceivedResponse) {
					if tb != nil {
						tb.Helper()
					}
					target := tb
					if target == nil {
						target = t
					}
					if resp.ResponseType != string(wiretypes.ResponsePayloadJSONRPC) {
						target.Fatalf("tool response type = %q", resp.ResponseType)
					}
					if resp.ResponseCode != http.StatusOK {
						target.Fatalf("tool status = %d", resp.ResponseCode)
					}
				},
			}},
		}

		h := harnesspkg.NewHarness(
			t,
			harnesspkg.WithClientConfig(func(cfg *config.Config) {
				cfg.Logging.Level = slog.LevelDebug
				cfg.MCP.ExtraHeaders = map[string]string{
					"X-MCP-Static":    "mcp-static",
					"X-Internal-Auth": "mcp-generic",
				}
				cfg.MCP.DiscoveryExtraHeaders = map[string]string{
					"X-Discovery-Auth": "discovery-static",
					"X-Internal-Auth":  "discovery-override",
				}
			}),
			harnesspkg.WithControlPlaneOptions(
				mocktunnelservice.WithSessionHeaderPropagation(),
				mocktunnelservice.WithInitializationPhaseCommands(),
				mocktunnelservice.WithCommandResponses(oauthCommand, toolCommand),
			),
			harnesspkg.WithBeforeClientStop(func(h *harnesspkg.Harness) {
				if err := h.WaitForMCPProbe(t.Context()); err != nil {
					t.Fatalf("wait for MCP startup probe: %v", err)
				}
			}),
			harnesspkg.WithMCPOptions(
				mockmcpserver.WithWWWAuthenticateProbe(),
				mockmcpserver.WithProtectedResourceMetadata(oauthex.ProtectedResourceMetadata{
					Resource:             "https://mcp.example.test/mcp",
					AuthorizationServers: []string{authServer.URL},
					ScopesSupported:      []string{"read"},
				}),
				mockmcpserver.WithRequiredHeader("X-MCP-Static", "mcp-static"),
				mockmcpserver.WithCalls(mockmcpserver.Call{
					Tool:   "echo",
					Result: json.RawMessage(`{"message":"ok"}`),
				}),
			),
		)

		h.ExecuteScenarious(t)

		authServerMu.Lock()
		authHeadersSnapshot := append([]http.Header(nil), authServerHeaders...)
		authServerMu.Unlock()

		assertControlPlaneDidNotReceiveStaticHeaders(t, h.ControlPlane.ReceivedHTTPRequests())
		assertAuthServerDidNotReceiveStaticHeaders(t, authHeadersSnapshot)
		assertMCPDiscoveryHeaders(t, h.MCP.ReceivedHTTPRequests())
		assertMCPRuntimeHeaders(t, h.MCP.ReceivedRequests())
	})
}

func authServerURLForRequest(r *http.Request) string {
	return fmt.Sprintf("http://%s", r.Host)
}

func assertControlPlaneDidNotReceiveStaticHeaders(t *testing.T, requests []mocktunnelservice.IncomingHTTPRequest) {
	t.Helper()
	if len(requests) == 0 {
		t.Fatalf("expected control-plane requests")
	}
	for _, req := range requests {
		if got := req.Headers.Get("X-MCP-Static"); got != "" {
			t.Fatalf("control-plane %s %s received X-MCP-Static=%q", req.Method, req.Path, got)
		}
		if got := req.Headers.Get("X-Discovery-Auth"); got != "" {
			t.Fatalf("control-plane %s %s received X-Discovery-Auth=%q", req.Method, req.Path, got)
		}
	}
}

func assertAuthServerDidNotReceiveStaticHeaders(t *testing.T, requests []http.Header) {
	t.Helper()
	if len(requests) == 0 {
		t.Fatalf("expected auth-server metadata requests")
	}
	for _, headers := range requests {
		if got := headers.Get("X-MCP-Static"); got != "" {
			t.Fatalf("auth server received X-MCP-Static=%q", got)
		}
		if got := headers.Get("X-Discovery-Auth"); got != "" {
			t.Fatalf("auth server received X-Discovery-Auth=%q", got)
		}
	}
}

func assertMCPDiscoveryHeaders(t *testing.T, requests []mockmcpserver.IncomingHTTPRequest) {
	t.Helper()
	var (
		wellKnownSeen           bool
		startupInitializeSeen   bool
		runtimeInitializeNoDisc bool
	)
	for _, req := range requests {
		if req.Path == "/.well-known/oauth-protected-resource" {
			wellKnownSeen = true
			if got := req.Headers.Get("X-MCP-Static"); got != "mcp-static" {
				t.Fatalf("well-known X-MCP-Static=%q", got)
			}
			if got := req.Headers.Get("X-Discovery-Auth"); got != "discovery-static" {
				t.Fatalf("well-known X-Discovery-Auth=%q", got)
			}
			if got := req.Headers.Get("X-Internal-Auth"); got != "discovery-override" {
				t.Fatalf("well-known X-Internal-Auth=%q", got)
			}
		}
		if req.Method == http.MethodPost && bytes.Contains(req.Body, []byte(`"method":"initialize"`)) {
			if req.Headers.Get("X-Discovery-Auth") == "discovery-static" {
				startupInitializeSeen = true
			} else {
				runtimeInitializeNoDisc = true
			}
		}
	}
	if !wellKnownSeen {
		t.Fatalf("expected well-known discovery request")
	}
	if !startupInitializeSeen {
		t.Fatalf("expected startup initialize probe with discovery headers")
	}
	if !runtimeInitializeNoDisc {
		t.Fatalf("expected runtime initialize without discovery headers")
	}
}

func assertMCPRuntimeHeaders(t *testing.T, requests []mockmcpserver.IncomingRequest) {
	t.Helper()
	if len(requests) != 1 {
		t.Fatalf("expected one tool request, got %d", len(requests))
	}
	headers := requests[0].Headers
	if got := headers.Get("X-MCP-Static"); got != "mcp-static" {
		t.Fatalf("runtime X-MCP-Static=%q", got)
	}
	if got := headers.Get("X-Internal-Auth"); got != "forwarded-runtime" {
		t.Fatalf("runtime X-Internal-Auth=%q", got)
	}
	if got := headers.Get("X-Discovery-Auth"); got != "" {
		t.Fatalf("runtime unexpectedly received X-Discovery-Auth=%q", got)
	}
}
