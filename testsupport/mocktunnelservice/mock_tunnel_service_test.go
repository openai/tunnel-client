package mocktunnelservice

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/url"
	"testing"
	"time"

	wiretypes "github.com/openai/tunnel-client/pkg/controlplane/wiretypes"
)

func TestMockTunnelServiceUsage(t *testing.T) {
	t.Parallel()

	mock := NewMockTunnelService(
		WithTunnelID("cli-tunnel"),
		WithAPIKey("test-api-key"),
		WithCommandResponses(
			CommandResponse{
				Command: json.RawMessage(`{
				"request_id":"cmd-1",
				"jsonrpc":{"jsonrpc":"2.0","id":1,"method":"ping"},
				"created_at":"2025-01-01T00:00:00Z",
				"headers":{"X-Test-Header":["alpha"]}
			}`),
				ExpectedResponses: []ExpectedResponse{{
					RequestID: "cmd-1",
					Headers: http.Header{
						"Content-Type": {"application/json"},
						"X-Mcp-Test":   {"alpha"},
					},
					Assert: func(tb testing.TB, resp ReceivedResponse) {
						if tb != nil {
							tb.Helper()
						}
						if resp.ResponseCode != http.StatusOK {
							tb.Fatalf("unexpected resp_code %d", resp.ResponseCode)
						}
					},
				}},
			},
			CommandResponse{
				Command: NewCommand(
					"cmd-2",
					json.RawMessage(`{"jsonrpc":"2.0","method":"notifications/ping","params":{"message":"hi"}}`),
					nil,
				),
				ExpectedResponses: []ExpectedResponse{{
					RequestID: "cmd-2",
					Assert: func(tb testing.TB, resp ReceivedResponse) {
						if tb != nil {
							tb.Helper()
						}
						if resp.ResponseCode != http.StatusAccepted {
							tb.Fatalf("expected 202 for notification, got %d", resp.ResponseCode)
						}
						if len(resp.JSONResponse) != 0 {
							tb.Fatalf("notification ack should not include resp_json, got %s", string(resp.JSONResponse))
						}
					},
				}},
			},
		),
	)

	mock.Start(t)

	baseURL := mock.BaseURL()
	if baseURL == nil {
		t.Fatal("mock did not expose a base URL")
	}

	client := &http.Client{}

	pollReq := newTunnelRequest(t, http.MethodGet, baseURL, &url.URL{Path: "/v1/tunnels/cli-tunnel/poll", RawQuery: "limit=1"}, mock, nil)

	pollResp, err := client.Do(pollReq)
	if err != nil {
		t.Fatalf("execute poll: %v", err)
	}
	defer func() {
		_ = pollResp.Body.Close()
	}()

	if pollResp.StatusCode != http.StatusOK {
		t.Fatalf("unexpected poll status %d", pollResp.StatusCode)
	}

	var envelope struct {
		Commands []json.RawMessage `json:"commands"`
	}
	if err := json.NewDecoder(pollResp.Body).Decode(&envelope); err != nil {
		t.Fatalf("decode poll payload: %v", err)
	}
	if len(envelope.Commands) != 1 {
		t.Fatalf("expected 1 command, got %d", len(envelope.Commands))
	}

	delivered := mock.DeliveredCommands()
	if len(delivered) != 1 {
		t.Fatalf("expected 1 delivered command, got %d", len(delivered))
	}
	var firstDelivered map[string]any
	if err := json.Unmarshal(delivered[0], &firstDelivered); err != nil {
		t.Fatalf("decode delivered command: %v", err)
	}
	if firstDelivered["request_id"] != "cmd-1" {
		t.Fatalf("unexpected delivered request_id %v", firstDelivered["request_id"])
	}

	payload := map[string]any{
		"request_id": "cmd-1",
		"resp_json": map[string]any{
			"jsonrpc": "2.0",
			"id":      1,
			"result":  map[string]any{"ok": true},
		},
		"resp_code": http.StatusOK,
		"resp_type": "jsonrpc_response",
		"resp_headers": map[string][]string{
			"Content-Type": {"application/json"},
			"X-Mcp-Test":   {"alpha"},
		},
	}

	body, err := json.Marshal(payload)
	if err != nil {
		t.Fatalf("marshal response payload: %v", err)
	}

	responseReq := newTunnelRequest(t, http.MethodPost, baseURL, &url.URL{Path: "/v1/tunnels/cli-tunnel/response"}, mock, bytes.NewReader(body))

	response, err := client.Do(responseReq)
	if err != nil {
		t.Fatalf("post response: %v", err)
	}
	defer func() {
		_ = response.Body.Close()
	}()

	if response.StatusCode != http.StatusOK {
		t.Fatalf("unexpected response status %d", response.StatusCode)
	}

	secondPollReq := newTunnelRequest(t, http.MethodGet, baseURL, &url.URL{Path: "/v1/tunnels/cli-tunnel/poll"}, mock, nil)
	secondPollResp, err := client.Do(secondPollReq)
	if err != nil {
		t.Fatalf("execute second poll: %v", err)
	}
	defer func() {
		_ = secondPollResp.Body.Close()
	}()
	if secondPollResp.StatusCode != http.StatusOK {
		t.Fatalf("unexpected second poll status %d", secondPollResp.StatusCode)
	}
	var secondEnvelope struct {
		Commands []json.RawMessage `json:"commands"`
	}
	if err := json.NewDecoder(secondPollResp.Body).Decode(&secondEnvelope); err != nil {
		t.Fatalf("decode second poll payload: %v", err)
	}
	if len(secondEnvelope.Commands) != 1 {
		t.Fatalf("expected 1 notification command, got %d", len(secondEnvelope.Commands))
	}
	delivered = mock.DeliveredCommands()
	if len(delivered) != 2 {
		t.Fatalf("expected 2 delivered commands, got %d", len(delivered))
	}

	notificationPayload := map[string]any{
		"request_id":   "cmd-2",
		"resp_code":    http.StatusAccepted,
		"resp_type":    "notification_ack",
		"resp_headers": map[string][]string{},
	}
	notificationBody, err := json.Marshal(notificationPayload)
	if err != nil {
		t.Fatalf("marshal notification payload: %v", err)
	}
	notificationReq := newTunnelRequest(t, http.MethodPost, baseURL, &url.URL{Path: "/v1/tunnels/cli-tunnel/response"}, mock, bytes.NewReader(notificationBody))
	notificationResp, err := client.Do(notificationReq)
	if err != nil {
		t.Fatalf("post notification response: %v", err)
	}
	defer func() {
		_ = notificationResp.Body.Close()
	}()
	if notificationResp.StatusCode != http.StatusOK {
		t.Fatalf("unexpected notification response status %d", notificationResp.StatusCode)
	}
	unmatchedPayload := map[string]any{
		"request_id": "unexpected-1",
		"resp_json": map[string]any{
			"jsonrpc": "2.0",
			"id":      99,
			"result":  map[string]any{"ok": false},
		},
		"resp_code": http.StatusBadGateway,
		"resp_type": "jsonrpc_response",
	}
	unmatchedBody, err := json.Marshal(unmatchedPayload)
	if err != nil {
		t.Fatalf("marshal unmatched payload: %v", err)
	}
	unmatchedReq := newTunnelRequest(t, http.MethodPost, baseURL, &url.URL{Path: "/v1/tunnels/cli-tunnel/response"}, mock, bytes.NewReader(unmatchedBody))
	unmatchedResp, err := client.Do(unmatchedReq)
	if err != nil {
		t.Fatalf("post unmatched response: %v", err)
	}
	defer func() {
		_ = unmatchedResp.Body.Close()
	}()
	if unmatchedResp.StatusCode != http.StatusOK {
		t.Fatalf("unexpected unmatched response status %d", unmatchedResp.StatusCode)
	}

	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()

	if err := mock.WaitForResponses(ctx, 2); err != nil {
		t.Fatalf("waiting for responses: %v", err)
	}

	responses := mock.ReceivedResponses(ResponseMatchAll)
	if len(responses) != 3 {
		t.Fatalf("expected 3 recorded responses, got %d", len(responses))
	}
	if got := responses[0].ResponseHeaders.Get("X-Mcp-Test"); got != "alpha" {
		t.Fatalf("unexpected resp_headers %v", responses[0].ResponseHeaders)
	}
	if !responses[0].MatchedCommand || !responses[1].MatchedCommand {
		t.Fatalf("expected first two responses to match commands: %+v", responses[:2])
	}
	if responses[1].ResponseCode != http.StatusAccepted || len(responses[1].JSONResponse) != 0 {
		t.Fatalf("unexpected notification response: %+v", responses[1])
	}
	if responses[2].MatchedCommand {
		t.Fatalf("unexpected response was marked as matched: %+v", responses[2])
	}
	matchedResponses := mock.ReceivedResponses(ResponseMatchMatched)
	if len(matchedResponses) != 2 {
		t.Fatalf("expected 2 matched responses, got %d", len(matchedResponses))
	}
	unexpectedResponses := mock.ReceivedResponses(ResponseMatchUnexpected)
	if len(unexpectedResponses) != 1 || unexpectedResponses[0].RequestID != "unexpected-1" {
		t.Fatalf("unexpected response filtering failure: %+v", unexpectedResponses)
	}
}

