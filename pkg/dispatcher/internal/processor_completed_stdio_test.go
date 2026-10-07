package dispatcherinternal

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/modelcontextprotocol/go-sdk/jsonrpc"
	"github.com/stretchr/testify/require"

	"github.com/openai/tunnel-client/pkg/mcpclient"
	"github.com/openai/tunnel-client/pkg/types"
)

func TestProcessorStdioCompletedResponsePostFailurePreservesSharedConnection(t *testing.T) {
	t.Parallel()
	for _, method := range []string{"ping", "initialize"} {
		for _, responseError := range []bool{false, true} {
			name := method + "/result"
			if responseError {
				name = method + "/error"
			}
			t.Run(name, func(t *testing.T) {
				t.Parallel()
				first := issue81Command(t, "first", method)
				next := issue81Command(t, "next", "ping")
				response := &jsonrpc.Response{ID: first.message.(*jsonrpc.Request).ID, Result: json.RawMessage(`{"ok":true}`)}
				if responseError {
					response.Result = nil
					response.Error = errors.New("MCP request rejected")
				}
				base := &issue81ForwardingConnection{scriptedForwardingConnection: &scriptedForwardingConnection{
					statusCode: http.StatusOK,
					readSteps: []readStep{
						{msg: response},
						{msg: &jsonrpc.Response{ID: next.message.(*jsonrpc.Request).ID, Result: json.RawMessage(`{"ok":true}`)}},
					},
				}}
				transport := mcpclient.NewStdioForwardingTransportWithOptions(&stubForwardingTransport{conn: base}, mcpclient.StdioForwardingOptions{})
				recorder := newRecordingResponder()
				responder := &issue81PostFailureResponder{failedRequest: first.id, next: recorder}
				processor := newDeadlineTestProcessor(t, transport, responder)

				require.NoError(t, processor.Process(context.Background(), first))
				require.False(t, base.isClosed(), "a completed stdio response must survive control-plane delivery failure")
				require.EqualValues(t, 1, responder.failedPosts.Load(), "a delivery failure must not replay MCP work")
				require.EqualValues(t, 1, base.writes.Load())

				if method == "initialize" {
					initialized := issue81Command(t, "initialized", "notifications/initialized")
					initialized.message.(*jsonrpc.Request).ID = jsonrpc.ID{}
					require.NoError(t, processor.Process(context.Background(), initialized))
					require.Equal(t, types.ResponseTypeNotificationAcknowledgment, recorder.waitForResponse(t).response.Type())
					operation := issue81Command(t, "operation", "tools/call")
					if !responseError {
						base.mu.Lock()
						base.readSteps = append([]readStep{{msg: &jsonrpc.Response{ID: operation.message.(*jsonrpc.Request).ID, Result: json.RawMessage(`{"ok":true}`)}}}, base.readSteps...)
						base.mu.Unlock()
					}
					writesBefore := base.writes.Load()
					require.NoError(t, processor.Process(context.Background(), operation))
					operationResponse := decodeJSONRPCResponse(t, recorder.waitForResponse(t).response.Payload())
					if responseError {
						require.Error(t, operationResponse.Error, "a failed initialize response must not open readiness")
						require.Equal(t, writesBefore, base.writes.Load(), "rejected legacy operation must not reach the child")
					} else {
						require.NoError(t, operationResponse.Error, "a successful handshake must survive failed response delivery")
						require.Equal(t, writesBefore+1, base.writes.Load())
					}
				}

				require.NoError(t, processor.Process(context.Background(), next))
				nextResponse := decodeJSONRPCResponse(t, recorder.waitForResponse(t).response.Payload())
				require.Equal(t, next.message.(*jsonrpc.Request).ID, nextResponse.ID)
				require.NoError(t, nextResponse.Error)
				require.False(t, base.isClosed())
			})
		}
	}
}

