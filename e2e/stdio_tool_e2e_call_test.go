package e2e_test

import (
	"bytes"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/openai/tunnel-client/pkg/config"
	"github.com/openai/tunnel-client/pkg/controlplane/wiretypes"
	harnesspkg "github.com/openai/tunnel-client/testsupport/e2e"
	"github.com/openai/tunnel-client/testsupport/mockmcpserver"
	"github.com/openai/tunnel-client/testsupport/mocktunnelservice"
)

func TestHarnessExecuteScenarioWithStdioCommand(t *testing.T) {
	t.Parallel()

	commandArgs := mockmcpserver.StdioServerCommand(t)
	runSimpleToolScenarioWithCommand(t, commandArgs)
}

func TestHarnessStdioLegacyDiscoveryFallbackAcrossReconnects(t *testing.T) {
	t.Parallel()

	for _, code := range []int{-32601, -32602} {
		t.Run(fmt.Sprintf("error_%d", code), func(t *testing.T) {
			t.Parallel()

			var commands []mocktunnelservice.CommandResponse
			var wantMessages []json.RawMessage
			addCommand := func(method string, rpcID any, params any, wantMessage string) {
				request := map[string]any{"jsonrpc": "2.0", "method": method, "params": params}
				if rpcID != nil {
					request["id"] = rpcID
				}
				payload, err := json.Marshal(request)
				require.NoError(t, err)
				wantMessages = append(wantMessages, payload)
				requestID := fmt.Sprintf("legacy-discovery-%d", len(commands))
				commands = append(commands, mocktunnelservice.CommandResponse{
					Command: mocktunnelservice.NewCommand(requestID, payload, nil),
					ExpectedResponses: []mocktunnelservice.ExpectedResponse{{
						RequestID: requestID,
						Assert: func(tb testing.TB, response mocktunnelservice.ReceivedResponse) {
							require.Equal(tb, http.StatusOK, response.ResponseCode, "%s: %s", method, response.JSONResponse)
							if rpcID == nil {
								require.Equal(tb, string(wiretypes.ResponsePayloadNotifyAck), response.ResponseType)
								return
							}
							require.Equal(tb, string(wiretypes.ResponsePayloadJSONRPC), response.ResponseType)
							if method == "server/discover" {
								want, err := json.Marshal(map[string]any{
									"jsonrpc": "2.0", "id": rpcID,
									"error": map[string]any{"code": code, "message": "legacy discovery unsupported", "data": map[string]any{"transport": "stdio"}},
								})
								require.NoError(tb, err)
								require.JSONEq(tb, string(want), string(response.JSONResponse))
								return
							}
							var envelope struct {
								ID     json.RawMessage `json:"id"`
								Error  json.RawMessage `json:"error"`
								Result struct {
									ProtocolVersion string `json:"protocolVersion"`
									Tools           []struct {
										Name string `json:"name"`
									} `json:"tools"`
									StructuredContent struct {
										Message string `json:"message"`
									} `json:"structuredContent"`
								} `json:"result"`
							}
							require.NoError(tb, json.Unmarshal(response.JSONResponse, &envelope))
							wantID, err := json.Marshal(rpcID)
							require.NoError(tb, err)
							require.JSONEq(tb, string(wantID), string(envelope.ID))
							require.Empty(tb, envelope.Error)
							switch method {
							case "initialize":
								require.NotEmpty(tb, envelope.Result.ProtocolVersion)
							case "tools/list":
								require.Len(tb, envelope.Result.Tools, 1)
								require.Equal(tb, "hello", envelope.Result.Tools[0].Name)
							case "tools/call":
								require.Equal(tb, wantMessage, envelope.Result.StructuredContent.Message)
							}
						},
					}},
				})
			}
			// The caller owns fallback and may initialize twice before using a
			// shared stdio child. Reconnects reuse RPC IDs, but never queue IDs.
			for cycle := range 3 {
				addCommand("server/discover", "openai-mcp-discover", map[string]any{"_meta": map[string]any{
					"io.modelcontextprotocol/protocolVersion":    "2026-07-28",
					"io.modelcontextprotocol/clientCapabilities": map[string]any{},
				}}, "")
				for _, id := range []int{1, 0} {
					addCommand("initialize", id, map[string]any{
						"protocolVersion": "2025-11-25", "capabilities": map[string]any{},
						"clientInfo": map[string]any{"name": "legacy-discovery-test", "version": "1"},
					}, "")
				}
				addCommand("notifications/initialized", nil, map[string]any{}, "")
				addCommand("tools/list", 2, map[string]any{}, "")
				name := fmt.Sprintf("cycle-%d", cycle)
				addCommand("tools/call", 3, map[string]any{"name": "hello", "arguments": map[string]any{"name": name}}, "hello "+name)
			}

			messageLog, pidLog := t.TempDir()+"/messages.jsonl", t.TempDir()+"/pids.log"
			commandArgs := append([]string{"env",
				fmt.Sprintf("MOCK_MCP_DISCOVER_ERROR_CODE=%d", code),
				"MOCK_MCP_REQUIRE_INITIALIZED=1", "MOCK_MCP_RAW_MESSAGE_LOG=" + messageLog, "MOCK_MCP_PID_LOG=" + pidLog,
			}, mockmcpserver.StdioServerCommand(t)...)
			h := harnesspkg.NewHarness(t,
				harnesspkg.WithMCPCommand(commandArgs),
				harnesspkg.WithControlPlaneOptions(mocktunnelservice.WithCommandResponses(commands...)),
			)
			h.ExecuteScenarious(t)
			require.Len(t, h.ControlPlane.ReceivedResponses(mocktunnelservice.ResponseMatchMatched), len(commands))
			require.Empty(t, h.ControlPlane.ReceivedResponses(mocktunnelservice.ResponseMatchUnexpected))
			messages, err := os.ReadFile(messageLog)
			require.NoError(t, err)
			lines := strings.Split(strings.TrimSpace(string(messages)), "\n")
			require.Len(t, lines, len(wantMessages), "no synthesized or replayed MCP requests")
			for i, want := range wantMessages {
				require.JSONEq(t, string(want), lines[i])
			}
			pids, err := os.ReadFile(pidLog)
			require.NoError(t, err)
			require.Len(t, strings.Fields(string(pids)), 1, "fallback and reconnects must retain the same child")
		})
	}
}

