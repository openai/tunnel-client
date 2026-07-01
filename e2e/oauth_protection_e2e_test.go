package e2e_test

import (
	"encoding/json"
	"log/slog"
	"net/http"
	"strings"
	"testing"

	"go.openai.org/api/tunnel-client/pkg/config"
	"go.openai.org/api/tunnel-client/pkg/controlplane/wiretypes"
	harnesspkg "go.openai.org/api/tunnel-client/testsupport/e2e"
	"go.openai.org/api/tunnel-client/testsupport/mockmcpserver"
	"go.openai.org/api/tunnel-client/testsupport/mocktunnelservice"
)

func TestSecureMCPServerOAuthProtection(t *testing.T) {

	const (
		apiKey        = "sk-1234567890abcdef"
		initUnauthID  = "init-unauth"
		discoveryID   = "oauth-discovery"
		initAuthID    = "init-auth"
		toolRequestID = "cmd-secure-tool"
		toolCallID    = "secure-call-1"
		toolName      = "echo"
		userName      = "SecureUser"
	)

	initHeaders := http.Header{
		"Accept":       []string{"application/json, text/event-stream"},
		"Content-Type": []string{"application/json"},
	}

	unauthorizedInit := mocktunnelservice.CommandResponse{
		Command: mocktunnelservice.NewCommand(
			initUnauthID,
			json.RawMessage(`{
				"jsonrpc":"2.0",
				"id":"`+initUnauthID+`",
				"method":"initialize",
				"params":{"protocolVersion":"2025-06-18"}
			}`),
			initHeaders,
		),
		ExpectedResponses: []mocktunnelservice.ExpectedResponse{{
			RequestID: initUnauthID,
			Assert: func(tb testing.TB, resp mocktunnelservice.ReceivedResponse) {
				if tb != nil {
					tb.Helper()
				}
				target := tb
				if target == nil {
					target = t
				}
				if resp.ResponseType != string(wiretypes.ResponsePayloadJSONRPC) {
					target.Fatalf("unauthorized init resp_type = %q", resp.ResponseType)
				}
				if resp.ResponseCode != http.StatusUnauthorized {
					target.Fatalf("unauthorized init status = %d", resp.ResponseCode)
				}
				if len(resp.JSONResponse) == 0 {
					target.Fatalf("unauthorized init missing resp_json payload")
				}
				challenge := resp.ResponseHeaders.Get("WWW-Authenticate")
				if challenge == "" {
					target.Fatalf("unauthorized init missing WWW-Authenticate challenge: %v", resp.ResponseHeaders)
				}
				if !strings.Contains(challenge, "resource_metadata") {
					target.Fatalf("unauthorized init WWW-Authenticate missing resource_metadata: %q", challenge)
				}
			},
		}},
	}

	oauthDiscovery := mocktunnelservice.CommandResponse{
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
					target.Fatalf("oauth discovery resp_type = %q", resp.ResponseType)
				}
				if resp.ResponseCode != http.StatusOK {
					target.Fatalf("oauth discovery status = %d", resp.ResponseCode)
				}
				if len(resp.JSONResponse) == 0 {
					target.Fatalf("oauth discovery missing payload")
				}
			},
		}},
	}

	authHeaders := initHeaders.Clone()
	authHeaders.Set("Authorization", "Bearer "+apiKey)

	authorizedInit := mocktunnelservice.CommandResponse{
		Command: mocktunnelservice.NewCommand(
			initAuthID,
			json.RawMessage(`{
				"jsonrpc":"2.0",
				"id":"`+initAuthID+`",
				"method":"initialize",
				"params":{"protocolVersion":"2025-06-18"}
			}`),
			authHeaders,
		),
		ExpectedResponses: []mocktunnelservice.ExpectedResponse{{
			RequestID: initAuthID,
			Assert: func(tb testing.TB, resp mocktunnelservice.ReceivedResponse) {
				if tb != nil {
					tb.Helper()
				}
				target := tb
				if target == nil {
					target = t
				}
				if resp.ResponseType != string(wiretypes.ResponsePayloadJSONRPC) {
					target.Fatalf("authorized init resp_type = %q", resp.ResponseType)
				}
				if resp.ResponseCode != http.StatusOK {
					target.Fatalf("authorized init status = %d", resp.ResponseCode)
				}
			},
		}},
	}

	toolHeaders := authHeaders.Clone()

	toolCommand := mocktunnelservice.CommandResponse{
		Command: mocktunnelservice.NewCommand(
			toolRequestID,
			json.RawMessage(`{
				"jsonrpc":"2.0",
				"id":"`+toolCallID+`",
				"method":"tools/call",
				"params":{
					"name":"`+toolName+`",
					"arguments":{"name":"`+userName+`"}
				}
			}`),
			toolHeaders,
		),
		ExpectedResponses: []mocktunnelservice.ExpectedResponse{{
			RequestID: toolRequestID,
			Assert: func(tb testing.TB, resp mocktunnelservice.ReceivedResponse) {
				if tb != nil {
					tb.Helper()
				}
				target := tb
				if target == nil {
					target = t
				}
				if resp.ResponseType != string(wiretypes.ResponsePayloadJSONRPC) {
					target.Fatalf("tool resp_type = %q", resp.ResponseType)
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
		}),
		harnesspkg.WithControlPlaneOptions(
			mocktunnelservice.WithSessionHeaderPropagation(),
			mocktunnelservice.WithCommandResponses(
				unauthorizedInit,
				oauthDiscovery,
				authorizedInit,
				toolCommand,
			),
		),
		harnesspkg.WithMCPOptions(
			mockmcpserver.WithOAuthProtection(),
			mockmcpserver.WithCalls(
				mockmcpserver.Call{
					Tool: toolName,
					DynamicResult: func(arguments json.RawMessage) (json.RawMessage, error) {
						var payload struct {
							Name string `json:"name"`
						}
						if err := json.Unmarshal(arguments, &payload); err != nil {
							return nil, err
						}
						return json.RawMessage(`{"message":"hi, ` + payload.Name + `!"}`), nil
					},
				},
			),
		),
	)

	h.ExecuteScenarious(t)

	matched := h.ControlPlane.ReceivedResponses(mocktunnelservice.ResponseMatchMatched)
	if len(matched) != 4 {
		t.Fatalf("expected four matched responses; got %d", len(matched))
	}

	if matched[0].RequestID != initUnauthID || matched[0].ResponseCode != http.StatusUnauthorized {
		t.Fatalf("first response mismatch: %+v", matched[0])
	}
	if matched[1].RequestID != discoveryID || matched[1].ResponseCode != http.StatusOK {
		t.Fatalf("discovery response mismatch: %+v", matched[1])
	}
	if matched[2].RequestID != initAuthID || matched[2].ResponseCode != http.StatusOK {
		t.Fatalf("authorized init response mismatch: %+v", matched[2])
	}
	if matched[3].RequestID != toolRequestID || matched[3].ResponseCode != http.StatusOK {
		t.Fatalf("tool response mismatch: %+v", matched[3])
	}

	delivered := h.ControlPlane.DeliveredCommands()
	if len(delivered) != 4 {
		t.Fatalf("expected four delivered commands; got %d", len(delivered))
	}

	mcpHTTPRequests := h.MCP.ReceivedHTTPRequests()
	assertRecordedMCPAuthorization(t, mcpHTTPRequests, initUnauthID, "")
	assertRecordedMCPAuthorization(t, mcpHTTPRequests, initAuthID, "Bearer "+apiKey)
	assertRecordedMCPAuthorization(t, mcpHTTPRequests, toolCallID, "Bearer "+apiKey)

	recorded := h.MCP.ReceivedRequests()
	if len(recorded) != 1 {
		t.Fatalf("expected single tool invocation on MCP server, got %d", len(recorded))
	}
	if recorded[0].Tool != toolName {
		t.Fatalf("expected tool %s, got %s", toolName, recorded[0].Tool)
	}
	if !strings.Contains(string(recorded[0].Arguments), userName) {
		t.Fatalf("unexpected tool arguments: %s", string(recorded[0].Arguments))
	}
	if got := recorded[0].Headers.Get("Authorization"); got != "Bearer "+apiKey {
		t.Fatalf("tool handler Authorization header = %q, want bearer token forwarded from connector command", got)
	}
}

func assertRecordedMCPAuthorization(t *testing.T, requests []mockmcpserver.IncomingHTTPRequest, bodyNeedle, want string) {
	t.Helper()
	for _, req := range requests {
		if req.Method != http.MethodPost || !strings.Contains(string(req.Body), bodyNeedle) {
			continue
		}
		if got := req.Headers.Get("Authorization"); got != want {
			t.Fatalf("Authorization header for MCP request containing %q = %q, want %q", bodyNeedle, got, want)
		}
		return
	}
	t.Fatalf("missing recorded MCP POST request containing %q; saw %d requests", bodyNeedle, len(requests))
}
