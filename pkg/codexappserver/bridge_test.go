package codexappserver

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestBridgeSupportsLoginThreadAndTurn(t *testing.T) {
	bridge := newMockBridge(t)

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	require.NoError(t, bridge.EnsureStarted(ctx))
	snapshot := bridge.Snapshot()
	require.Equal(t, "worker@example.com", snapshot.Account.Email)
	require.True(t, snapshot.Ready)

	login, err := bridge.StartDeviceCodeLogin(ctx)
	require.NoError(t, err)
	require.Equal(t, "login_123", login.LoginID)

	require.NoError(t, bridge.CancelLogin(ctx, login.LoginID))
	require.Eventually(t, func() bool {
		current := bridge.Snapshot()
		return current.Login != nil && !current.Login.Pending
	}, 2*time.Second, 50*time.Millisecond)

	thread, err := bridge.StartThread(ctx, ThreadStartParams{
		CWD:                   "/workspace/openai",
		Model:                 "gpt-5.5",
		ApprovalPolicy:        "never",
		SandboxType:           "dangerFullAccess",
		DeveloperInstructions: "focus on tunnels",
	})
	require.NoError(t, err)
	require.Equal(t, "thread_123", thread.ThreadID)
	require.Equal(t, "danger-full-access", thread.Sandbox)

	require.NoError(t, bridge.InjectThreadItems(ctx, thread.ThreadID, []map[string]any{
		{
			"type": "message",
			"role": "developer",
			"content": []map[string]any{
				{"type": "input_text", "text": "tunnel context"},
			},
		},
	}))

	turn, err := bridge.StartTurn(ctx, TurnStartParams{
		ThreadID:       thread.ThreadID,
		Input:          []map[string]any{{"type": "message", "role": "user", "content": "diagnose"}},
		ApprovalPolicy: "never",
		SandboxType:    "dangerFullAccess",
	})
	require.NoError(t, err)
	require.Equal(t, "turn_456", turn.TurnID)

	require.Eventually(t, func() bool {
		current := bridge.Snapshot()
		return current.Turn != nil && current.Turn.Status == "completed"
	}, 2*time.Second, 50*time.Millisecond)
	require.Contains(t, bridge.RecentEvents(20)[len(bridge.RecentEvents(20))-1].Method, "turn/completed")

	stopCtx, stopCancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer stopCancel()
	require.NoError(t, bridge.Stop(stopCtx))
}

func TestBridgeStartThreadTimeoutIncludesRecentDiagnostics(t *testing.T) {
	bridge := NewBridge(nil, nil)
	bridge.mu.Lock()
	bridge.ready = true
	bridge.stdin = discardWriteCloser{}
	bridge.thread = &ThreadState{ID: "thread_ready"}
	bridge.mu.Unlock()

	bridge.appendStderrLine("thread/start is stuck")
	bridge.publish(Event{
		Time:    time.Now().UTC(),
		Source:  "notification",
		Method:  "thread/started",
		Summary: "thread started",
	})

	ctx, cancel := expiredDeadlineContext()
	defer cancel()

	_, err := bridge.StartThread(ctx, ThreadStartParams{CWD: "/workspace/openai"})
	require.Error(t, err)
	require.ErrorContains(t, err, "thread/start timed out")
	require.ErrorContains(t, err, "recent stderr: thread/start is stuck")
	require.ErrorContains(t, err, "recent bridge events: thread/started thread started")
}

func TestBridgeStartTurnTimeoutIncludesRecentDiagnostics(t *testing.T) {
	bridge := NewBridge(nil, nil)
	bridge.mu.Lock()
	bridge.ready = true
	bridge.stdin = discardWriteCloser{}
	bridge.thread = &ThreadState{ID: "thread_123"}
	bridge.mu.Unlock()

	bridge.appendStderrLine("turn/start is stuck")
	bridge.publish(Event{
		Time:     time.Now().UTC(),
		Source:   "notification",
		Method:   "thread/started",
		ThreadID: "thread_123",
		Summary:  "thread started",
	})

	ctx, cancel := expiredDeadlineContext()
	defer cancel()

	_, err := bridge.StartTurn(ctx, TurnStartParams{
		ThreadID: "thread_123",
		Input:    []map[string]any{{"type": "text", "text": "diagnose"}},
	})
	require.Error(t, err)
	require.ErrorContains(t, err, "turn/start timed out")
	require.ErrorContains(t, err, "recent stderr: turn/start is stuck")
	require.ErrorContains(t, err, "recent bridge events: thread/started thread started")
}

func newMockBridge(t *testing.T) *Bridge {
	t.Helper()

	bridge := NewBridge(nil, nil)

	requestReader, requestWriter := io.Pipe()
	responseReader, responseWriter := io.Pipe()
	serverDone := make(chan error, 1)

	requiresAuth := true
	bridge.mu.Lock()
	bridge.ready = true
	bridge.startedAt = time.Now().UTC()
	bridge.stdin = requestWriter
	bridge.requiresAuth = &requiresAuth
	bridge.authMethod = "chatgpt"
	bridge.account = &Account{
		Type:     "chatgpt",
		Email:    "worker@example.com",
		PlanType: "business",
	}
	bridge.mu.Unlock()

	go bridge.readStdout(responseReader)
	go func() {
		defer func() { _ = responseWriter.Close() }()
		serverDone <- runMockCodexAppServer(requestReader, responseWriter)
	}()

	t.Cleanup(func() {
		stopCtx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		_ = bridge.Stop(stopCtx)
		select {
		case err := <-serverDone:
			require.NoError(t, err)
		case <-time.After(2 * time.Second):
			t.Fatal("timed out waiting for mock codex app-server to stop")
		}
	})

	return bridge
}

