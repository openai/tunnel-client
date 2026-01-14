package e2e_test

import (
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"testing"
	"time"

	"go.openai.org/api/tunnel-client/pkg/config"
	"go.openai.org/api/tunnel-client/pkg/controlplane/wiretypes"
	harnesspkg "go.openai.org/api/tunnel-client/testsupport/e2e"
	"go.openai.org/api/tunnel-client/testsupport/mockmcpserver"
	"go.openai.org/api/tunnel-client/testsupport/mocktunnelservice"
)

func TestHarnessExecuteScenariousWithInitializationAndTool(t *testing.T) {
	runSimpleToolScenario(t)
}

func TestHarnessHandlesKeepalivePingEvents(t *testing.T) {
	runSimpleToolScenarioWithHarnessOptions(
		t,
		[]harnesspkg.HarnessOption{
			harnesspkg.WithScenarioTimeout(10 * time.Second),
			harnesspkg.WithClientConfig(func(cfg *config.Config) {
				// Give the control-plane poller more headroom so the initial poll
				// (which dequeues initialize) does not time out under load before
				// headers arrive, which would strand the script.
				cfg.ControlPlane.PollTimeout = time.Second
			}),
		},
		mockmcpserver.WithKeepalivePings(),
	)
}

func runSimpleToolScenario(t *testing.T, mcpOptions ...mockmcpserver.Option) {
	runSimpleToolScenarioWithHarnessOptions(t, nil, mcpOptions...)
}

func runSimpleToolScenarioWithHarnessOptions(
	t *testing.T,
	harnessOptions []harnesspkg.HarnessOption,
	mcpOptions ...mockmcpserver.Option,
) {
	t.Helper()

	const (
		toolRequestID = "cmd-tool"
		callID        = "tool-1"
		userName      = "Ada"
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

	mcpOpts := []mockmcpserver.Option{
		mockmcpserver.WithCalls(
			mockmcpserver.Call{
				Tool: "echo",
				DynamicResult: func(arguments json.RawMessage) (json.RawMessage, error) {
					var payload struct {
						Name string `json:"name"`
					}
					if err := json.Unmarshal(arguments, &payload); err != nil {
						return nil, err
					}
					return json.RawMessage(fmt.Sprintf(`{"message":"hi, %s!"}`, payload.Name)), nil
				},
			},
		),
	}
	mcpOpts = append(mcpOpts, mcpOptions...)

	options := []harnesspkg.HarnessOption{
		harnesspkg.WithClientConfig(func(cfg *config.Config) {
			cfg.Logging.Level = slog.LevelDebug
		}),
		harnesspkg.WithControlPlaneOptions(
			mocktunnelservice.WithSessionHeaderPropagation(),
			mocktunnelservice.WithInitializationPhaseCommands(),
			mocktunnelservice.WithCommandResponses(toolCommand),
		),
		harnesspkg.WithMCPOptions(mcpOpts...),
	}
	if len(harnessOptions) > 0 {
		options = append(options, harnessOptions...)
	}

	h := harnesspkg.NewHarness(t, options...)

	h.ExecuteScenarious(t)

	matched := h.ControlPlane.ReceivedResponses(mocktunnelservice.ResponseMatchMatched)
	if len(matched) != 3 {
		t.Fatalf("expected three matched responses (initialize, initialized, tool); got %d", len(matched))
	}
	delivered := h.ControlPlane.DeliveredCommands()
	if len(delivered) != 3 {
		t.Fatalf("expected three delivered commands; got %d", len(delivered))
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
	expectedMessage := fmt.Sprintf("hi, %s!", userName)
	if msg != expectedMessage {
		t.Fatalf("unexpected tool response message: got %q want %q", msg, expectedMessage)
	}

	recorded := h.MCP.ReceivedRequests()
	if len(recorded) != 1 {
		t.Fatalf("expected single tool invocation on MCP server, got %d", len(recorded))
	}
	if recorded[0].Tool != "echo" {
		t.Fatalf("expected tool name echo, got %s", recorded[0].Tool)
	}
	var args map[string]any
	if err := json.Unmarshal(recorded[0].Arguments, &args); err != nil {
		t.Fatalf("failed to decode tool arguments: %v", err)
	}
	if args["name"] != userName {
		t.Fatalf("unexpected tool arguments: %v", args)
	}
}