func TestHarnessStdioOptInInitializesBeforeToolCallWhenControlPlaneOmitsNotification(t *testing.T) {
	t.Parallel()

	runStdioInitializeThenToolScenario(t, true, "MOCK_MCP_REQUIRE_INITIALIZED=1", "initialize\nnotifications/initialized\ntools/call\n")
}

func TestHarnessStdioDefaultDoesNotInjectInitializedNotification(t *testing.T) {
	t.Parallel()

	runStdioInitializationGuardScenario(t, false, false, []mocktunnelservice.CommandResponse{
		stdioGuardInitializeCommand(t),
		stdioGuardToolCommand(t, "before-initialized", false, http.StatusConflict),
	}, "initialize\n")
}

func TestHarnessStdioInitializationGuardRequiresServiceHandshakeAfterRestart(t *testing.T) {
	t.Parallel()

	// Each harness stops its client and child before returning. Reusing the
	// default tunnel ID proves the replacement runtime starts uninitialized.
	for _, generation := range []string{"first_child", "replacement_child"} {
		t.Run(generation, func(t *testing.T) {
			runStdioInitializationGuardScenario(t, false, true, []mocktunnelservice.CommandResponse{
				stdioGuardToolCommand(t, "before-initialize", false, http.StatusConflict),
				stdioGuardInitializeCommand(t),
				stdioGuardToolCommand(t, "before-initialized", false, http.StatusConflict),
				stdioGuardInitializedCommand(t),
				stdioGuardToolCommand(t, "after-initialized", false, http.StatusOK),
			}, "initialize\nnotifications/initialized\ntools/call\n")
		})
	}
}

func TestHarnessStdioInitializationGuardSupportsInitializedNotificationShim(t *testing.T) {
	t.Parallel()

	runStdioInitializationGuardScenario(t, true, true, []mocktunnelservice.CommandResponse{
		stdioGuardToolCommand(t, "before-initialize", false, http.StatusConflict),
		stdioGuardInitializeCommand(t),
		stdioGuardToolCommand(t, "after-initialize", false, http.StatusOK),
	}, "initialize\nnotifications/initialized\ntools/call\n")
}

