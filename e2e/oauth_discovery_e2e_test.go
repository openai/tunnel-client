package e2e_test

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
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

func TestOAuthDiscoveredHarpoonTargetIsUnavailableAfterSecondaryDiscoveryMissE2E(t *testing.T) {
	// Characterize the current proc-affinity boundary. Once dynamic Harpoon
	// targets become replica-safe, this test should flip from rejection to success.
	const (
		customerHost = "redundant-mcp.internal.preproduction.smp.bigco-example.com"
		idpIssuer    = "http://idp.bigco-example.com/oauth2/aus2jrb9zi4O8hseE0h8"
		targetLabel  = "oauth-registration-endpoint-0"
		requestID    = "cmd-harpoon-process-local-oauth-target"
	)
	customerBase := "http://" + customerHost
	discoveryEnabled := atomic.Bool{}
	discoveryEnabled.Store(true)
	var registrationCalls atomic.Int32
	secondaryDiscoveryFailed := make(chan struct{}, 1)
	secondaryReady := make(chan struct{})

	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/register":
			registrationCalls.Add(1)
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"registered":true}`))
		case "/.well-known/oauth-protected-resource/mcp", "/.well-known/oauth-protected-resource":
			if !discoveryEnabled.Load() {
				select {
				case secondaryDiscoveryFailed <- struct{}{}:
				default:
				}
				http.Error(w, "discovery unavailable", http.StatusServiceUnavailable)
				return
			}
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(map[string]any{
				"resource":              customerBase + "/mcp",
				"authorization_servers": []string{customerBase},
				"scopes_supported":      []string{"mcp:tools"},
			})
		case "/.well-known/oauth-authorization-server":
			if !discoveryEnabled.Load() {
				select {
				case secondaryDiscoveryFailed <- struct{}{}:
				default:
				}
				http.Error(w, "discovery unavailable", http.StatusServiceUnavailable)
				return
			}
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(map[string]any{
				"issuer":                                idpIssuer,
				"authorization_endpoint":                idpIssuer + "/v1/authorize",
				"token_endpoint":                        idpIssuer + "/v1/token",
				"registration_endpoint":                 customerBase + "/register",
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

	callTargetCommand := newModernHarpoonCommand(t, requestID, "tools/call", map[string]any{
		"name": "call_target",
		"arguments": map[string]any{
			"label":   targetLabel,
			"method":  "GET",
			"headers": map[string]any{},
		},
	}, func(testing.TB, map[string]any) {})
	callTargetCommand.DeliverAfter = secondaryReady
	callTargetCommand.ExpectedResponses[0].Assert = func(tb testing.TB, resp mocktunnelservice.ReceivedResponse) {
		target := oauthDiscoveryE2ETarget(t, tb)
		if resp.ResponseType != string(wiretypes.ResponsePayloadJSONRPC) {
			target.Fatalf("harpoon call response type mismatch: got %q", resp.ResponseType)
		}
		if resp.ResponseCode != http.StatusOK {
			target.Fatalf("harpoon call response code mismatch: %d", resp.ResponseCode)
		}
		var payload struct {
			Result struct {
				IsError bool `json:"isError"`
				Content []struct {
					Text string `json:"text"`
				} `json:"content"`
			} `json:"result"`
			Error json.RawMessage `json:"error"`
		}
		if err := json.Unmarshal(resp.JSONResponse, &payload); err != nil {
			target.Fatalf("decode harpoon call response: %v", err)
		}
		if len(payload.Error) != 0 {
			target.Fatalf("harpoon call returned JSON-RPC error: %s", payload.Error)
		}
		if !payload.Result.IsError {
			target.Fatalf("dynamic OAuth target unexpectedly remained callable on the second client")
		}
		var textParts []string
		for _, content := range payload.Result.Content {
			textParts = append(textParts, content.Text)
		}
		if !strings.Contains(strings.Join(textParts, "\n"), "unknown target") {
			target.Fatalf("unexpected process-local target rejection: %v", textParts)
		}
	}

	var secondaryClient *harnesspkg.TunnelClient
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
			mocktunnelservice.WithPollWaitLimit(time.Second),
			mocktunnelservice.WithCommandResponses(callTargetCommand),
		),
		harnesspkg.WithAfterClientStart(func(h *harnesspkg.Harness) {
			if h.HarpoonRegistry == nil {
				t.Fatal("primary harpoon registry not populated")
			}
			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer cancel()
			if _, err := h.HarpoonRegistry.WaitForTarget(ctx, targetLabel); err != nil {
				t.Fatalf("wait for primary dynamic OAuth target: %v", err)
			}
			discoveryEnabled.Store(false)
			primaryClient := h.PrimaryClient()
			if err := primaryClient.PausePoller(ctx); err != nil {
				t.Fatalf("pause primary poller: %v", err)
			}
			secondaryClient = h.StartAdditionalClient(t)
			waitForActiveHarpoonPollers(t, h, secondaryClient)
			select {
			case <-secondaryDiscoveryFailed:
			case <-ctx.Done():
				t.Fatal("second client did not attempt disabled OAuth discovery")
			}
			close(secondaryReady)
		}),
	)

	h.ExecuteScenarious(t)

	if got := registrationCalls.Load(); got != 0 {
		t.Fatalf("dynamic registration endpoint calls = %d, want 0", got)
	}
	if got := len(h.ControlPlane.DeliveredCommands()); got != 1 {
		t.Fatalf("delivered process-local OAuth commands = %d, want 1", got)
	}
	if got := len(h.ControlPlane.ReceivedResponses(mocktunnelservice.ResponseMatchMatched)); got != 1 {
		t.Fatalf("matched process-local OAuth responses = %d, want 1", got)
	}
	assertHarpoonResponseAttribution(t, h.ControlPlane.ReceivedHTTPRequests(), map[string]int{
		secondaryClient.Name(): 1,
	})
}

func TestOAuthDiscoveryRejectsOffOriginPrivateMetadataEndpointsE2E(t *testing.T) {
	testCases := []struct {
		name                 string
		issuerMismatch       bool
		poisonResourceAnchor bool
		poisonedLabels       []string
	}{
		{
			name:           "exact_issuer",
			poisonedLabels: []string{"oauth-token-endpoint-0"},
		},
		{
			name:           "issuer_mismatch",
			issuerMismatch: true,
			poisonedLabels: []string{"oauth-issuer-0", "oauth-token-endpoint-0"},
		},
		{
			name:                 "private_prmd_resource_anchor",
			poisonResourceAnchor: true,
			poisonedLabels:       []string{"oauth-prmd-resource-0", "oauth-token-endpoint-0"},
		},
	}

	for _, testCase := range testCases {
		testCase := testCase
		t.Run(testCase.name, func(t *testing.T) {
			const customerHost = "location-mcp.internal.preproduction.smp.bigco-example.com"
			customerBase := "http://" + customerHost
			targetsReady := make(chan struct{})
			privateCalls := make(chan string, 1)

			privateTarget := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				select {
				case privateCalls <- r.Method + " " + r.URL.Path:
				default:
				}
				_, _ = w.Write([]byte("customer-internal-secret"))
			}))
			t.Cleanup(privateTarget.Close)

			metadataIssuer := customerBase
			if testCase.issuerMismatch {
				metadataIssuer = privateTarget.URL + "/issuer"
			}
			resourceURL := customerBase + "/mcp"
			if testCase.poisonResourceAnchor {
				resourceURL = privateTarget.URL + "/mcp"
			}
			upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				switch r.URL.Path {
				case "/.well-known/oauth-protected-resource/mcp", "/.well-known/oauth-protected-resource":
					w.Header().Set("Content-Type", "application/json")
					_ = json.NewEncoder(w).Encode(map[string]any{
						"resource":              resourceURL,
						"authorization_servers": []string{customerBase},
						"scopes_supported":      []string{"mcp:tools"},
					})
				case "/.well-known/oauth-authorization-server":
					w.Header().Set("Content-Type", "application/json")
					_ = json.NewEncoder(w).Encode(map[string]any{
						"issuer":                metadataIssuer,
						"token_endpoint":        privateTarget.URL + "/admin/config",
						"registration_endpoint": customerBase + "/register",
						"revocation_endpoint":   customerBase + "/revoke",
					})
				default:
					http.NotFound(w, r)
				}
			}))
			t.Cleanup(upstream.Close)

			proxy := mockproxy.New(mockproxy.WithRoute(customerHost, mustParseURL(t, upstream.URL)))
			proxy.Start()
			t.Cleanup(proxy.Close)

			discoveryID := "cmd-oauth-off-origin-" + testCase.name
			harpoonInitID := "cmd-harpoon-init-off-origin-" + testCase.name
			harpoonReadyID := "cmd-harpoon-ready-off-origin-" + testCase.name
			harpoonListID := "cmd-harpoon-list-off-origin-" + testCase.name
			harpoonCallID := "cmd-harpoon-call-off-origin-" + testCase.name

			assertResponse := func(expectedType string, expectedCode int) func(testing.TB, mocktunnelservice.ReceivedResponse) {
				return func(tb testing.TB, resp mocktunnelservice.ReceivedResponse) {
					target := oauthDiscoveryE2ETarget(t, tb)
					if resp.ResponseType != expectedType {
						target.Fatalf("response type mismatch: got %q want %q", resp.ResponseType, expectedType)
					}
					if resp.ResponseCode != expectedCode {
						target.Fatalf("response code mismatch: got %d want %d", resp.ResponseCode, expectedCode)
					}
				}
			}

			oauthCommand := mocktunnelservice.CommandResponse{
				Command: mocktunnelservice.NewOAuthDiscoveryCommand(discoveryID, nil),
				ExpectedResponses: []mocktunnelservice.ExpectedResponse{{
					RequestID: discoveryID,
					Assert:    assertResponse(string(wiretypes.ResponsePayloadOAuth), http.StatusOK),
				}},
			}
			harpoonInitialize := mocktunnelservice.CommandResponse{
				Command: newChannelCommand(
					harpoonInitID,
					types.ChannelHarpoon.String(),
					json.RawMessage(`{
						"jsonrpc":"2.0",
						"id":"initialize-harpoon-off-origin",
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
					Assert:    assertResponse(string(wiretypes.ResponsePayloadJSONRPC), http.StatusOK),
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
					Assert:    assertResponse(string(wiretypes.ResponsePayloadNotifyAck), http.StatusOK),
				}},
			}
			harpoonListTargets := mocktunnelservice.CommandResponse{
				Command: newChannelCommand(
					harpoonListID,
					types.ChannelHarpoon.String(),
					json.RawMessage(`{
						"jsonrpc":"2.0",
						"id":"list-targets-off-origin",
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
				DeliverAfter: targetsReady,
				ExpectedResponses: []mocktunnelservice.ExpectedResponse{{
					RequestID: harpoonListID,
					Assert: func(tb testing.TB, resp mocktunnelservice.ReceivedResponse) {
						target := oauthDiscoveryE2ETarget(t, tb)
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
						labels := make(map[string]bool, len(payload.Result.StructuredContent.Targets))
						for _, entry := range payload.Result.StructuredContent.Targets {
							labels[entry.Label] = true
						}
						for _, expected := range []string{"oauth-auth-server-metadata-0", "oauth-registration-endpoint-0"} {
							if !labels[expected] {
								target.Fatalf("harpoon list_targets missing compatibility target %q", expected)
							}
						}
						for _, poisonedLabel := range testCase.poisonedLabels {
							if labels[poisonedLabel] {
								target.Errorf("harpoon list_targets exposed off-origin private target %q", poisonedLabel)
							}
						}
					},
				}},
			}
			harpoonCallPoisonedTarget := mocktunnelservice.CommandResponse{
				Command: newChannelCommand(
					harpoonCallID,
					types.ChannelHarpoon.String(),
					json.RawMessage(`{
						"jsonrpc":"2.0",
						"id":"call-poisoned-target",
						"method":"tools/call",
						"params":{
							"name":"call_target",
							"arguments":{
								"label":"oauth-token-endpoint-0",
								"method":"POST",
								"body":"grant_type=authorization_code"
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
						target := oauthDiscoveryE2ETarget(t, tb)
						if resp.ResponseType != string(wiretypes.ResponsePayloadJSONRPC) {
							target.Fatalf("harpoon call response type mismatch: got %q", resp.ResponseType)
						}
						if resp.ResponseCode != http.StatusOK {
							target.Fatalf("harpoon call response code mismatch: %d", resp.ResponseCode)
						}
						var payload struct {
							Result struct {
								IsError bool `json:"isError"`
								Content []struct {
									Text string `json:"text"`
								} `json:"content"`
							} `json:"result"`
							Error json.RawMessage `json:"error"`
						}
						if err := json.Unmarshal(resp.JSONResponse, &payload); err != nil {
							target.Fatalf("decode harpoon call response: %v", err)
						}
						if len(payload.Error) != 0 {
							target.Fatalf("harpoon call returned JSON-RPC error: %s", payload.Error)
						}
						if !payload.Result.IsError {
							target.Errorf("off-origin private token endpoint remained callable")
							return
						}
						var textParts []string
						for _, content := range payload.Result.Content {
							textParts = append(textParts, content.Text)
						}
						if !strings.Contains(strings.Join(textParts, "\n"), "unknown target") {
							target.Errorf("unexpected rejection for off-origin private target: %v", textParts)
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
					cfg.Harpoon.HostClassifier.IncludeLoopback = true
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
						harpoonCallPoisonedTarget,
					),
				),
				harnesspkg.WithAfterClientStart(func(h *harnesspkg.Harness) {
					t.Helper()
					if h.HarpoonRegistry == nil {
						t.Fatal("harpoon registry not populated")
					}
					go func() {
						defer close(targetsReady)
						ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
						defer cancel()
						// Registration follows token-endpoint in the published bundle, so
						// waiting for it avoids racing list_targets against auto-registration.
						for _, label := range []string{
							"oauth-auth-server-metadata-0",
							"oauth-registration-endpoint-0",
						} {
							if _, err := h.HarpoonRegistry.WaitForTarget(ctx, label); err != nil {
								t.Errorf("wait for harpoon target %q: %v", label, err)
								return
							}
						}
					}()
				}),
			)

			h.ExecuteScenarious(t)

			select {
			case call := <-privateCalls:
				t.Errorf("off-origin private endpoint was called: %s", call)
			default:
			}
		})
	}
}