func TestWithInitializationPhaseCommands(t *testing.T) {
	t.Parallel()

	mock := NewMockTunnelService(
		WithTunnelID("cli-tunnel"),
		WithInitializationPhaseCommands(),
	)

	mock.Start(t)

	baseURL := mock.BaseURL()
	if baseURL == nil {
		t.Fatal("mock did not expose a base URL")
	}

	client := &http.Client{}

	firstPoll := newTunnelRequest(t, http.MethodGet, baseURL, &url.URL{Path: "/v1/tunnels/cli-tunnel/poll"}, mock, nil)
	firstResp, err := client.Do(firstPoll)
	if err != nil {
		t.Fatalf("execute first poll: %v", err)
	}
	defer func() { _ = firstResp.Body.Close() }()
	if firstResp.StatusCode != http.StatusOK {
		t.Fatalf("unexpected first poll status %d", firstResp.StatusCode)
	}

	var firstEnvelope wiretypes.PolledCommandEnvelope
	if err := json.NewDecoder(firstResp.Body).Decode(&firstEnvelope); err != nil {
		t.Fatalf("decode first poll payload: %v", err)
	}
	if len(firstEnvelope.Commands) != 1 {
		t.Fatalf("expected a single initialize command, got %d", len(firstEnvelope.Commands))
	}
	initCommand := firstEnvelope.Commands[0]
	var initRaw wiretypes.RawJSONRPCPolledCommand
	if err := json.Unmarshal(initCommand, &initRaw); err != nil {
		t.Fatalf("decode initialize raw command: %v", err)
	}
	var initRPC struct {
		Method string `json:"method"`
		ID     any    `json:"id"`
	}
	if err := json.Unmarshal(initRaw.JSONRPC, &initRPC); err != nil {
		t.Fatalf("decode initialize command: %v", err)
	}
	if initRPC.Method != "initialize" {
		t.Fatalf("unexpected method %q in initialize command", initRPC.Method)
	}

	sessionID := "session-from-server"
	initResponsePayload := map[string]any{
		"request_id": initRaw.RequestID,
		"resp_code":  http.StatusOK,
		"resp_type":  string(wiretypes.ResponsePayloadJSONRPC),
		"resp_json": map[string]any{
			"jsonrpc": "2.0",
			"id":      initRPC.ID,
			"result": map[string]any{
				"protocolVersion": "2025-06-18",
				"capabilities":    map[string]any{},
				"serverInfo": map[string]any{
					"name":    "mock-server",
					"version": "1.0.0",
				},
			},
		},
		"resp_headers": map[string][]string{
			"Mcp-Session-Id": {sessionID},
		},
	}
	initBody, err := json.Marshal(initResponsePayload)
	if err != nil {
		t.Fatalf("marshal initialize response: %v", err)
	}
	initRespReq := newTunnelRequest(t, http.MethodPost, baseURL, &url.URL{Path: "/v1/tunnels/cli-tunnel/response"}, mock, bytes.NewReader(initBody))
	initResp, err := client.Do(initRespReq)
	if err != nil {
		t.Fatalf("post initialize response: %v", err)
	}
	defer func() { _ = initResp.Body.Close() }()
	if initResp.StatusCode != http.StatusOK {
		t.Fatalf("unexpected initialize response status %d", initResp.StatusCode)
	}

	secondPoll := newTunnelRequest(t, http.MethodGet, baseURL, &url.URL{Path: "/v1/tunnels/cli-tunnel/poll"}, mock, nil)
	secondResp, err := client.Do(secondPoll)
	if err != nil {
		t.Fatalf("execute second poll: %v", err)
	}
	defer func() { _ = secondResp.Body.Close() }()
	if secondResp.StatusCode != http.StatusOK {
		t.Fatalf("unexpected second poll status %d", secondResp.StatusCode)
	}
	var secondEnvelope wiretypes.PolledCommandEnvelope
	if err := json.NewDecoder(secondResp.Body).Decode(&secondEnvelope); err != nil {
		t.Fatalf("decode second poll payload: %v", err)
	}
	if len(secondEnvelope.Commands) != 1 {
		t.Fatalf("expected notifications/initialized command, got %d", len(secondEnvelope.Commands))
	}
	notifyCommand := secondEnvelope.Commands[0]
	var notifyRaw wiretypes.RawJSONRPCPolledCommand
	if err := json.Unmarshal(notifyCommand, &notifyRaw); err != nil {
		t.Fatalf("decode notification raw command: %v", err)
	}
	var notifyRPC struct {
		Method string `json:"method"`
	}
	if err := json.Unmarshal(notifyRaw.JSONRPC, &notifyRPC); err != nil {
		t.Fatalf("decode notification command: %v", err)
	}
	if notifyRPC.Method != "notifications/initialized" {
		t.Fatalf("unexpected method %q for notifications/initialized command", notifyRPC.Method)
	}
	ackPayload := map[string]any{
		"request_id": notifyRaw.RequestID,
		"resp_code":  http.StatusNoContent,
		"resp_type":  string(wiretypes.ResponsePayloadNotifyAck),
	}
	ackBody, err := json.Marshal(ackPayload)
	if err != nil {
		t.Fatalf("marshal initialized ack: %v", err)
	}
	ackReq := newTunnelRequest(t, http.MethodPost, baseURL, &url.URL{Path: "/v1/tunnels/cli-tunnel/response"}, mock, bytes.NewReader(ackBody))
	ackResp, err := client.Do(ackReq)
	if err != nil {
		t.Fatalf("post initialized ack: %v", err)
	}
	defer func() { _ = ackResp.Body.Close() }()
	if ackResp.StatusCode != http.StatusOK {
		t.Fatalf("unexpected initialized ack status %d", ackResp.StatusCode)
	}

	snapshot := mock.SharedStorageSnapshot()
	if snapshot[sessionHeaderKey] != sessionID {
		t.Fatalf("expected session storage to include %q, snapshot=%v", sessionID, snapshot)
	}
}