func TestProcessorStdioIncompleteResponseStillClosesSharedConnection(t *testing.T) {
	t.Parallel()
	id, err := jsonrpc.MakeID("incomplete")
	require.NoError(t, err)
	otherID, err := jsonrpc.MakeID("other")
	require.NoError(t, err)
	cases := []struct {
		name  string
		steps []readStep
	}{
		{"malformed_result", []readStep{{msg: &jsonrpc.Response{ID: id, Result: json.RawMessage(`{`)}}}},
		{"invalid_id", []readStep{{msg: &jsonrpc.Response{Result: json.RawMessage(`{}`)}}}},
		{"mismatched_id", []readStep{{msg: &jsonrpc.Response{ID: otherID, Result: json.RawMessage(`{}`)}}}},
		{"non_response", []readStep{{msg: &jsonrpc.Request{ID: id, Method: "unexpected"}}}},
		{"nil_message", []readStep{{}}},
		{"eof", []readStep{{err: io.EOF}}},
		{"read_error", []readStep{{err: errors.New("read failed")}}},
		{"canceled_read", []readStep{{err: context.Canceled}}},
		{"malformed_notification", []readStep{{msg: &jsonrpc.Request{Method: "notifications/progress", Params: json.RawMessage(`{`)}}}},
		{"notification_without_terminal_response", []readStep{{msg: &jsonrpc.Request{Method: "notifications/progress"}}, {err: io.EOF}}},
	}
	for _, tc := range cases {
		for _, postFails := range []bool{false, true} {
			name := tc.name + "/posted_error"
			if postFails {
				name = tc.name + "/post_failure"
			}
			t.Run(name, func(t *testing.T) {
				t.Parallel()
				base := &issue81ForwardingConnection{scriptedForwardingConnection: &scriptedForwardingConnection{
					statusCode: http.StatusOK,
					readSteps:  append([]readStep(nil), tc.steps...),
				}}
				transport := mcpclient.NewStdioDeadlineRetiringForwardingTransport(&stubForwardingTransport{conn: base})
				responder := &countingResponder{}
				if postFails {
					responder.err = errors.New("control-plane POST returned HTTP 520")
				}
				processor := newDeadlineTestProcessor(t, transport, responder)
				command := issue81Command(t, "incomplete", "ping")
				require.NoError(t, processor.Process(context.Background(), command))
				require.True(t, base.isClosed(), "malformed or incomplete stdio lifecycle must keep physical cleanup")
				require.EqualValues(t, 1, base.writes.Load())
			})
		}
	}
}

func TestProcessorStdioPostFailureDoesNotCloseNextActiveRequest(t *testing.T) {
	t.Parallel()
	first := issue81Command(t, "first", "ping")
	second := issue81Command(t, "second", "ping")
	base := &issue81OverlappingConnection{
		firstID: first.message.(*jsonrpc.Request).ID, secondID: second.message.(*jsonrpc.Request).ID,
		secondWritten: make(chan struct{}), finishSecond: make(chan struct{}),
	}
	transport := mcpclient.NewStdioDeadlineRetiringForwardingTransport(&stubForwardingTransport{conn: base})
	recorder := newRecordingResponder()
	responder := &issue81PostFailureResponder{
		failedRequest: first.id, next: recorder, started: make(chan struct{}), release: make(chan struct{}),
	}
	processor := newDeadlineTestProcessor(t, transport, responder)
	var releaseFirstOnce, finishSecondOnce sync.Once
	ctx := t.Context()
	t.Cleanup(func() {
		releaseFirstOnce.Do(func() { close(responder.release) })
		finishSecondOnce.Do(func() { close(base.finishSecond) })
	})
	firstDone := make(chan error, 1)
	go func() { firstDone <- processor.Process(ctx, first) }()
	waitForSignal(t, responder.started, "first response POST to block")
	secondDone := make(chan error, 1)
	go func() { secondDone <- processor.Process(ctx, second) }()
	waitForSignal(t, base.secondWritten, "second request to own the shared transport")
	releaseFirstOnce.Do(func() { close(responder.release) })
	require.NoError(t, waitForResult(t, firstDone, "first response POST to fail"))
	closedAfterFirst := base.closed.Load()
	finishSecondOnce.Do(func() { close(base.finishSecond) })
	require.NoError(t, waitForResult(t, secondDone, "second request to finish"))
	require.False(t, closedAfterFirst, "an earlier POST failure must not close the next active request")
	require.EqualValues(t, 2, base.writes.Load(), "each MCP command must execute only once")
	response := decodeJSONRPCResponse(t, recorder.waitForResponse(t).response.Payload())
	require.Equal(t, second.message.(*jsonrpc.Request).ID, response.ID)
	require.NoError(t, response.Error)
}