func TestHarpoonOnlyDisabledMainDoesNotBootstrapOAuthE2E(t *testing.T) {
	requestSeen := make(chan struct{}, 1)
	disabledMain := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		select {
		case requestSeen <- struct{}{}:
		default:
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"resource":"http://127.0.0.1/private"}`))
	}))
	t.Cleanup(disabledMain.Close)

	h := harnesspkg.NewHarness(
		t,
		harnesspkg.WithPreserveClientURLs(),
		harnesspkg.WithClientConfig(func(cfg *config.Config) {
			cfg.Logging.Level = slog.LevelDebug
			cfg.ControlPlane.PollChannelsConfigured = true
			cfg.ControlPlane.PollChannels = []types.Channel{types.ChannelHarpoon}
			cfg.MCP.AllowNoMain = true
			cfg.MCP.TransportKind = config.MCPTransportHTTPStreamable
			cfg.MCP.ServerURL = mustParseURL(t, disabledMain.URL+"/mcp")
			cfg.MCP.ChannelBindings = []config.MCPChannelBinding{{
				Channel:       types.DefaultChannel,
				TransportKind: config.MCPTransportHTTPStreamable,
				ServerURL:     cfg.MCP.ServerURL,
			}}
			cfg.Harpoon.AllowPlaintextHTTP = true
			cfg.Harpoon.Targets = []config.HarpoonTarget{{
				Label:       "seed",
				Description: "seed target for routable harpoon channel",
				BaseURL:     mustParseURL(t, disabledMain.URL+"/seed"),
			}}
		}),
		harnesspkg.WithAfterClientStart(func(h *harnesspkg.Harness) {
			if h.OAuthState == nil {
				t.Fatal("OAuth discovery state not populated")
			}
			_, _, _, err, done := h.OAuthState.Wait(time.Second)
			if !done {
				t.Fatal("OAuth discovery did not settle")
			}
			if err != nil {
				t.Fatalf("disabled OAuth discovery returned error: %v", err)
			}
			select {
			case <-requestSeen:
				t.Fatal("disabled main MCP endpoint received an OAuth discovery request")
			default:
			}
			if h.HarpoonRegistry == nil {
				t.Fatal("harpoon registry not populated")
			}
			if _, ok := h.HarpoonRegistry.Lookup("seed"); !ok {
				t.Fatal("configured Harpoon seed target was not registered")
			}
			if _, ok := h.HarpoonRegistry.Lookup("oauth-prmd-resource-0"); ok {
				t.Fatal("disabled main registered a dynamic OAuth target")
			}
		}),
	)

	h.ExecuteScenarious(t)
}

func oauthDiscoveryE2ETarget(t *testing.T, tb testing.TB) testing.TB {
	if tb != nil {
		tb.Helper()
		return tb
	}
	t.Helper()
	return t
}
