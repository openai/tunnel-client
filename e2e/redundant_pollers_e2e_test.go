package e2e_test

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"go.openai.org/api/tunnel-client/pkg/controlplane/wiretypes"
	harnesspkg "go.openai.org/api/tunnel-client/testsupport/e2e"
	"go.openai.org/api/tunnel-client/testsupport/mockmcpserver"
	"go.openai.org/api/tunnel-client/testsupport/mocktunnelservice"
)

const redundantPollerTestTimeout = 2 * time.Second

func TestRedundantTunnelClientsPollSameTunnelAndHandover(t *testing.T) {
	t.Parallel()

	const toolName = "hello"

	var (
		primaryClient   *harnesspkg.TunnelClient
		secondaryClient *harnesspkg.TunnelClient
		handoverOnce    sync.Once
		resumeOnce      sync.Once
	)
	steadyStateReady := make(chan struct{})
	handoverReady := make(chan struct{})
	resumedReady := make(chan struct{})
	handoverErrors := make(chan error, 1)
	invocationLogPath := filepath.Join(t.TempDir(), "mcp-invocations.log")

	commands := []mocktunnelservice.CommandResponse{
		newRedundantToolCommand("cmd-steady-1", toolName, steadyStateReady, nil),
		newRedundantToolCommand("cmd-steady-2", toolName, steadyStateReady, func(testing.TB) {
			handoverOnce.Do(func() {
				go func() {
					ctx, cancel := context.WithTimeout(context.Background(), redundantPollerTestTimeout)
					defer cancel()
					if err := primaryClient.PausePoller(ctx); err != nil {
						handoverErrors <- fmt.Errorf("pause primary poller: %w", err)
						close(handoverReady)
						return
					}
					pollsBeforeHandover := secondaryClient.PollCount()
					if err := secondaryClient.WaitForPolls(ctx, pollsBeforeHandover+1); err != nil {
						handoverErrors <- fmt.Errorf("wait for secondary handover poll: %w", err)
						close(handoverReady)
						return
					}
					close(handoverReady)
				}()
			})
		}),
		newRedundantToolCommand("cmd-handover-1", toolName, handoverReady, nil),
		newRedundantToolCommand("cmd-handover-2", toolName, handoverReady, func(tb testing.TB) {
			resumeOnce.Do(func() {
				pollsBeforeResume := primaryClient.PollCount()
				primaryClient.UnpausePoller()
				ctx, cancel := context.WithTimeout(context.Background(), redundantPollerTestTimeout)
				defer cancel()
				if err := primaryClient.WaitForPolls(ctx, pollsBeforeResume+1); err != nil {
					tb.Fatalf("wait for primary poller resume: %v", err)
				}
				close(resumedReady)
			})
		}),
		newRedundantToolCommand("cmd-resumed-1", toolName, resumedReady, nil),
		newRedundantToolCommand("cmd-resumed-2", toolName, resumedReady, nil),
	}

	h := harnesspkg.NewHarness(
		t,
		harnesspkg.WithControlPlaneOptions(
			mocktunnelservice.WithPollWaitLimit(time.Second),
			mocktunnelservice.WithCommandResponses(commands...),
		),
		harnesspkg.WithMCPCommand(redundantStdioCommand(t, invocationLogPath)),
		harnesspkg.WithAfterClientStart(func(h *harnesspkg.Harness) {
			primaryClient = h.PrimaryClient()
			secondaryClient = h.StartAdditionalClient(t)
			waitForActiveRedundantPollers(
				t,
				h,
				harnesspkg.TestClientInstanceHeader,
				primaryClient,
				secondaryClient,
			)
			close(steadyStateReady)
		}),
	)

	h.ExecuteScenarious(t)

	select {
	case err := <-handoverErrors:
		t.Fatal(err)
	default:
	}

	assertRedundantScenarioExactlyOnce(t, h, invocationLogPath, len(commands))
}

func waitForActiveRedundantPollers(
	t *testing.T,
	h *harnesspkg.Harness,
	clientHeader string,
	clients ...*harnesspkg.TunnelClient,
) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), redundantPollerTestTimeout)
	defer cancel()
	for _, client := range clients {
		if err := client.WaitForPolls(ctx, 1); err != nil {
			t.Fatalf("wait for %s poller: %v", client.Name(), err)
		}
		clientName := client.Name()
		if err := h.ControlPlane.WaitForHTTPRequests(ctx, 1, func(req mocktunnelservice.IncomingHTTPRequest) bool {
			return req.Method == http.MethodGet &&
				strings.HasSuffix(req.Path, "/poll") &&
				req.Headers.Get(clientHeader) == clientName
		}); err != nil {
			t.Fatalf("wait for %s poll request: %v", clientName, err)
		}
	}
}

