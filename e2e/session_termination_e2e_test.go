package e2e_test

import (
	"encoding/json"
	"fmt"
	"net/http"
	"testing"

	"github.com/openai/tunnel-client/pkg/controlplane/wiretypes"
	"github.com/openai/tunnel-client/pkg/mcpclient"
	harnesspkg "github.com/openai/tunnel-client/testsupport/e2e"
	"github.com/openai/tunnel-client/testsupport/mockmcpserver"
	"github.com/openai/tunnel-client/testsupport/mocktunnelservice"
)

func TestHarnessHandlesSessionTerminationCommand(t *testing.T) {
	const (
		requestID     = "cmd-session-termination"
		connectorAuth = "Bearer connector-session-close"
	)

	sessionTermination := mocktunnelservice.CommandResponse{
		Command: mocktunnelservice.NewSessionTerminationCommand(requestID, http.Header{
			"Authorization":                 {connectorAuth},
			mcpclient.HeaderProtocolVersion: {"2025-06-18"},
		}),
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
				if resp.ResponseType != string(wiretypes.ResponsePayloadSessionTermination) {
					target.Fatalf("session termination response type mismatch: got %q", resp.ResponseType)
				}
				if resp.ResponseCode != http.StatusNoContent {
					target.Fatalf("session termination response code mismatch: %d", resp.ResponseCode)
				}
				if len(resp.JSONResponse) != 0 {
					target.Fatalf("session termination response should not include resp_json payload")
				}
			},
		}},
	}

	h := harnesspkg.NewHarness(
		t,
		harnesspkg.WithControlPlaneOptions(
			mocktunnelservice.WithSessionHeaderPropagation(),
			mocktunnelservice.WithInitializationPhaseCommands(),
			mocktunnelservice.WithCommandResponses(sessionTermination),
		),
		harnesspkg.WithBeforeClientStop(func(h *harnesspkg.Harness) {
			assertSessionTerminationForwardedConnectorHeaders(t, h.MCP, connectorAuth)
		}),
	)
	h.ExecuteScenarious(t)
}

func assertSessionTerminationForwardedConnectorHeaders(t *testing.T, mcp *mockmcpserver.MockMCPServer, wantAuthorization string) {
	t.Helper()

	var matchingDeletes []mockmcpserver.IncomingHTTPRequest
	for _, req := range mcp.ReceivedHTTPRequests() {
		if req.Method == http.MethodDelete && req.Headers.Get("Authorization") == wantAuthorization {
			matchingDeletes = append(matchingDeletes, req)
		}
	}
	if len(matchingDeletes) != 1 {
		t.Fatalf("expected one MCP session DELETE request with connector Authorization, got %d", len(matchingDeletes))
	}
	deleteReq := matchingDeletes[0]
	if got := deleteReq.Headers.Get(mcpclient.HeaderSessionID); got == "" {
		t.Fatalf("session DELETE missing %s header: %v", mcpclient.HeaderSessionID, deleteReq.Headers)
	}
	if got := deleteReq.Headers.Get(mcpclient.HeaderProtocolVersion); got != "2025-06-18" {
		t.Fatalf("session DELETE %s = %q, want %q", mcpclient.HeaderProtocolVersion, got, "2025-06-18")
	}
}

func TestHarnessRejectsSessionTerminationForStdioAndKeepsServing(t *testing.T) {
	commandArgs := mockmcpserver.StdioServerCommand(t)

	const (
		sessionTerminationRequestID = "cmd-session-termination"
		toolRequestID               = "cmd-tool-after-session-termination"
		callID                      = "tool-after-session-termination"
		userName                    = "Ada"
		sessionID                   = "stdio-session"
	)

	sessionTermination := mocktunnelservice.CommandResponse{
		Command: mocktunnelservice.NewSessionTerminationCommand(
			sessionTerminationRequestID,
			http.Header{"Mcp-Session-Id": {sessionID}},
		),
		ExpectedResponses: []mocktunnelservice.ExpectedResponse{{
			RequestID: sessionTerminationRequestID,
			Assert: func(tb testing.TB, resp mocktunnelservice.ReceivedResponse) {
				if tb != nil {
					tb.Helper()
				}
				target := tb
				if target == nil {
					target = t
				}
				if resp.ResponseType != string(wiretypes.ResponsePayloadSessionTermination) {
					target.Fatalf("session termination response type mismatch: got %q", resp.ResponseType)
				}
				if resp.ResponseCode != http.StatusMethodNotAllowed {
					target.Fatalf("session termination response code mismatch: got %d want %d", resp.ResponseCode, http.StatusMethodNotAllowed)
				}
			},
		}},
	}
	toolCommand := mocktunnelservice.CommandResponse{
		Command: mocktunnelservice.NewCommand(
			toolRequestID,
			json.RawMessage(`{
				"jsonrpc":"2.0",
				"id":"`+callID+`",
				"method":"tools/call",
				"params":{
					"name":"echo",
					"arguments":{
						"name":"`+userName+`"
					}
				}
			}`),
			nil,
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
					target.Fatalf("tool call response type mismatch: got %q", resp.ResponseType)
				}
				if resp.ResponseCode != http.StatusOK {
					target.Fatalf("tool call response code mismatch: %d", resp.ResponseCode)
				}
				if len(resp.JSONResponse) == 0 {
					target.Fatalf("tool call missing resp_json payload")
				}
			},
		}},
	}

	h := harnesspkg.NewHarness(
		t,
		harnesspkg.WithMCPCommand(commandArgs),
		harnesspkg.WithControlPlaneOptions(
			mocktunnelservice.WithInitializationPhaseCommandsWithoutSessionHeaders(),
			mocktunnelservice.WithCommandResponses(sessionTermination, toolCommand),
		),
	)
	h.ExecuteScenarious(t)

	matched := h.ControlPlane.ReceivedResponses(mocktunnelservice.ResponseMatchMatched)
	if len(matched) != 4 {
		t.Fatalf("expected four matched responses (initialize, initialized, delete, tool); got %d", len(matched))
	}
	var toolResponse mocktunnelservice.ReceivedResponse
	for _, resp := range matched {
		if resp.RequestID == toolRequestID {
			toolResponse = resp
			break
		}
	}
	if toolResponse.RequestID == "" {
		t.Fatalf("tool response for %s not recorded", toolRequestID)
	}
	var rpcPayload struct {
		Result struct {
			StructuredContent map[string]any `json:"structuredContent"`
		} `json:"result"`
	}
	if err := json.Unmarshal(toolResponse.JSONResponse, &rpcPayload); err != nil {
		t.Fatalf("decode tool response payload: %v", err)
	}
	msg, _ := rpcPayload.Result.StructuredContent["message"].(string)
	expectedMessage := fmt.Sprintf("hello %s", userName)
	if msg != expectedMessage {
		t.Fatalf("unexpected tool response message: got %q want %q", msg, expectedMessage)
	}
}