func TestMockTunnelServiceWaitUntilIdle(t *testing.T) {
	t.Parallel()

	mock := NewMockTunnelService()

	ctx, cancel := context.WithTimeout(context.Background(), time.Millisecond*50)
	defer cancel()
	if err := mock.WaitUntilIdle(ctx); err != nil {
		t.Fatalf("unexpected wait error for empty script: %v", err)
	}

	mock.appendCommandResponses(CommandResponse{
		Command: NewCommand("cmd-1", json.RawMessage(`{"jsonrpc":"2.0","id":1,"method":"ping"}`), nil),
		ExpectedResponses: []ExpectedResponse{{
			RequestID: "cmd-1",
		}},
	})

	shortCtx, shortCancel := context.WithDeadline(
		context.Background(),
		time.Now().Add(-time.Nanosecond),
	)
	defer shortCancel()
	if err := mock.WaitUntilIdle(shortCtx); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("expected deadline exceeded waiting for pending work, got %v", err)
	}

	mock.mu.Lock()
	for _, slot := range mock.script {
		slot.delivered = true
		slot.completed = true
	}
	mock.mu.Unlock()

	ctx2, cancel2 := context.WithTimeout(context.Background(), time.Second)
	defer cancel2()
	if err := mock.WaitUntilIdle(ctx2); err != nil {
		t.Fatalf("wait for drained script failed: %v", err)
	}
}