func expiredDeadlineContext() (context.Context, context.CancelFunc) {
	return context.WithDeadline(context.Background(), time.Now().Add(-time.Nanosecond))
}

type discardWriteCloser struct{}

func (discardWriteCloser) Write(data []byte) (int, error) {
	return len(data), nil
}

func (discardWriteCloser) Close() error {
	return nil
}

func runMockCodexAppServer(requests io.Reader, responses io.Writer) error {
	scanner := bufio.NewScanner(requests)
	scanner.Buffer(make([]byte, 0, 16*1024), 1024*1024)
	encoder := json.NewEncoder(responses)

	write := func(payload any) error {
		return encoder.Encode(payload)
	}

	for scanner.Scan() {
		line := scanner.Bytes()
		if len(line) == 0 {
			continue
		}

		var envelope struct {
			ID     any             `json:"id,omitempty"`
			Method string          `json:"method,omitempty"`
			Params json.RawMessage `json:"params,omitempty"`
		}
		if err := json.Unmarshal(line, &envelope); err != nil {
			return fmt.Errorf("decode request envelope: %w", err)
		}

		var params map[string]any
		if len(envelope.Params) > 0 {
			if err := json.Unmarshal(envelope.Params, &params); err != nil {
				return fmt.Errorf("decode params for %s: %w", envelope.Method, err)
			}
		}

		switch envelope.Method {
		case "account/login/start":
			if err := write(map[string]any{
				"id": envelope.ID,
				"result": map[string]any{
					"type":            "chatgptDeviceCode",
					"loginId":         "login_123",
					"verificationUrl": "https://auth.openai.com/codex/device",
					"userCode":        "ABCD-EFGH",
				},
			}); err != nil {
				return err
			}
		case "account/login/cancel":
			if stringValue(params["loginId"]) != "login_123" {
				return fmt.Errorf("unexpected loginId: %v", params["loginId"])
			}
			if err := write(map[string]any{
				"id":     envelope.ID,
				"result": map[string]any{"status": "canceled"},
			}); err != nil {
				return err
			}
			if err := write(map[string]any{
				"method": "account/login/completed",
				"params": map[string]any{
					"loginId": "login_123",
					"success": false,
					"error":   "Login was not completed",
				},
			}); err != nil {
				return err
			}
		case "thread/start":
			if stringValue(params["sandbox"]) != "danger-full-access" {
				return fmt.Errorf("unexpected thread sandbox: %v", params["sandbox"])
			}
			if stringValue(params["cwd"]) != "/workspace/openai" {
				return fmt.Errorf("unexpected thread cwd: %v", params["cwd"])
			}
			if err := write(map[string]any{
				"id": envelope.ID,
				"result": map[string]any{
					"thread": map[string]any{
						"id":        "thread_123",
						"preview":   "Explain tunnel state",
						"cwd":       "/workspace/openai",
						"createdAt": 1713740000,
						"updatedAt": 1713740001,
					},
					"model":          "gpt-5.5",
					"modelProvider":  "openai",
					"approvalPolicy": "never",
					"sandbox":        "danger-full-access",
				},
			}); err != nil {
				return err
			}
			if err := write(map[string]any{
				"method": "thread/started",
				"params": map[string]any{"threadId": "thread_123"},
			}); err != nil {
				return err
			}
		case "thread/inject_items":
			if stringValue(params["threadId"]) != "thread_123" {
				return fmt.Errorf("unexpected inject thread id: %v", params["threadId"])
			}
			if err := write(map[string]any{
				"id":     envelope.ID,
				"result": map[string]any{},
			}); err != nil {
				return err
			}
		case "turn/start":
			if stringValue(params["threadId"]) != "thread_123" {
				return fmt.Errorf("unexpected turn thread id: %v", params["threadId"])
			}
			sandboxPolicy, _ := params["sandboxPolicy"].(map[string]any)
			if stringValue(sandboxPolicy["type"]) != "dangerFullAccess" {
				return fmt.Errorf("unexpected turn sandbox policy: %#v", params["sandboxPolicy"])
			}
			if err := write(map[string]any{
				"id": envelope.ID,
				"result": map[string]any{
					"turn": map[string]any{
						"id":     "turn_456",
						"status": "in_progress",
					},
				},
			}); err != nil {
				return err
			}
			if err := write(map[string]any{
				"method": "turn/started",
				"params": map[string]any{
					"threadId": "thread_123",
					"turnId":   "turn_456",
				},
			}); err != nil {
				return err
			}
			if err := write(map[string]any{
				"method": "item/agentMessage/delta",
				"params": map[string]any{
					"threadId": "thread_123",
					"turnId":   "turn_456",
					"delta":    "hello from codex",
				},
			}); err != nil {
				return err
			}
			if err := write(map[string]any{
				"method": "turn/completed",
				"params": map[string]any{
					"threadId": "thread_123",
					"turnId":   "turn_456",
					"status":   "completed",
				},
			}); err != nil {
				return err
			}
		default:
			return fmt.Errorf("unexpected method: %s", envelope.Method)
		}
	}

	if err := scanner.Err(); err != nil {
		return err
	}
	return nil
}