func TestHarnessStdioInitializationGuardForwardsSelfContainedRequests(t *testing.T) {
	t.Parallel()

	runStdioInitializationGuardScenario(t, false, false, []mocktunnelservice.CommandResponse{
		stdioGuardToolCommand(t, "self-contained", true, http.StatusOK),
		// A self-contained request does not initialize the legacy session.
		stdioGuardToolCommand(t, "legacy-after-self-contained", false, http.StatusConflict),
	}, "tools/call\n")
}

func TestHarnessStdioInitializationGuardClassifiesRequestsIndependentlyOfChild(t *testing.T) {
	t.Parallel()

	// This child accepts tools without initialization. Metadata-free requests
	// still require the legacy handshake because they carry no stateless signal.
	runStdioInitializationGuardScenario(t, false, false, []mocktunnelservice.CommandResponse{
		stdioGuardToolCommand(t, "legacy-without-initialize", false, http.StatusConflict),
		stdioGuardToolCommand(t, "self-contained", true, http.StatusOK),
	}, "tools/call\n")
}

func runStdioInitializationGuardScenario(t *testing.T, sendInitializedNotification, childRequiresInitialization bool, commands []mocktunnelservice.CommandResponse, wantMessages string) {
	t.Helper()

	messageLog := t.TempDir() + "/stdio-messages.log"
	commandArgs := []string{"env", "MOCK_MCP_MESSAGE_LOG=" + messageLog}
	if childRequiresInitialization {
		commandArgs = append(commandArgs, "MOCK_MCP_REQUIRE_INITIALIZED=1")
	}
	commandArgs = append(commandArgs, mockmcpserver.StdioServerCommand(t)...)
	h := harnesspkg.NewHarness(t,
		harnesspkg.WithMCPCommand(commandArgs),
		harnesspkg.WithScenarioTimeout(3*time.Second),
		harnesspkg.WithClientConfig(func(cfg *config.Config) {
			cfg.MCP.StdioSendInitializedNotification = sendInitializedNotification
		}),
		harnesspkg.WithControlPlaneOptions(mocktunnelservice.WithCommandResponses(commands...)),
	)
	h.ExecuteScenarious(t)

	messages, err := os.ReadFile(messageLog)
	if err != nil {
		t.Fatalf("read stdio message log: %v", err)
	}
	if got := string(messages); got != wantMessages {
		t.Fatalf("messages delivered to child = %q, want %q", got, wantMessages)
	}
	if got := len(h.ControlPlane.ReceivedResponses(mocktunnelservice.ResponseMatchMatched)); got != len(commands) {
		t.Fatalf("posted responses = %d, want %d", got, len(commands))
	}
}

func stdioGuardInitializeCommand(t *testing.T) mocktunnelservice.CommandResponse {
	t.Helper()
	return stdioGuardCommand(t, "initialize", "initialize-1", json.RawMessage(`{
		"jsonrpc":"2.0","id":"initialize-1","method":"initialize",
		"params":{"protocolVersion":"2025-11-25","capabilities":{},
		"clientInfo":{"name":"stdio-guard-e2e","version":"1"}}
	}`), http.StatusOK)
}

func stdioGuardInitializedCommand(t *testing.T) mocktunnelservice.CommandResponse {
	t.Helper()
	return stdioGuardCommand(t, "initialized", "", json.RawMessage(`{
		"jsonrpc":"2.0","method":"notifications/initialized"
	}`), http.StatusOK)
}

func stdioGuardToolCommand(t *testing.T, requestID string, selfContained bool, wantStatus int) mocktunnelservice.CommandResponse {
	t.Helper()
	meta := ""
	if selfContained {
		meta = `,"_meta":{
			"io.modelcontextprotocol/protocolVersion":"2026-07-28",
			"io.modelcontextprotocol/clientCapabilities":{},
			"io.modelcontextprotocol/clientInfo":{"name":"stdio-guard-e2e","version":"1"}
		}`
	}
	// Reuse the JSON-RPC ID across rejected and successful calls to verify a
	// rejected request does not retire an ID that was never sent to the child.
	return stdioGuardCommand(t, requestID, "tool-1", json.RawMessage(`{
		"jsonrpc":"2.0","id":"tool-1","method":"tools/call",
		"params":{"name":"echo","arguments":{"name":"Ada"}`+meta+`}
	}`), wantStatus)
}

