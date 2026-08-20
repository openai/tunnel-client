package e2e_test

import (
	"encoding/json"
	"log/slog"
	"net/http"
	"testing"

	"github.com/openai/tunnel-client/pkg/config"
	"github.com/openai/tunnel-client/pkg/controlplane/wiretypes"
	harnesspkg "github.com/openai/tunnel-client/testsupport/e2e"
	"github.com/openai/tunnel-client/testsupport/mockmcpserver"
	"github.com/openai/tunnel-client/testsupport/mocktunnelservice"
)

func TestNotificationsAreDeliveredToControlPlane(t *testing.T) {
	const (
		toolRequestID = "cmd-notify-tool"
		callID        = "notify-call-1"
	)

	toolCommand := mocktunnelservice.CommandResponse{
		Command: mocktunnelservice.NewCommand(
			toolRequestID,
			json.RawMessage(`{
				"jsonrpc":"2.0",
				"id":"`+callID+`",
				"method":"tools/call",
				"params":{
					"name":"echo",
					"arguments":{"name":"Notifications"}
				}
			}`),
			http.Header{
				"Accept":       []string{"application/json, text/event-stream"},
				"Content-Type": []string{"application/json"},
			},
		),
		ExpectedResponses: []mocktunnelservice.ExpectedResponse{
			{
				RequestID: toolRequestID,
				Assert: func(tb testing.TB, resp mocktunnelservice.ReceivedResponse) {
					if tb != nil {
						tb.Helper()
					}
					target := tb
					if target == nil {
						target = t
					}
					if resp.ResponseType != string(wiretypes.ResponsePayloadJSONRPCNotify) {
						target.Fatalf("expected first response to be notification, got %q", resp.ResponseType)
					}
					if resp.ResponseCode != http.StatusOK {
						target.Fatalf("notification status code = %d", resp.ResponseCode)
					}
					var msg struct {
						Method string `json:"method"`
						Params struct {
							Message string `json:"message"`
						} `json:"params"`
					}
					if err := json.Unmarshal(resp.JSONResponse, &msg); err != nil {
						target.Fatalf("decode notification payload: %v", err)
					}
					if msg.Method != "notifications/progress" {
						target.Fatalf("unexpected notification method %q", msg.Method)
					}
					if msg.Params.Message != "quarter" {
						target.Fatalf("unexpected notification message %q", msg.Params.Message)
					}
					if resp.ResponseHeaders.Get("Content-Type") == "" {
						target.Fatalf("notification missing Content-Type header")
					}
				},
			},
			{
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
						target.Fatalf("final response type mismatch: got %q", resp.ResponseType)
					}
					if resp.ResponseCode != http.StatusOK {
						target.Fatalf("final response status code = %d", resp.ResponseCode)
					}
					var finalPayload struct {
						Result struct {
							StructuredContent map[string]any `json:"structuredContent"`
							Message           string         `json:"message"`
						} `json:"result"`
					}
					if err := json.Unmarshal(resp.JSONResponse, &finalPayload); err != nil {
						target.Fatalf("decode final response: %v", err)
					}
					finalMsg := finalPayload.Result.Message
					if msg, ok := finalPayload.Result.StructuredContent["message"].(string); ok {
						finalMsg = msg
					}
					if finalMsg != "done" {
						target.Fatalf("unexpected final message %q", finalMsg)
					}
				},
			},
		},
	}

	h := harnesspkg.NewHarness(
		t,
		harnesspkg.WithClientConfig(func(cfg *config.Config) {
			cfg.Logging.Level = slog.LevelDebug
		}),
		harnesspkg.WithControlPlaneOptions(
			mocktunnelservice.WithSessionHeaderPropagation(),
			mocktunnelservice.WithInitializationPhaseCommands(),
			mocktunnelservice.WithCommandResponses(toolCommand),
		),
		harnesspkg.WithMCPOptions(
			mockmcpserver.WithCalls(
				mockmcpserver.Call{
					Tool: "echo",
					Progress: []mockmcpserver.ProgressUpdate{
						{Percentage: 0.25, Message: "quarter"},
					},
					Result: json.RawMessage(`{"message":"done"}`),
				},
			),
		),
	)

	h.ExecuteScenarious(t)

	responses := h.ControlPlane.ReceivedResponses(mocktunnelservice.ResponseMatchAll)

	var notifs []mocktunnelservice.ReceivedResponse
	var finals []mocktunnelservice.ReceivedResponse
	for _, resp := range responses {
		if resp.RequestID != toolRequestID {
			continue
		}
		switch resp.ResponseType {
		case string(wiretypes.ResponsePayloadJSONRPCNotify):
			notifs = append(notifs, resp)
		case string(wiretypes.ResponsePayloadJSONRPC):
			finals = append(finals, resp)
		}
	}

	if len(notifs) != 1 {
		t.Fatalf("expected one notification for %s, got %d", toolRequestID, len(notifs))
	}
	if len(finals) != 1 {
		t.Fatalf("expected one final JSON-RPC response for %s, got %d", toolRequestID, len(finals))
	}
	if indexOf(responses, notifs[0]) >= indexOf(responses, finals[0]) {
		t.Fatalf("expected notifications to arrive before final response")
	}

	expectedMsgs := map[string]bool{"quarter": false}
	for _, resp := range notifs {
		var msg struct {
			Method string `json:"method"`
			Params struct {
				Message string `json:"message"`
			} `json:"params"`
		}
		if err := json.Unmarshal(resp.JSONResponse, &msg); err != nil {
			t.Fatalf("decode notification payload: %v", err)
		}
		if msg.Method != "notifications/progress" {
			t.Fatalf("unexpected notification method %q", msg.Method)
		}
		if resp.ResponseHeaders.Get("Content-Type") == "" {
			t.Fatalf("notification missing Content-Type header")
		}
		if _, ok := expectedMsgs[msg.Params.Message]; ok {
			expectedMsgs[msg.Params.Message] = true
		}
	}
	for m, seen := range expectedMsgs {
		if !seen {
			t.Fatalf("missing notification message %q", m)
		}
	}

	var finalPayload struct {
		Result struct {
			StructuredContent map[string]any `json:"structuredContent"`
			Message           string         `json:"message"`
		} `json:"result"`
	}
	if err := json.Unmarshal(finals[0].JSONResponse, &finalPayload); err != nil {
		t.Fatalf("decode final response: %v", err)
	}
	finalMsg := finalPayload.Result.Message
	if msg, ok := finalPayload.Result.StructuredContent["message"].(string); ok {
		finalMsg = msg
	}
	if finalMsg != "done" {
		t.Fatalf("unexpected final message %q", finalMsg)
	}
}

func indexOf(responses []mocktunnelservice.ReceivedResponse, target mocktunnelservice.ReceivedResponse) int {
	for i, r := range responses {
		if r.RequestID == target.RequestID &&
			r.ResponseType == target.ResponseType &&
			r.ResponseCode == target.ResponseCode &&
			string(r.JSONResponse) == string(target.JSONResponse) {
			return i
		}
	}
	return -1
}