func newRedundantToolCommand(
	requestID string,
	toolName string,
	deliverAfter <-chan struct{},
	afterResponse func(testing.TB),
) mocktunnelservice.CommandResponse {
	return mocktunnelservice.CommandResponse{
		Command: mocktunnelservice.NewCommand(
			requestID,
			json.RawMessage(fmt.Sprintf(`{
				"jsonrpc":"2.0",
				"id":"call-%s",
				"method":"tools/call",
				"params":{
					"name":"%s",
					"arguments":{"name":"%s","request_id":"%s"}
				}
			}`, requestID, toolName, requestID, requestID)),
			nil,
		),
		DeliverAfter: deliverAfter,
		ExpectedResponses: []mocktunnelservice.ExpectedResponse{{
			RequestID: requestID,
			Assert: func(tb testing.TB, resp mocktunnelservice.ReceivedResponse) {
				if resp.ResponseType != string(wiretypes.ResponsePayloadJSONRPC) {
					tb.Fatalf("response type for %s: got %q", requestID, resp.ResponseType)
				}
				if resp.ResponseCode != http.StatusOK {
					tb.Fatalf("response code for %s: got %d", requestID, resp.ResponseCode)
				}
				var payload struct {
					Error  json.RawMessage `json:"error"`
					Result struct {
						StructuredContent map[string]any `json:"structuredContent"`
					} `json:"result"`
				}
				if err := json.Unmarshal(resp.JSONResponse, &payload); err != nil {
					tb.Fatalf("decode response for %s: %v", requestID, err)
				}
				if len(payload.Error) != 0 {
					tb.Fatalf("response error for %s: %s", requestID, payload.Error)
				}
				message, _ := payload.Result.StructuredContent["message"].(string)
				if want := fmt.Sprintf("hello %s", requestID); message != want {
					tb.Fatalf("response message for %s: got %q want %q", requestID, message, want)
				}
				if afterResponse != nil {
					afterResponse(tb)
				}
			},
		}},
	}
}

func redundantStdioCommand(t *testing.T, invocationLogPath string) []string {
	t.Helper()
	command := mockmcpserver.StdioServerCommand(t)
	return append([]string{"env", "MOCK_MCP_INVOCATION_LOG=" + invocationLogPath}, command...)
}

func assertRedundantScenarioExactlyOnce(
	t *testing.T,
	h *harnesspkg.Harness,
	invocationLogPath string,
	want int,
) {
	t.Helper()

	deliveredRequestIDs := requestIDsFromCommands(t, h.ControlPlane.DeliveredCommands())
	assertUniqueRequestIDs(t, "delivered commands", deliveredRequestIDs, want)

	matched := h.ControlPlane.ReceivedResponses(mocktunnelservice.ResponseMatchMatched)
	responseRequestIDs := make([]string, 0, len(matched))
	for _, resp := range matched {
		responseRequestIDs = append(responseRequestIDs, resp.RequestID)
	}
	assertUniqueRequestIDs(t, "matched responses", responseRequestIDs, want)
	assertSameRequestIDs(t, "matched responses", deliveredRequestIDs, responseRequestIDs)
	if unexpected := h.ControlPlane.ReceivedResponses(mocktunnelservice.ResponseMatchUnexpected); len(unexpected) != 0 {
		t.Fatalf("unexpected responses: got %d", len(unexpected))
	}

	invokedRequestIDs := readStdioInvocationRequestIDs(t, invocationLogPath)
	assertUniqueRequestIDs(t, "MCP tool invocations", invokedRequestIDs, want)
	assertSameRequestIDs(t, "MCP tool invocations", deliveredRequestIDs, invokedRequestIDs)
}

func readStdioInvocationRequestIDs(t *testing.T, invocationLogPath string) []string {
	t.Helper()
	contents, err := os.ReadFile(invocationLogPath)
	if err != nil {
		t.Fatalf("read MCP invocation log: %v", err)
	}
	return strings.Fields(string(contents))
}

func requestIDsFromCommands(t *testing.T, commands []json.RawMessage) []string {
	t.Helper()
	requestIDs := make([]string, 0, len(commands))
	for _, command := range commands {
		var payload struct {
			RequestID string `json:"request_id"`
		}
		if err := json.Unmarshal(command, &payload); err != nil {
			t.Fatalf("decode delivered command: %v", err)
		}
		requestIDs = append(requestIDs, payload.RequestID)
	}
	return requestIDs
}

func assertUniqueRequestIDs(t *testing.T, label string, requestIDs []string, want int) {
	t.Helper()
	if len(requestIDs) != want {
		t.Fatalf("%s: got %d want %d", label, len(requestIDs), want)
	}
	seen := make(map[string]struct{}, len(requestIDs))
	for _, requestID := range requestIDs {
		if requestID == "" {
			t.Fatalf("%s: request_id must be non-empty", label)
		}
		if _, ok := seen[requestID]; ok {
			t.Fatalf("%s: duplicate request_id %q", label, requestID)
		}
		seen[requestID] = struct{}{}
	}
}

func assertSameRequestIDs(t *testing.T, label string, want []string, got []string) {
	t.Helper()
	wantSet := make(map[string]struct{}, len(want))
	for _, requestID := range want {
		wantSet[requestID] = struct{}{}
	}
	gotSet := make(map[string]struct{}, len(got))
	for _, requestID := range got {
		gotSet[requestID] = struct{}{}
	}
	if len(gotSet) != len(wantSet) {
		t.Fatalf("%s: got request_ids %v want %v", label, got, want)
	}
	for requestID := range wantSet {
		if _, ok := gotSet[requestID]; !ok {
			t.Fatalf("%s: got request_ids %v want %v", label, got, want)
		}
	}
}