func stdioGuardCommand(t *testing.T, requestID, rpcID string, payload json.RawMessage, wantStatus int) mocktunnelservice.CommandResponse {
	t.Helper()
	return mocktunnelservice.CommandResponse{
		// The scenario's shorter failure deadline proves rejection is posted
		// before the normal command response timeout could expire.
		Command: withResponseTimeout(t, mocktunnelservice.NewCommand(requestID, payload, nil), "30s"),
		ExpectedResponses: []mocktunnelservice.ExpectedResponse{{
			RequestID: requestID,
			Assert: func(tb testing.TB, response mocktunnelservice.ReceivedResponse) {
				tb.Helper()
				if response.ResponseCode != wantStatus {
					tb.Fatalf("%s status = %d, want %d; payload=%s", requestID, response.ResponseCode, wantStatus, response.JSONResponse)
				}
				if rpcID == "" {
					if response.ResponseType != string(wiretypes.ResponsePayloadNotifyAck) || len(response.JSONResponse) != 0 {
						tb.Fatalf("notification response = %+v, want empty notify_ack", response)
					}
					return
				}
				if response.ResponseType != string(wiretypes.ResponsePayloadJSONRPC) {
					tb.Fatalf("%s response type = %q, want JSON-RPC", requestID, response.ResponseType)
				}
				var envelope struct {
					JSONRPC string          `json:"jsonrpc"`
					ID      string          `json:"id"`
					Result  json.RawMessage `json:"result"`
					Error   *struct {
						Code    int    `json:"code"`
						Message string `json:"message"`
						Data    struct {
							Origin    string `json:"origin"`
							ErrorType string `json:"error_type"`
						} `json:"data"`
					} `json:"error"`
				}
				if err := json.Unmarshal(response.JSONResponse, &envelope); err != nil {
					tb.Fatalf("decode %s response: %v", requestID, err)
				}
				if envelope.JSONRPC != "2.0" || envelope.ID != rpcID {
					tb.Fatalf("%s response envelope = version %q id %q, want 2.0 id %q", requestID, envelope.JSONRPC, envelope.ID, rpcID)
				}
				if wantStatus == http.StatusConflict {
					const wantMessage = "MCP server is not initialized; send initialize and notifications/initialized before operational requests"
					if envelope.Error == nil || envelope.Error.Code != -32002 || envelope.Error.Message != wantMessage || len(envelope.Result) != 0 {
						tb.Fatalf("%s initialization-required response = %s", requestID, response.JSONResponse)
					}
					if envelope.Error.Data.Origin != "tunnel-client" || envelope.Error.Data.ErrorType != "mcp_initialization_required" {
						tb.Fatalf("%s initialization-required error source = %+v", requestID, envelope.Error.Data)
					}
					return
				}
				if envelope.Error != nil || len(envelope.Result) == 0 {
					tb.Fatalf("%s success response = %s", requestID, response.JSONResponse)
				}
				if rpcID == "tool-1" && !bytes.Contains(envelope.Result, []byte(`"message":"hello Ada"`)) {
					tb.Fatalf("%s tool result = %s", requestID, envelope.Result)
				}
			},
		}},
	}
}

