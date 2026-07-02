package e2e_test

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/openai/tunnel-client/pkg/config"
	"github.com/openai/tunnel-client/pkg/controlplane/wiretypes"
	"github.com/openai/tunnel-client/pkg/types"
	harnesspkg "github.com/openai/tunnel-client/testsupport/e2e"
	"github.com/openai/tunnel-client/testsupport/mockmcpserver"
	"github.com/openai/tunnel-client/testsupport/mockproxy"
	"github.com/openai/tunnel-client/testsupport/mocktunnelservice"
)

func TestHarnessHandlesOAuthDiscoveryCommand(t *testing.T) {

	const requestID = "cmd-oauth"

	oauthCommand := mocktunnelservice.CommandResponse{
		Command: mocktunnelservice.NewOAuthDiscoveryCommand(requestID, nil),
		ExpectedResponses: []mocktunnelservice.ExpectedResponse{{
			RequestID: requestID,
			Assert: func(tb testing.TB, resp mocktunnelservice.ReceivedResponse) {
				if tb != nil {
					tb.Helper()
				}
				target := tb
				if target == nil {
					target = t
				}
				if resp.ResponseType != string(wiretypes.ResponsePayloadOAuth) {
					target.Fatalf("oauth discovery response type mismatch: got %q", resp.ResponseType)
				}
				if resp.ResponseCode != http.StatusOK {
					target.Fatalf("oauth discovery response code mismatch: %d", resp.ResponseCode)
				}
				var payload map[string]any
				if err := json.Unmarshal(resp.JSONResponse, &payload); err != nil {
					target.Fatalf("decode oauth discovery payload: %v", err)
				}
				if payload["resource"] == "" {
					target.Fatalf("oauth discovery payload missing resource: %v", payload)
				}
			},
		}},
	}

	h := harnesspkg.NewHarness(
		t,
		harnesspkg.WithClientConfig(func(cfg *config.Config) {
			cfg.Logging.Level = slog.LevelDebug
		}),
		harnesspkg.WithControlPlaneOptions(
			mocktunnelservice.WithCommandResponses(oauthCommand),
		),
		harnesspkg.WithMCPOptions(
			mockmcpserver.WithOAuthDiscoveryResources(),
		),
	)

	h.ExecuteScenarious(t)

	matched := h.ControlPlane.ReceivedResponses(mocktunnelservice.ResponseMatchMatched)
	if len(matched) != 1 {
		t.Fatalf("expected single oauth discovery response; got %d", len(matched))
	}
	if matched[0].RequestID != requestID {
		t.Fatalf("unexpected response request id: %s", matched[0].RequestID)
	}
}

func TestHarnessHandlesOAuthDiscoveryCommandWithWWWAuthenticateProbe(t *testing.T) {

	const requestID = "cmd-oauth-www-auth"

	oauthCommand := mocktunnelservice.CommandResponse{
		Command: mocktunnelservice.NewOAuthDiscoveryCommand(requestID, nil),
		ExpectedResponses: []mocktunnelservice.ExpectedResponse{{
			RequestID: requestID,
			Assert: func(tb testing.TB, resp mocktunnelservice.ReceivedResponse) {
				if tb != nil {
					tb.Helper()
				}
				target := tb
				if target == nil {
					target = t
				}
				if resp.ResponseType != string(wiretypes.ResponsePayloadOAuth) {
					target.Fatalf("oauth discovery response type mismatch: got %q", resp.ResponseType)
				}
				if resp.ResponseCode != http.StatusOK {
					target.Fatalf("oauth discovery response code mismatch: %d", resp.ResponseCode)
				}
				var payload map[string]any
				if err := json.Unmarshal(resp.JSONResponse, &payload); err != nil {
					target.Fatalf("decode oauth discovery payload: %v", err)
				}
				if payload["resource"] == "" {
					target.Fatalf("oauth discovery payload missing resource: %v", payload)
				}
			},
		}},
	}

	h := harnesspkg.NewHarness(
		t,
		harnesspkg.WithClientConfig(func(cfg *config.Config) {
			cfg.Logging.Level = slog.LevelDebug
		}),
		harnesspkg.WithControlPlaneOptions(
			mocktunnelservice.WithCommandResponses(oauthCommand),
		),
		harnesspkg.WithMCPOptions(
			mockmcpserver.WithWWWAuthenticateProbe(),
			mockmcpserver.WithOAuthDiscoveryResources(),
		),
	)

	h.ExecuteScenarious(t)

	matched := h.ControlPlane.ReceivedResponses(mocktunnelservice.ResponseMatchMatched)
	if len(matched) != 1 {
		t.Fatalf("expected single oauth discovery response; got %d", len(matched))
	}
	if matched[0].RequestID != requestID {
		t.Fatalf("unexpected response request id: %s", matched[0].RequestID)
	}
}

