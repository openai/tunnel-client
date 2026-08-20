package e2e_test

import (
	"encoding/json"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/openai/tunnel-client/pkg/config"
	"github.com/openai/tunnel-client/pkg/controlplane/wiretypes"
	harnesspkg "github.com/openai/tunnel-client/testsupport/e2e"
	"github.com/openai/tunnel-client/testsupport/mocktunnelservice"
)

func TestHarpoonChannelInitializeThenToolsList(t *testing.T) {
	const (
		channel              = "harpoon"
		initializeCommandID  = "cmd-harpoon-init"
		initializedCommandID = "cmd-harpoon-initialized"
		toolsListCommandID   = "cmd-harpoon-tools-list"
	)

	targetServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"ok":true}`))
	}))
	defer targetServer.Close()
	targetURL := mustParseURL(t, targetServer.URL)

	initializeCommand := mocktunnelservice.CommandResponse{
		Command: newChannelCommand(
			initializeCommandID,
			channel,
			json.RawMessage(`{
				"jsonrpc":"2.0",
				"id":"initialize-harpoon-0",
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
			RequestID: initializeCommandID,
			Assert: func(tb testing.TB, resp mocktunnelservice.ReceivedResponse) {
				if tb != nil {
					tb.Helper()
				}
				target := tb
				if target == nil {
					target = t
				}
				if resp.ResponseType != string(wiretypes.ResponsePayloadJSONRPC) {
					target.Fatalf("initialize response type mismatch: got %q", resp.ResponseType)
				}
				if resp.ResponseCode != http.StatusOK {
					target.Fatalf("initialize response code mismatch: got %d", resp.ResponseCode)
				}
				if len(resp.JSONResponse) == 0 {
					target.Fatalf("initialize response missing resp_json payload")
				}
				var payload struct {
					Result struct {
						ServerInfo struct {
							Name string `json:"name"`
						} `json:"serverInfo"`
					} `json:"result"`
				}
				if err := json.Unmarshal(resp.JSONResponse, &payload); err != nil {
					target.Fatalf("decode initialize response payload: %v", err)
				}
				if payload.Result.ServerInfo.Name != "harpoon" {
					target.Fatalf(
						"initialize server info mismatch: got %q want %q",
						payload.Result.ServerInfo.Name,
						"harpoon",
					)
				}
			},
		}},
	}

	initializedCommand := mocktunnelservice.CommandResponse{
		Command: newChannelCommand(
			initializedCommandID,
			channel,
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
			RequestID: initializedCommandID,
			Assert: func(tb testing.TB, resp mocktunnelservice.ReceivedResponse) {
				if tb != nil {
					tb.Helper()
				}
				target := tb
				if target == nil {
					target = t
				}
				if resp.ResponseType != string(wiretypes.ResponsePayloadNotifyAck) {
					target.Fatalf("initialized ack type mismatch: got %q", resp.ResponseType)
				}
				if resp.ResponseCode != http.StatusOK {
					target.Fatalf("initialized ack code mismatch: got %d", resp.ResponseCode)
				}
			},
		}},
	}

	toolsListCommand := mocktunnelservice.CommandResponse{
		Command: newChannelCommand(
			toolsListCommandID,
			channel,
			json.RawMessage(`{
				"jsonrpc":"2.0",
				"id":"tools-list-harpoon-1",
				"method":"tools/list",
				"params":{}
			}`),
			http.Header{
				"Accept":       []string{"application/json"},
				"Content-Type": []string{"application/json"},
			},
		),
		ExpectedResponses: []mocktunnelservice.ExpectedResponse{{
			RequestID: toolsListCommandID,
			Assert: func(tb testing.TB, resp mocktunnelservice.ReceivedResponse) {
				if tb != nil {
					tb.Helper()
				}
				target := tb
				if target == nil {
					target = t
				}
				if resp.ResponseType != string(wiretypes.ResponsePayloadJSONRPC) {
					target.Fatalf("tools/list response type mismatch: got %q", resp.ResponseType)
				}
				if resp.ResponseCode != http.StatusOK {
					target.Fatalf("tools/list response code mismatch: got %d", resp.ResponseCode)
				}
				if len(resp.JSONResponse) == 0 {
					target.Fatalf("tools/list response missing resp_json payload")
				}
				var payload struct {
					Result struct {
						Tools []struct {
							Name string `json:"name"`
						} `json:"tools"`
					} `json:"result"`
				}
				if err := json.Unmarshal(resp.JSONResponse, &payload); err != nil {
					target.Fatalf("decode tools/list response payload: %v", err)
				}
				toolNames := make(map[string]bool, len(payload.Result.Tools))
				for _, tool := range payload.Result.Tools {
					toolNames[tool.Name] = true
				}
				if !toolNames["list_targets"] {
					target.Fatalf("tools/list missing list_targets tool")
				}
				if !toolNames["call_target"] {
					target.Fatalf("tools/list missing call_target tool")
				}
			},
		}},
	}

	h := harnesspkg.NewHarness(
		t,
		harnesspkg.WithClientConfig(func(cfg *config.Config) {
			cfg.Logging.Level = slog.LevelDebug
			cfg.Harpoon.AllowPlaintextHTTP = true
			cfg.Harpoon.Targets = []config.HarpoonTarget{{
				Label:       "seed",
				Description: "seed target for routable harpoon channel",
				BaseURL:     targetURL,
			}}
		}),
		harnesspkg.WithControlPlaneOptions(
			mocktunnelservice.WithCommandResponses(
				initializeCommand,
				initializedCommand,
				toolsListCommand,
			),
		),
	)

	h.ExecuteScenarious(t)

	matched := h.ControlPlane.ReceivedResponses(mocktunnelservice.ResponseMatchMatched)
	if len(matched) != 3 {
		t.Fatalf("expected three matched responses (initialize, initialized, tools/list); got %d", len(matched))
	}
	delivered := h.ControlPlane.DeliveredCommands()
	if len(delivered) != 3 {
		t.Fatalf("expected three delivered commands; got %d", len(delivered))
	}
}

func newChannelCommand(
	requestID string,
	channel string,
	jsonrpcPayload json.RawMessage,
	headers http.Header,
) json.RawMessage {
	command := map[string]any{
		"command_type": "jsonrpc",
		"request_id":   requestID,
		"jsonrpc":      jsonrpcPayload,
		"created_at":   time.Now().UTC().Format(time.RFC3339),
		"shard_token":  requestID,
		"channel":      channel,
	}
	if headers != nil {
		command["headers"] = headers
	}
	data, _ := json.Marshal(command)
	return json.RawMessage(data)
}