func runStdioInitializeThenToolScenario(t *testing.T, sendInitializedNotification bool, serverEnv, wantMessages string) {
	t.Helper()

	const (
		initializeRequestID = "cmd-initialize"
		initializeCallID    = "initialize-1"
		toolRequestID       = "cmd-tool"
		toolCallID          = "tool-1"
	)

	messageLog := t.TempDir() + "/stdio-messages.log"
	commandArgs := append([]string{
		"env",
		serverEnv,
		"MOCK_MCP_MESSAGE_LOG=" + messageLog,
	}, mockmcpserver.StdioServerCommand(t)...)

	initializeCommand := mocktunnelservice.CommandResponse{
		Command: mocktunnelservice.NewCommand(
			initializeRequestID,
			json.RawMessage(`{
				"jsonrpc":"2.0",
				"id":"`+initializeCallID+`",
				"method":"initialize",
				"params":{
					"protocolVersion":"2025-11-25",
					"capabilities":{},
					"clientInfo":{"name":"openai-mcp (Codex)","version":"1.0.0"}
				}
			}`),
			nil,
		),
		ExpectedResponses: []mocktunnelservice.ExpectedResponse{{
			RequestID: initializeRequestID,
			Assert: func(tb testing.TB, resp mocktunnelservice.ReceivedResponse) {
				if resp.ResponseType != string(wiretypes.ResponsePayloadJSONRPC) {
					tb.Fatalf("initialize response type mismatch: got %q", resp.ResponseType)
				}
				if resp.ResponseCode != http.StatusOK {
					tb.Fatalf("initialize response code mismatch: got %d", resp.ResponseCode)
				}
			},
		}},
	}
	toolCommand := mocktunnelservice.CommandResponse{
		Command: mocktunnelservice.NewCommand(
			toolRequestID,
			json.RawMessage(`{
				"jsonrpc":"2.0",
				"id":"`+toolCallID+`",
				"method":"tools/call",
				"params":{"name":"echo","arguments":{"name":"Ada"}}
			}`),
			nil,
		),
		ExpectedResponses: []mocktunnelservice.ExpectedResponse{{
			RequestID: toolRequestID,
			Assert: func(tb testing.TB, resp mocktunnelservice.ReceivedResponse) {
				if resp.ResponseType != string(wiretypes.ResponsePayloadJSONRPC) {
					tb.Fatalf("tool response type mismatch: got %q", resp.ResponseType)
				}
				if resp.ResponseCode != http.StatusOK {
					tb.Fatalf("tool response code mismatch: got %d", resp.ResponseCode)
				}
			},
		}},
	}

	options := []harnesspkg.HarnessOption{
		harnesspkg.WithMCPCommand(commandArgs),
		harnesspkg.WithScenarioTimeout(3 * time.Second),
		harnesspkg.WithControlPlaneOptions(
			mocktunnelservice.WithCommandResponses(initializeCommand, toolCommand),
		),
	}
	if sendInitializedNotification {
		options = append(options, harnesspkg.WithClientConfig(func(cfg *config.Config) {
			cfg.MCP.StdioSendInitializedNotification = true
		}))
	}
	h := harnesspkg.NewHarness(t, options...)
	h.ExecuteScenarious(t)

	messages, err := os.ReadFile(messageLog)
	if err != nil {
		t.Fatalf("read stdio message log: %v", err)
	}
	if got := string(messages); got != wantMessages {
		t.Fatalf("unexpected stdio lifecycle sequence: got %q want %q", got, wantMessages)
	}
}