func TestMockTunnelServicePollTimeoutReturnsEmptyCommands(t *testing.T) {
	t.Parallel()

	mock := NewMockTunnelService(
		WithTunnelID("cli-tunnel"),
		WithAPIKey("test-api-key"),
	)
	mock.Start(t)

	baseURL := mock.BaseURL()
	if baseURL == nil {
		t.Fatal("mock did not expose a base URL")
	}

	client := &http.Client{Timeout: 10 * time.Second}

	req := newTunnelRequest(t, http.MethodGet, baseURL, &url.URL{Path: "/v1/tunnels/cli-tunnel/poll"}, mock, nil)

	start := time.Now()
	resp, err := client.Do(req)
	if err != nil {
		t.Fatalf("poll request failed: %v", err)
	}
	defer func() {
		_ = resp.Body.Close()
	}()

	elapsed := time.Since(start)
	if elapsed < defaultPollWaitLimit/2 {
		t.Fatalf("poll returned too quickly: %s", elapsed)
	}
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("unexpected poll status %d", resp.StatusCode)
	}

	var envelope struct {
		Commands []json.RawMessage `json:"commands"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&envelope); err != nil {
		t.Fatalf("decode poll response: %v", err)
	}
	if len(envelope.Commands) != 0 {
		t.Fatalf("expected empty command list, got %d entries", len(envelope.Commands))
	}
}

func TestMockTunnelServiceWaitsForCommandDeliveryGate(t *testing.T) {
	t.Parallel()

	ready := make(chan struct{})
	mock := NewMockTunnelService(
		WithTunnelID("cli-tunnel"),
		WithAPIKey("test-api-key"),
		WithPollWaitLimit(10*time.Millisecond),
		WithAllowPendingCommands(),
		WithCommandResponses(CommandResponse{
			Command:      NewCommand("cmd-1", json.RawMessage(`{"jsonrpc":"2.0","id":1,"method":"ping"}`), nil),
			DeliverAfter: ready,
			ExpectedResponses: []ExpectedResponse{{
				RequestID: "cmd-1",
			}},
		}),
	)
	mock.Start(t)

	baseURL := mock.BaseURL()
	if baseURL == nil {
		t.Fatal("mock did not expose a base URL")
	}

	client := &http.Client{Timeout: 10 * time.Second}
	firstPollReq := newTunnelRequest(t, http.MethodGet, baseURL, &url.URL{Path: "/v1/tunnels/cli-tunnel/poll"}, mock, nil)
	firstPollResp, err := client.Do(firstPollReq)
	if err != nil {
		t.Fatalf("execute first poll: %v", err)
	}
	defer func() { _ = firstPollResp.Body.Close() }()

	var firstEnvelope wiretypes.PolledCommandEnvelope
	if err := json.NewDecoder(firstPollResp.Body).Decode(&firstEnvelope); err != nil {
		t.Fatalf("decode first poll payload: %v", err)
	}
	if len(firstEnvelope.Commands) != 0 {
		t.Fatalf("expected gated command to remain pending, got %d command(s)", len(firstEnvelope.Commands))
	}

	close(ready)

	secondPollReq := newTunnelRequest(t, http.MethodGet, baseURL, &url.URL{Path: "/v1/tunnels/cli-tunnel/poll"}, mock, nil)
	secondPollResp, err := client.Do(secondPollReq)
	if err != nil {
		t.Fatalf("execute second poll: %v", err)
	}
	defer func() { _ = secondPollResp.Body.Close() }()

	var secondEnvelope wiretypes.PolledCommandEnvelope
	if err := json.NewDecoder(secondPollResp.Body).Decode(&secondEnvelope); err != nil {
		t.Fatalf("decode second poll payload: %v", err)
	}
	if len(secondEnvelope.Commands) != 1 {
		t.Fatalf("expected gated command after readiness, got %d command(s)", len(secondEnvelope.Commands))
	}
}

func TestMockTunnelServiceBlocksUntilResponseBeforeDeliveringNextCommand(t *testing.T) {
	t.Parallel()

	mock := NewMockTunnelService(
		WithTunnelID("cli-tunnel"),
		WithAPIKey("test-api-key"),
		WithPollWaitLimit(500*time.Millisecond),
		WithCommandResponses(
			CommandResponse{
				Command: NewCommand("cmd-1", json.RawMessage(`{"jsonrpc":"2.0","id":1,"method":"ping"}`), nil),
				ExpectedResponses: []ExpectedResponse{{
					RequestID: "cmd-1",
				}},
			},
			CommandResponse{
				Command: NewCommand("cmd-2", json.RawMessage(`{"jsonrpc":"2.0","id":2,"method":"ping"}`), nil),
				ExpectedResponses: []ExpectedResponse{{
					RequestID: "cmd-2",
				}},
			},
		),
	)
	mock.Start(t)

	baseURL := mock.BaseURL()
	if baseURL == nil {
		t.Fatal("mock did not expose a base URL")
	}

	client := &http.Client{Timeout: 10 * time.Second}

	firstPollReq := newTunnelRequest(t, http.MethodGet, baseURL, &url.URL{Path: "/v1/tunnels/cli-tunnel/poll"}, mock, nil)

	firstPollResp, err := client.Do(firstPollReq)
	if err != nil {
		t.Fatalf("execute first poll: %v", err)
	}
	defer func() {
		_ = firstPollResp.Body.Close()
	}()

	if firstPollResp.StatusCode != http.StatusOK {
		t.Fatalf("unexpected first poll status %d", firstPollResp.StatusCode)
	}
	var firstEnvelope struct {
		Commands []json.RawMessage `json:"commands"`
	}
	if err := json.NewDecoder(firstPollResp.Body).Decode(&firstEnvelope); err != nil {
		t.Fatalf("decode first poll response: %v", err)
	}
	if len(firstEnvelope.Commands) != 1 {
		t.Fatalf("expected single command from first poll, got %d", len(firstEnvelope.Commands))
	}

	secondPollReq := newTunnelRequest(t, http.MethodGet, baseURL, &url.URL{Path: "/v1/tunnels/cli-tunnel/poll"}, mock, nil)

	secondPollRespCh := make(chan *http.Response, 1)
	secondPollErrCh := make(chan error, 1)
	go func() {
		resp, err := client.Do(secondPollReq)
		if err != nil {
			secondPollErrCh <- err
			return
		}
		secondPollRespCh <- resp
	}()

	select {
	case resp := <-secondPollRespCh:
		_ = resp.Body.Close()
		t.Fatal("second poll returned before first response was acknowledged")
	case err := <-secondPollErrCh:
		t.Fatalf("second poll failed early: %v", err)
	case <-time.After(200 * time.Millisecond):
	}

	postTunnelResponse := func(requestID string, id int, headers http.Header) {
		payload := map[string]any{
			"request_id": requestID,
			"resp_code":  http.StatusOK,
			"resp_type":  "jsonrpc_response",
			"resp_json": map[string]any{
				"jsonrpc": "2.0",
				"id":      id,
				"result":  map[string]any{"ok": true},
			},
		}
		if headers != nil {
			payload["resp_headers"] = headers
		}
		raw, err := json.Marshal(payload)
		if err != nil {
			t.Fatalf("marshal response payload: %v", err)
		}
		req := newTunnelRequest(t, http.MethodPost, baseURL, &url.URL{Path: "/v1/tunnels/cli-tunnel/response"}, mock, bytes.NewReader(raw))
		resp, err := client.Do(req)
		if err != nil {
			t.Fatalf("post response for %s: %v", requestID, err)
		}
		defer func() {
			_ = resp.Body.Close()
		}()
		if resp.StatusCode != http.StatusOK {
			t.Fatalf("unexpected response status %d for %s", resp.StatusCode, requestID)
		}
	}

	postTunnelResponse("cmd-1", 1, nil)

	var secondPollResp *http.Response
	select {
	case err := <-secondPollErrCh:
		t.Fatalf("second poll failed: %v", err)
	case resp := <-secondPollRespCh:
		secondPollResp = resp
	case <-time.After(time.Second):
		t.Fatal("second poll did not resume after response")
	}
	defer func() {
		_ = secondPollResp.Body.Close()
	}()

	if secondPollResp.StatusCode != http.StatusOK {
		t.Fatalf("unexpected second poll status %d", secondPollResp.StatusCode)
	}

	var secondEnvelope struct {
		Commands []json.RawMessage `json:"commands"`
	}
	if err := json.NewDecoder(secondPollResp.Body).Decode(&secondEnvelope); err != nil {
		t.Fatalf("decode second poll payload: %v", err)
	}
	if len(secondEnvelope.Commands) != 1 {
		t.Fatalf("expected single command in second poll, got %d", len(secondEnvelope.Commands))
	}

	var secondCommand map[string]any
	if err := json.Unmarshal(secondEnvelope.Commands[0], &secondCommand); err != nil {
		t.Fatalf("decode second command: %v", err)
	}
	if secondCommand["request_id"] != "cmd-2" {
		t.Fatalf("unexpected second command %v", secondCommand["request_id"])
	}

	postTunnelResponse("cmd-2", 2, nil)
}

func TestMockTunnelServiceSharedStorage(t *testing.T) {
	t.Parallel()

	mock := NewMockTunnelService(
		WithTunnelID("cli-tunnel"),
		WithAPIKey("test-api-key"),
		WithSessionHeaderPropagation(),
		WithCommandResponses(
			CommandResponse{
				Command: NewCommand("cmd-1", json.RawMessage(`{"jsonrpc":"2.0","id":1,"method":"init"}`), nil),
				ExpectedResponses: []ExpectedResponse{{
					RequestID: "cmd-1",
				}},
			},
			CommandResponse{
				Command: NewCommand("cmd-2", json.RawMessage(`{"jsonrpc":"2.0","id":2,"method":"ping"}`), nil),
				ExpectedResponses: []ExpectedResponse{{
					RequestID: "cmd-2",
				}},
			},
		),
	)

	mock.Start(t)

	baseURL := mock.BaseURL()
	if baseURL == nil {
		t.Fatal("mock did not expose a base URL")
	}

	client := &http.Client{}

	pollReq := newTunnelRequest(t, http.MethodGet, baseURL, &url.URL{Path: "/v1/tunnels/cli-tunnel/poll"}, mock, nil)
	firstPollResp, err := client.Do(pollReq)
	if err != nil {
		t.Fatalf("execute first poll: %v", err)
	}
	defer func() {
		_ = firstPollResp.Body.Close()
	}()
	if firstPollResp.StatusCode != http.StatusOK {
		t.Fatalf("unexpected first poll status %d", firstPollResp.StatusCode)
	}

	sessionHeaders := http.Header{
		"Mcp-Session-Id": {"session-xyz"},
	}
	firstResponsePayload := map[string]any{
		"request_id": "cmd-1",
		"resp_code":  http.StatusOK,
		"resp_type":  "jsonrpc_response",
		"resp_json": map[string]any{
			"jsonrpc": "2.0",
			"id":      1,
			"result":  map[string]any{"ok": true},
		},
		"resp_headers": sessionHeaders,
	}
	firstResponseBody, err := json.Marshal(firstResponsePayload)
	if err != nil {
		t.Fatalf("marshal first response payload: %v", err)
	}
	firstResponseReq := newTunnelRequest(t, http.MethodPost, baseURL, &url.URL{Path: "/v1/tunnels/cli-tunnel/response"}, mock, bytes.NewReader(firstResponseBody))
	firstResponseResp, err := client.Do(firstResponseReq)
	if err != nil {
		t.Fatalf("post first response: %v", err)
	}
	defer func() {
		_ = firstResponseResp.Body.Close()
	}()
	if firstResponseResp.StatusCode != http.StatusOK {
		t.Fatalf("unexpected first response status %d", firstResponseResp.StatusCode)
	}

	storageSnapshot := mock.SharedStorageSnapshot()
	if storageSnapshot[sessionHeaderKey] != "session-xyz" {
		t.Fatalf("expected shared storage to include session id, snapshot=%v", storageSnapshot)
	}

	secondPollReq := newTunnelRequest(t, http.MethodGet, baseURL, &url.URL{Path: "/v1/tunnels/cli-tunnel/poll"}, mock, nil)
	secondPollResp, err := client.Do(secondPollReq)
	if err != nil {
		t.Fatalf("execute second poll: %v", err)
	}
	defer func() {
		_ = secondPollResp.Body.Close()
	}()
	if secondPollResp.StatusCode != http.StatusOK {
		t.Fatalf("unexpected second poll status %d", secondPollResp.StatusCode)
	}

	var commandEnvelope wiretypes.PolledCommandEnvelope
	if err := json.NewDecoder(secondPollResp.Body).Decode(&commandEnvelope); err != nil {
		t.Fatalf("decode second poll payload: %v", err)
	}
	if len(commandEnvelope.Commands) != 1 {
		t.Fatalf("expected single command, got %d", len(commandEnvelope.Commands))
	}
	var secondRaw wiretypes.RawJSONRPCPolledCommand
	if err := json.Unmarshal(commandEnvelope.Commands[0], &secondRaw); err != nil {
		t.Fatalf("decode second raw command: %v", err)
	}
	if secondRaw.Headers.Get("Mcp-Session-Id") != "session-xyz" {
		t.Fatalf("command headers missing session id: %v", secondRaw.Headers)
	}

	finalResponsePayload := map[string]any{
		"request_id": "cmd-2",
		"resp_code":  http.StatusOK,
		"resp_type":  "jsonrpc_response",
		"resp_json": map[string]any{
			"jsonrpc": "2.0",
			"id":      2,
			"result":  map[string]any{"ok": true},
		},
	}
	finalBody, err := json.Marshal(finalResponsePayload)
	if err != nil {
		t.Fatalf("marshal final response payload: %v", err)
	}
	finalReq := newTunnelRequest(t, http.MethodPost, baseURL, &url.URL{Path: "/v1/tunnels/cli-tunnel/response"}, mock, bytes.NewReader(finalBody))
	finalResp, err := client.Do(finalReq)
	if err != nil {
		t.Fatalf("post final response: %v", err)
	}
	defer func() {
		_ = finalResp.Body.Close()
	}()
	if finalResp.StatusCode != http.StatusOK {
		t.Fatalf("unexpected final response status %d", finalResp.StatusCode)
	}
}

func newTunnelRequest(t testing.TB, method string, baseURL *url.URL, rel *url.URL, mock *MockTunnelService, body io.Reader) *http.Request {
	t.Helper()
	target := baseURL.ResolveReference(rel)
	req, err := http.NewRequest(method, target.String(), body)
	if err != nil {
		t.Fatalf("build %s %s request: %v", method, target.Path, err)
	}
	req.Header.Set("Authorization", "Bearer "+mock.APIKey())
	req.Header.Set("Accept", "application/json")
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	return req
}
