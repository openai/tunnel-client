package e2e_test

import (
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/openai/tunnel-client/pkg/config"
	"github.com/openai/tunnel-client/pkg/controlplane/wiretypes"
	harnesspkg "github.com/openai/tunnel-client/testsupport/e2e"
	"github.com/openai/tunnel-client/testsupport/mocktunnelservice"
)

const harpoonConnectionTTLE2E = 250 * time.Millisecond

func TestHarpoonChannelReconnectsAfterConnectionTTL(t *testing.T) {
	runHarpoonChannelReconnectAfterTTLs(t, 1)
}

func TestHarpoonChannelReconnectsAfterRepeatedConnectionTTLs(t *testing.T) {
	runHarpoonChannelReconnectAfterTTLs(t, 2)
}

func runHarpoonChannelReconnectAfterTTLs(t *testing.T, ttlCycles int) {
	t.Helper()
	if ttlCycles < 1 {
		t.Fatalf("ttlCycles must be positive: got %d", ttlCycles)
	}

	releaseTarget := make(chan struct{})
	var releaseOnce sync.Once
	release := func() {
		releaseOnce.Do(func() {
			close(releaseTarget)
		})
	}

	handlerDone := make(chan struct{}, ttlCycles+1)
	targetCanceled := make([]chan struct{}, ttlCycles)
	for i := range targetCanceled {
		targetCanceled[i] = make(chan struct{})
	}
	var targetCalls atomic.Int32
	targetServer := httptest.NewServer(http.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) {
		callIndex := int(targetCalls.Add(1)) - 1
		if callIndex < 0 || callIndex >= len(targetCanceled) {
			<-releaseTarget
			handlerDone <- struct{}{}
			return
		}
		select {
		case <-r.Context().Done():
			close(targetCanceled[callIndex])
		case <-releaseTarget:
		}
		handlerDone <- struct{}{}
	}))
	defer targetServer.Close()
	defer release()

	targetURL := mustParseURL(t, targetServer.URL)
	commands := make([]mocktunnelservice.CommandResponse, 0, 3*ttlCycles+3)
	for session := 0; session <= ttlCycles; session++ {
		initialize := newTTLHarpoonInitializeCommand(t, session)
		if session > 0 {
			// A TTL-expired Harpoon request must release its outbound HTTP
			// request before the replacement session starts. Without session
			// cancellation this gate never opens and the scenario times out.
			initialize.DeliverAfter = targetCanceled[session-1]
		}
		commands = append(
			commands,
			initialize,
			newTTLHarpoonInitializedCommand(t, session),
		)
		if session < ttlCycles {
			commands = append(commands, newTTLHarpoonSlowCallCommand(session))
		}
	}
	commands = append(commands, newTTLHarpoonToolsListCommand(t, ttlCycles))

	h := harnesspkg.NewHarness(
		t,
		harnesspkg.WithClientConfig(func(cfg *config.Config) {
			cfg.Logging.Level = slog.LevelDebug
			cfg.MCP.ConnectionMaxTTL = harpoonConnectionTTLE2E
			cfg.MCP.MaxConcurrentRequests = 1
			cfg.Harpoon.AllowPlaintextHTTP = true
			cfg.Harpoon.Targets = []config.HarpoonTarget{{
				Label:       "slow",
				Description: "target that stays in flight until the dispatcher TTL expires",
				BaseURL:     targetURL,
			}}
		}),
		harnesspkg.WithControlPlaneOptions(
			mocktunnelservice.WithCommandResponses(commands...),
		),
		harnesspkg.WithBeforeClientStop(func(_ *harnesspkg.Harness) {
			if got := int(targetCalls.Load()); got != ttlCycles {
				t.Fatalf("harpoon target call count mismatch: got %d want %d", got, ttlCycles)
			}
			for i, canceled := range targetCanceled {
				select {
				case <-canceled:
				default:
					t.Fatalf("harpoon target call %d was not canceled before reconnect", i)
				}
			}

			release()
			timer := time.NewTimer(5 * time.Second)
			defer timer.Stop()
			for i := 0; i < ttlCycles; i++ {
				select {
				case <-handlerDone:
				case <-timer.C:
					t.Fatalf("timed out waiting for %d harpoon target handler(s) to exit", ttlCycles-i)
				}
			}
		}),
	)

	h.ExecuteScenarious(t)

	matched := h.ControlPlane.ReceivedResponses(mocktunnelservice.ResponseMatchMatched)
	wantMatched := 2*(ttlCycles+1) + 1
	if len(matched) != wantMatched {
		t.Fatalf("matched response count mismatch: got %d want %d", len(matched), wantMatched)
	}
	unexpected := h.ControlPlane.ReceivedResponses(mocktunnelservice.ResponseMatchUnexpected)
	if len(unexpected) != 0 {
		t.Fatalf("expected no unexpected responses after TTL reconnects; got %+v", unexpected)
	}
	delivered := h.ControlPlane.DeliveredCommands()
	wantDelivered := 3*ttlCycles + 3
	if len(delivered) != wantDelivered {
		t.Fatalf("delivered command count mismatch: got %d want %d", len(delivered), wantDelivered)
	}
}

func newTTLHarpoonInitializeCommand(t *testing.T, session int) mocktunnelservice.CommandResponse {
	t.Helper()
	requestID := fmt.Sprintf("cmd-harpoon-init-%d", session)
	return mocktunnelservice.CommandResponse{
		Command: newChannelCommand(
			requestID,
			"harpoon",
			json.RawMessage(fmt.Sprintf(`{
				"jsonrpc":"2.0",
				"id":"initialize-harpoon-%d",
				"method":"initialize",
				"params":{
					"protocolVersion":"2025-06-18",
					"capabilities":{},
					"clientInfo":{"name":"harpoon-ttl-e2e","version":"0.0.1"}
				}
			}`, session)),
			ttlHarpoonHeaders("application/json, text/event-stream"),
		),
		ExpectedResponses: []mocktunnelservice.ExpectedResponse{
			expectedTTLHarpoonInitializeResponse(t, requestID),
		},
	}
}