func TestOAuthDiscoveryRegistersCustomerHostRegistrationEndpointE2E(t *testing.T) {
	const (
		customerHost     = "location-mcp.internal.preproduction.smp.bigco-example.com"
		idpIssuer        = "http://idp.bigco-example.com/oauth2/aus2jrb9zi4O8hseE0h8"
		discoveryID      = "cmd-oauth-customer-host"
		harpoonInitID    = "cmd-harpoon-init-after-oauth"
		harpoonReadyID   = "cmd-harpoon-ready-after-oauth"
		harpoonListID    = "cmd-harpoon-list-targets-after-oauth"
		harpoonCallID    = "cmd-harpoon-auth-metadata"
		harpoonListRPCID = "call-list-targets"
		harpoonJSONRPCID = "call-auth-metadata"
	)
	customerBase := "http://" + customerHost
	idpTokenEndpoint := idpIssuer + "/v1/token"
	harpoonOAuthTargetsReady := make(chan struct{})

	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/.well-known/oauth-protected-resource/mcp", "/.well-known/oauth-protected-resource":
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(map[string]any{
				"resource":              customerBase + "/mcp",
				"authorization_servers": []string{customerBase},
				"scopes_supported":      []string{"mcp:tools"},
			})
		case "/.well-known/oauth-authorization-server":
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(map[string]any{
				"issuer":                                idpIssuer,
				"authorization_endpoint":                idpIssuer + "/v1/authorize",
				"token_endpoint":                        idpTokenEndpoint,
				"registration_endpoint":                 customerBase + "/register",
				"revocation_endpoint":                   idpIssuer + "/v1/revoke",
				"code_challenge_methods_supported":      []string{"S256"},
				"token_endpoint_auth_methods_supported": []string{"none"},
			})
		default:
			http.NotFound(w, r)
		}
	}))
	defer upstream.Close()

	proxy := mockproxy.New(mockproxy.WithRoute(customerHost, mustParseURL(t, upstream.URL)))
	proxy.Start()
	t.Cleanup(proxy.Close)

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
					target.Fatalf("oauth discovery response type mismatch: got %q", resp.ResponseType)
				}
				if resp.ResponseCode != http.StatusOK {
					target.Fatalf("oauth discovery response code mismatch: %d", resp.ResponseCode)
				}
			},
		}},
	}

	harpoonInitialize := mocktunnelservice.CommandResponse{
		Command: newChannelCommand(
			harpoonInitID,
			types.ChannelHarpoon.String(),
			json.RawMessage(`{
				"jsonrpc":"2.0",
				"id":"initialize-harpoon-customer-host",
				"method":"initialize",
				"params":{
					"protocolVersion":"2025-06-18",
					"capabilities":{},
					"clientInfo":{"name":"harpoon-e2e","version":"0.0.1"}
				}
			}`),
			http.Header{
				"Accept":       []string{"application/json, text/event-stream"},
				"Content-Type": []string{"application/json"},
			},
		),
		ExpectedResponses: []mocktunnelservice.ExpectedResponse{{
			RequestID: harpoonInitID,
			Assert: func(tb testing.TB, resp mocktunnelservice.ReceivedResponse) {
				if tb != nil {
					tb.Helper()
				}
				target := tb
				if target == nil {
					target = t
				}
				if resp.ResponseType != string(wiretypes.ResponsePayloadJSONRPC) {
					target.Fatalf("harpoon initialize response type mismatch: got %q", resp.ResponseType)
				}
				if resp.ResponseCode != http.StatusOK {
					target.Fatalf("harpoon initialize response code mismatch: %d", resp.ResponseCode)
				}
			},
		}},
	}

	harpoonInitialized := mocktunnelservice.CommandResponse{
		Command: newChannelCommand(
			harpoonReadyID,
			types.ChannelHarpoon.String(),
			json.RawMessage(`{
				"jsonrpc":"2.0",
				"method":"notifications/initialized",
				"params":{}
			}`),
			http.Header{
				"Accept":       []string{"application/json"},
				"Content-Type": []string{"application/json"},
			},
		),
		ExpectedResponses: []mocktunnelservice.ExpectedResponse{{
			RequestID: harpoonReadyID,
			Assert: func(tb testing.TB, resp mocktunnelservice.ReceivedResponse) {
				if tb != nil {
					tb.Helper()
				}
				target := tb
				if target == nil {
					target = t
				}
				if resp.ResponseType != string(wiretypes.ResponsePayloadNotifyAck) {
					target.Fatalf("harpoon initialized response type mismatch: got %q", resp.ResponseType)
				}
				if resp.ResponseCode != http.StatusOK {
					target.Fatalf("harpoon initialized response code mismatch: %d", resp.ResponseCode)
				}
			},
		}},
	}

	harpoonCallAuthMetadata := mocktunnelservice.CommandResponse{
		Command: newChannelCommand(
			harpoonCallID,
			types.ChannelHarpoon.String(),
			json.RawMessage(`{
				"jsonrpc":"2.0",
				"id":"`+harpoonJSONRPCID+`",
				"method":"tools/call",
				"params":{
					"name":"call_target",
					"arguments":{
						"label":"oauth-auth-server-metadata-0",
						"method":"GET",
						"headers":{}
					}
				}
			}`),
			http.Header{
				"Accept":       []string{"application/json"},
				"Content-Type": []string{"application/json"},
			},
		),
		ExpectedResponses: []mocktunnelservice.ExpectedResponse{{
			RequestID: harpoonCallID,
			Assert: func(tb testing.TB, resp mocktunnelservice.ReceivedResponse) {
				if tb != nil {
					tb.Helper()
				}
				target := tb
				if target == nil {
					target = t
				}
				if resp.ResponseType != string(wiretypes.ResponsePayloadJSONRPC) {
					target.Fatalf("harpoon call response type mismatch: got %q", resp.ResponseType)
				}
				if resp.ResponseCode != http.StatusOK {
					target.Fatalf("harpoon call response code mismatch: %d", resp.ResponseCode)
				}

				var payload struct {
					Result struct {
						StructuredContent struct {
							StatusCode int    `json:"status_code"`
							BodyBase64 string `json:"body_base64"`
						} `json:"structuredContent"`
					} `json:"result"`
					Error json.RawMessage `json:"error"`
				}
				if err := json.Unmarshal(resp.JSONResponse, &payload); err != nil {
					target.Fatalf("decode harpoon call response: %v", err)
				}
				if len(payload.Error) != 0 {
					target.Fatalf("harpoon call returned JSON-RPC error: %s", payload.Error)
				}
				if payload.Result.StructuredContent.StatusCode != http.StatusOK {
					target.Fatalf("harpoon auth metadata status mismatch: %d", payload.Result.StructuredContent.StatusCode)
				}
				body, err := base64.StdEncoding.DecodeString(payload.Result.StructuredContent.BodyBase64)
				if err != nil {
					target.Fatalf("decode harpoon auth metadata body: %v", err)
				}
				var metadata map[string]any
				if err := json.Unmarshal(body, &metadata); err != nil {
					target.Fatalf("decode harpoon auth metadata JSON: %v", err)
				}
				if got := metadata["registration_endpoint"]; got != "harpoon://oauth-registration-endpoint-0" {
					target.Fatalf("registration endpoint mismatch: got %v", got)
				}
				if got := metadata["token_endpoint"]; got != idpTokenEndpoint {
					target.Fatalf("token endpoint should stay public, got %v", got)
				}
			},
		}},
	}

	harpoonListTargets := mocktunnelservice.CommandResponse{
		Command: newChannelCommand(
			harpoonListID,
			types.ChannelHarpoon.String(),
			json.RawMessage(`{
				"jsonrpc":"2.0",
				"id":"`+harpoonListRPCID+`",
				"method":"tools/call",
				"params":{
					"name":"list_targets",
					"arguments":{}
				}
			}`),
			http.Header{
				"Accept":       []string{"application/json"},
				"Content-Type": []string{"application/json"},
			},
		),
		DeliverAfter: harpoonOAuthTargetsReady,
		ExpectedResponses: []mocktunnelservice.ExpectedResponse{{
			RequestID: harpoonListID,
			Assert: func(tb testing.TB, resp mocktunnelservice.ReceivedResponse) {
				if tb != nil {
					tb.Helper()
				}
				target := tb
				if target == nil {
					target = t
				}
				if resp.ResponseType != string(wiretypes.ResponsePayloadJSONRPC) {
					target.Fatalf("harpoon list_targets response type mismatch: got %q", resp.ResponseType)
				}
				if resp.ResponseCode != http.StatusOK {
					target.Fatalf("harpoon list_targets response code mismatch: %d", resp.ResponseCode)
				}

				var payload struct {
					Result struct {
						StructuredContent struct {
							Targets []struct {
								Label string `json:"label"`
							} `json:"targets"`
						} `json:"structuredContent"`
					} `json:"result"`
					Error json.RawMessage `json:"error"`
				}
				if err := json.Unmarshal(resp.JSONResponse, &payload); err != nil {
					target.Fatalf("decode harpoon list_targets response: %v", err)
				}
				if len(payload.Error) != 0 {
					target.Fatalf("harpoon list_targets returned JSON-RPC error: %s", payload.Error)
				}
				foundAuthMetadataTarget := false
				for _, entry := range payload.Result.StructuredContent.Targets {
					if entry.Label == "oauth-auth-server-metadata-0" {
						foundAuthMetadataTarget = true
						break
					}
				}
				if !foundAuthMetadataTarget {
					target.Fatalf("harpoon list_targets missing oauth-auth-server-metadata-0")
				}
			},
		}},
	}

	h := harnesspkg.NewHarness(
		t,
		harnesspkg.WithPreserveClientURLs(),
		harnesspkg.WithClientConfig(func(cfg *config.Config) {
			cfg.Logging.Level = slog.LevelDebug
			cfg.MCP.TransportKind = config.MCPTransportHTTPStreamable
			cfg.MCP.ServerURL = mustParseURL(t, customerBase+"/mcp")
			cfg.MCP.HTTPProxy = mustParseURL(t, proxy.URL())
			cfg.MCP.HTTPProxySource = config.ProxySource("mcp.http-proxy")
			cfg.MCP.ChannelBindings = []config.MCPChannelBinding{{
				Channel:         types.DefaultChannel,
				TransportKind:   config.MCPTransportHTTPStreamable,
				ServerURL:       cfg.MCP.ServerURL,
				HTTPProxy:       cfg.MCP.HTTPProxy,
				HTTPProxySource: cfg.MCP.HTTPProxySource,
			}}
			cfg.Harpoon.AllowPlaintextHTTP = true
			cfg.Harpoon.MaxResponseBytes = config.DefaultHarpoonMaxResponseBytes
			cfg.Harpoon.MaxRedirects = config.DefaultHarpoonMaxRedirects
			cfg.Harpoon.HTTPProxy = mustParseURL(t, proxy.URL())
			cfg.Harpoon.HTTPProxySource = config.ProxySource("harpoon.http-proxy")
			cfg.Harpoon.Targets = []config.HarpoonTarget{{
				Label:       "seed",
				Description: "seed target for routable harpoon channel",
				BaseURL:     mustParseURL(t, upstream.URL),
			}}
		}),
		harnesspkg.WithControlPlaneOptions(
			mocktunnelservice.WithCommandResponses(
				oauthCommand,
				harpoonInitialize,
				harpoonInitialized,
				harpoonListTargets,
				harpoonCallAuthMetadata,
			),
		),
		harnesspkg.WithAfterClientStart(func(h *harnesspkg.Harness) {
			t.Helper()
			if h.HarpoonRegistry == nil {
				t.Fatal("harpoon registry not populated")
			}
			go func() {
				ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
				defer cancel()
				for _, label := range []string{
					"oauth-auth-server-metadata-0",
					"oauth-registration-endpoint-0",
				} {
					if _, err := h.HarpoonRegistry.WaitForTarget(ctx, label); err != nil {
						t.Errorf("wait for harpoon target %q: %v", label, err)
					}
				}
				close(harpoonOAuthTargetsReady)
			}()
		}),
	)

	h.ExecuteScenarious(t)
}