func issue81Command(t *testing.T, name, method string) *fakePolledCommand {
	t.Helper()
	id, err := jsonrpc.MakeID(name)
	require.NoError(t, err)
	return &fakePolledCommand{
		id: types.RequestID(name), message: &jsonrpc.Request{ID: id, Method: method},
		enqueuedAt: time.Now(), polledAt: time.Now(), shardToken: "fixture-shard",
	}
}

type issue81ForwardingConnection struct {
	*scriptedForwardingConnection
	writes atomic.Int32
}

func (c *issue81ForwardingConnection) isClosed() bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.closed
}

func (c *issue81ForwardingConnection) Write(ctx context.Context, headers http.Header, msg jsonrpc.Message) (mcpclient.ForwardingWriteResult, error) {
	if c.isClosed() {
		return mcpclient.ForwardingWriteResult{}, io.ErrClosedPipe
	}
	c.writes.Add(1)
	return c.scriptedForwardingConnection.Write(ctx, headers, msg)
}

type issue81PostFailureResponder struct {
	failedRequest types.RequestID
	next          *recordingResponder
	failedPosts   atomic.Int32
	started       chan struct{}
	release       chan struct{}
}

func (r *issue81PostFailureResponder) PostResponse(ctx context.Context, requestID types.RequestID, response *types.TunnelResponse) (types.TunnelServiceRequestID, error) {
	if requestID != r.failedRequest {
		return r.next.PostResponse(ctx, requestID, response)
	}
	r.failedPosts.Add(1)
	if r.started != nil {
		close(r.started)
		select {
		case <-r.release:
		case <-ctx.Done():
			return "", ctx.Err()
		}
	}
	return "", errors.New("control-plane POST returned HTTP 520")
}

type issue81OverlappingConnection struct {
	firstID       jsonrpc.ID
	secondID      jsonrpc.ID
	secondWritten chan struct{}
	finishSecond  chan struct{}
	closed        atomic.Bool
	writes        atomic.Int32
	reads         atomic.Int32
}

func (c *issue81OverlappingConnection) Write(_ context.Context, _ http.Header, msg jsonrpc.Message) (mcpclient.ForwardingWriteResult, error) {
	if c.closed.Load() {
		return mcpclient.ForwardingWriteResult{}, io.ErrClosedPipe
	}
	c.writes.Add(1)
	if msg.(*jsonrpc.Request).ID == c.secondID {
		close(c.secondWritten)
	}
	return mcpclient.ForwardingWriteResult{StatusCode: http.StatusOK}, nil
}

func (c *issue81OverlappingConnection) Read(ctx context.Context) (jsonrpc.Message, error) {
	id := c.firstID
	if c.reads.Add(1) > 1 {
		select {
		case <-c.finishSecond:
		case <-ctx.Done():
			return nil, ctx.Err()
		}
		if c.closed.Load() {
			return nil, io.ErrClosedPipe
		}
		id = c.secondID
	}
	return &jsonrpc.Response{ID: id, Result: json.RawMessage(`{"ok":true}`)}, nil
}

func (c *issue81OverlappingConnection) Close() error {
	c.closed.Store(true)
	return nil
}