func TestHarnessStdioResponseDeadlineKeepsServingAfterTimedOutRequest(t *testing.T) {
	t.Parallel()

	const (
		timedOutRequestID = "cmd-timeout"
		recoveryRequestID = "cmd-recovery"
		timedOutCallID    = "call-timeout"
		recoveryCallID    = timedOutCallID
	)

	invocationLog := t.TempDir() + "/stdio-invocations.log"

	timedOutCommand := mocktunnelservice.CommandResponse{
		Command: withResponseTimeout(t, mocktunnelservice.NewCommand(
			timedOutRequestID,
			json.RawMessage(`{
				"jsonrpc":"2.0",
				"id":"`+timedOutCallID+`",
				"method":"tools/call",
				"params":{
					"name":"echo",
					"arguments":{"name":"timeout","request_id":"timeout"}
				}
			}`),
			nil,
		), "2s"),
		NoResponseExpected: true,
	}
	recoveryCommand := mocktunnelservice.CommandResponse{
		Command: mocktunnelservice.NewCommand(
			recoveryRequestID,
			json.RawMessage(`{
				"jsonrpc":"2.0",
				"id":"`+recoveryCallID+`",
				"method":"tools/call",
				"params":{
					"name":"echo",
					"arguments":{"name":"recovered","request_id":"recovered"}
				}
			}`),
			nil,
		),
		ExpectedResponses: []mocktunnelservice.ExpectedResponse{{
			RequestID: recoveryRequestID,
			Assert: func(tb testing.TB, resp mocktunnelservice.ReceivedResponse) {
				if resp.ResponseType != string(wiretypes.ResponsePayloadJSONRPC) {
					tb.Fatalf("recovery response type mismatch: got %q", resp.ResponseType)
				}
				if resp.ResponseCode != http.StatusOK {
					tb.Fatalf("recovery response code mismatch: got %d", resp.ResponseCode)
				}
				if !bytes.Contains(resp.JSONResponse, []byte(`"message":"hello recovered"`)) {
					tb.Fatalf("recovery response payload mismatch: %s", string(resp.JSONResponse))
				}
				var payload map[string]any
				if err := json.Unmarshal(resp.JSONResponse, &payload); err != nil {
					tb.Fatalf("decode recovery response payload: %v", err)
				}
				if payload["id"] != recoveryCallID {
					tb.Fatalf("recovery response ID mismatch: got %v want %q", payload["id"], recoveryCallID)
				}
			},
		}},
	}

	var logs bytes.Buffer
	commandArgs := append([]string{
		"env",
		"MOCK_MCP_DROP_RESPONSE_NAME=timeout",
		"MOCK_MCP_SERVER_REQUEST_BEFORE_RESPONSE_NAME=recovered",
		"MOCK_MCP_INVOCATION_LOG=" + invocationLog,
	}, mockmcpserver.StdioServerCommand(t)...)
	h := harnesspkg.NewHarness(t,
		harnesspkg.WithMCPCommand(commandArgs),
		harnesspkg.WithLogWriter(&logs),
		harnesspkg.WithScenarioTimeout(8*time.Second),
		harnesspkg.WithClientConfig(func(cfg *config.Config) {
			cfg.Logging.Level = slog.LevelInfo
			// Keep one dispatcher worker so the recovery command can only
			// write after the timed-out lifecycle releases its stdio slot.
			cfg.MCP.MaxConcurrentRequests = 1
		}),
		harnesspkg.WithControlPlaneOptions(
			mocktunnelservice.WithInitializationPhaseCommandsWithoutSessionHeaders(),
			mocktunnelservice.WithCommandResponses(timedOutCommand, recoveryCommand),
		),
	)
	h.ExecuteScenarious(t)

	invocations, err := os.ReadFile(invocationLog)
	if err != nil {
		t.Fatalf("read stdio invocation log: %v", err)
	}
	if got := string(invocations); !strings.Contains(got, "timeout\n") || !strings.Contains(got, "recovered\n") {
		t.Fatalf("stdio server did not observe both commands: %q", got)
	}
	if !strings.Contains(logs.String(), "command response deadline reached; dropping without posting a response") {
		t.Fatalf("missing response deadline log:\n%s", logs.String())
	}
	// ExecuteScenarious stops the client before returning, and normal stdio
	// teardown emits the generic shutdown warning.
	if strings.Contains(logs.String(), `reason="stdio MCP command stdin write failed"`) ||
		strings.Contains(logs.String(), "file already closed") {
		t.Fatalf("stdio deadline closed shared transport:\n%s", logs.String())
	}
}

func runSimpleToolScenarioWithCommand(t *testing.T, commandArgs []string) {
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

	options := []harnesspkg.HarnessOption{
		harnesspkg.WithClientConfig(func(cfg *config.Config) {
			cfg.Logging.Level = slog.LevelDebug
		}),
		harnesspkg.WithMCPCommand(commandArgs),
		harnesspkg.WithControlPlaneOptions(
			mocktunnelservice.WithInitializationPhaseCommandsWithoutSessionHeaders(),
			mocktunnelservice.WithCommandResponses(toolCommand),
		),
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
	expectedMessage := fmt.Sprintf("hello %s", userName)
	if msg != expectedMessage {
		t.Fatalf("unexpected tool response message: got %q want %q", msg, expectedMessage)
	}
}

func withResponseTimeout(t testing.TB, command json.RawMessage, timeout string) json.RawMessage {
	t.Helper()
	var payload map[string]any
	if err := json.Unmarshal(command, &payload); err != nil {
		t.Fatalf("decode command payload: %v", err)
	}
	payload["response_timeout"] = timeout
	encoded, err := json.Marshal(payload)
	if err != nil {
		t.Fatalf("encode command payload: %v", err)
	}
	return encoded
}