func newTTLHarpoonInitializedCommand(t *testing.T, session int) mocktunnelservice.CommandResponse {
	t.Helper()
	requestID := fmt.Sprintf("cmd-harpoon-initialized-%d", session)
	return mocktunnelservice.CommandResponse{
		Command: newChannelCommand(
			requestID,
			"harpoon",
			json.RawMessage(`{
				"jsonrpc":"2.0",
				"method":"notifications/initialized",
				"params":{}
			}`),
			ttlHarpoonHeaders("application/json"),
		),
		ExpectedResponses: []mocktunnelservice.ExpectedResponse{
			expectedTTLHarpoonInitializedResponse(t, requestID),
		},
	}
}

func newTTLHarpoonSlowCallCommand(session int) mocktunnelservice.CommandResponse {
	requestID := fmt.Sprintf("cmd-harpoon-slow-call-%d", session)
	return mocktunnelservice.CommandResponse{
		Command: newChannelCommand(
			requestID,
			"harpoon",
			json.RawMessage(fmt.Sprintf(`{
				"jsonrpc":"2.0",
				"id":"slow-harpoon-%d",
				"method":"tools/call",
				"params":{
					"name":"call_target",
					"arguments":{
						"label":"slow",
						"method":"GET",
						"headers":{}
					}
				}
			}`, session)),
			ttlHarpoonHeaders("application/json"),
		),
		// The dispatcher drops this response when its connection TTL expires.
		// Completing the scripted command on delivery lets the mock continue
		// polling while the real dispatcher queue remains serialized.
		NoResponseExpected: true,
	}
}

func newTTLHarpoonToolsListCommand(t *testing.T, session int) mocktunnelservice.CommandResponse {
	t.Helper()
	requestID := fmt.Sprintf("cmd-harpoon-tools-list-%d", session)
	return mocktunnelservice.CommandResponse{
		Command: newChannelCommand(
			requestID,
			"harpoon",
			json.RawMessage(fmt.Sprintf(`{
				"jsonrpc":"2.0",
				"id":"tools-list-harpoon-%d",
				"method":"tools/list",
				"params":{}
			}`, session)),
			ttlHarpoonHeaders("application/json"),
		),
		ExpectedResponses: []mocktunnelservice.ExpectedResponse{
			expectedTTLHarpoonToolsListResponse(t, requestID),
		},
	}
}

func expectedTTLHarpoonInitializeResponse(t *testing.T, requestID string) mocktunnelservice.ExpectedResponse {
	t.Helper()
	return mocktunnelservice.ExpectedResponse{
		RequestID: requestID,
		Assert: func(tb testing.TB, resp mocktunnelservice.ReceivedResponse) {
			target := ttlHarpoonAssertionTarget(tb, t)
			if strings.Contains(string(resp.JSONResponse), "closed pipe") {
				target.Fatalf("initialize response reused a closed Harpoon pipe: %s", resp.JSONResponse)
			}
			if resp.ResponseType != string(wiretypes.ResponsePayloadJSONRPC) {
				target.Fatalf("initialize response type mismatch: got %q", resp.ResponseType)
			}
			if resp.ResponseCode != http.StatusOK {
				target.Fatalf("initialize response code mismatch: got %d payload=%s", resp.ResponseCode, resp.JSONResponse)
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
				target.Fatalf("initialize server info mismatch: got %q want %q", payload.Result.ServerInfo.Name, "harpoon")
			}
		},
	}
}

func expectedTTLHarpoonInitializedResponse(t *testing.T, requestID string) mocktunnelservice.ExpectedResponse {
	t.Helper()
	return mocktunnelservice.ExpectedResponse{
		RequestID: requestID,
		Assert: func(tb testing.TB, resp mocktunnelservice.ReceivedResponse) {
			target := ttlHarpoonAssertionTarget(tb, t)
			if resp.ResponseType != string(wiretypes.ResponsePayloadNotifyAck) {
				target.Fatalf("initialized ack type mismatch: got %q", resp.ResponseType)
			}
			if resp.ResponseCode != http.StatusOK {
				target.Fatalf("initialized ack code mismatch: got %d", resp.ResponseCode)
			}
		},
	}
}

func expectedTTLHarpoonToolsListResponse(t *testing.T, requestID string) mocktunnelservice.ExpectedResponse {
	t.Helper()
	return mocktunnelservice.ExpectedResponse{
		RequestID: requestID,
		Assert: func(tb testing.TB, resp mocktunnelservice.ReceivedResponse) {
			target := ttlHarpoonAssertionTarget(tb, t)
			if resp.ResponseType != string(wiretypes.ResponsePayloadJSONRPC) {
				target.Fatalf("tools/list response type mismatch: got %q", resp.ResponseType)
			}
			if resp.ResponseCode != http.StatusOK {
				target.Fatalf("tools/list response code mismatch: got %d payload=%s", resp.ResponseCode, resp.JSONResponse)
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
	}
}

func ttlHarpoonHeaders(accept string) http.Header {
	return http.Header{
		"Accept":       []string{accept},
		"Content-Type": []string{"application/json"},
	}
}

func ttlHarpoonAssertionTarget(tb testing.TB, fallback *testing.T) testing.TB {
	if tb != nil {
		tb.Helper()
		return tb
	}
	fallback.Helper()
	return fallback
}
