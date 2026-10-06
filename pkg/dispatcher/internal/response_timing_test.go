package dispatcherinternal

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"testing"
	"time"

	"github.com/modelcontextprotocol/go-sdk/jsonrpc"
	"github.com/stretchr/testify/require"

	"github.com/openai/tunnel-client/pkg/config"
	"github.com/openai/tunnel-client/pkg/mcpclient"
	"github.com/openai/tunnel-client/pkg/types"
)

func TestProcessorResponseTimingRequiresMeasuredTerminalResponse(t *testing.T) {
	t.Parallel()
	id, err := jsonrpc.MakeID("timed-call")
	require.NoError(t, err)
	otherID, err := jsonrpc.MakeID("other-call")
	require.NoError(t, err)
	result := &jsonrpc.Response{ID: id, Result: json.RawMessage(`{"ok":true}`)}
	targetError := &jsonrpc.Response{ID: id, Error: &jsonrpc.Error{Code: -32042, Message: "target failed"}}
	preserved := mcpclient.NewPreservedMCPError([]byte(`{"jsonrpc":"2.0","id":"timed-call","error":{"code":-32042,"message":"target failed"}}`), -32042)

	cases := []struct {
		name         string
		kind         config.MCPTransportKind
		steps        []readStep
		preserved    *mcpclient.PreservedMCPError
		writeErr     error
		unmeasured   bool
		notification bool
		wantTiming   bool
	}{
		{name: "stdio result", kind: config.MCPTransportStdio, steps: []readStep{{msg: result}}, wantTiming: true},
		{name: "HTTP result", kind: config.MCPTransportHTTPStreamable, steps: []readStep{{msg: result}}, wantTiming: true},
		{name: "stdio target error", kind: config.MCPTransportStdio, steps: []readStep{{msg: targetError}}, wantTiming: true},
		{name: "HTTP target error", kind: config.MCPTransportHTTPStreamable, steps: []readStep{{msg: targetError}}, wantTiming: true},
		{name: "preserved target error", kind: config.MCPTransportHTTPStreamable, preserved: preserved, writeErr: errors.New("HTTP error"), wantTiming: true},
		{name: "local preserved rejection", kind: config.MCPTransportStdio, preserved: preserved, unmeasured: true},
		{name: "unmeasured result", kind: config.MCPTransportStdio, steps: []readStep{{msg: result}}, unmeasured: true},
		{name: "in-memory result", kind: config.MCPTransportInMemory, steps: []readStep{{msg: result}}},
		{name: "notification acknowledgment", kind: config.MCPTransportHTTPStreamable, notification: true},
		{name: "mismatched ID", kind: config.MCPTransportHTTPStreamable, steps: []readStep{{msg: &jsonrpc.Response{ID: otherID, Result: result.Result}}}},
		{name: "invalid ID", kind: config.MCPTransportStdio, steps: []readStep{{msg: &jsonrpc.Response{Result: result.Result}}}},
		{name: "unencodable response", kind: config.MCPTransportStdio, steps: []readStep{{msg: &jsonrpc.Response{ID: id, Result: json.RawMessage(`invalid`)}}}},
		{name: "synthetic write failure", kind: config.MCPTransportHTTPStreamable, writeErr: errors.New("transport failed")},
		{name: "synthetic read failure", kind: config.MCPTransportStdio, steps: []readStep{{err: io.EOF}}},
		{name: "progress then result", kind: config.MCPTransportHTTPStreamable, steps: []readStep{{msg: &jsonrpc.Request{Method: "notifications/progress", Params: json.RawMessage(`{"progress":1}`)}}, {msg: result}}, wantTiming: true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			status := http.StatusOK
			if tc.preserved != nil {
				status = http.StatusBadRequest
			}
			conn := &timingForwardingConnection{ForwardingConnection: &scriptedForwardingConnection{
				statusCode: status, preservedError: tc.preserved, writeErr: tc.writeErr, readSteps: tc.steps,
			}}
			if !tc.unmeasured {
				conn.startedAt = time.Now().Add(-time.Millisecond)
				conn.completedAt = conn.startedAt.Add(125 * time.Microsecond)
			}
			bindings := newTestChannelBindings(&stubForwardingTransport{conn: conn})
			main := bindings[types.DefaultChannel]
			main.TransportKind = tc.kind
			bindings[types.DefaultChannel] = main
			responder := newRecordingResponder()
			processor, err := NewProcessor(processorParams{
				Logger:          slog.New(slog.NewTextHandler(io.Discard, nil)),
				ChannelBindings: bindings,
				TunnelResponder: responder,
				MCPConfig:       newTestMCPConfig(t, time.Second),
				OAuthHTTPClient: &http.Client{},
				ControlPlaneCfg: newTestControlPlaneConfig(t),
				MeterProvider:   newTestMeterProvider(t),
			})
			require.NoError(t, err)
			req := &jsonrpc.Request{ID: id, Method: "ping"}
			if tc.notification {
				req = &jsonrpc.Request{Method: "notifications/initialized"}
			}
			cmd := &fakePolledCommand{id: "timed-request", message: req, enqueuedAt: time.Now(), polledAt: time.Now(), shardToken: "timed-shard"}
			require.NoError(t, processor.Process(context.Background(), cmd))
			if len(tc.steps) > 1 {
				progress := responder.waitForResponse(t).response
				require.Equal(t, types.ResponseTypeJSONRPCNotification, progress.Type())
				require.Nil(t, progress.Timing())
			}
			response := responder.waitForResponse(t).response
			if !tc.wantTiming {
				require.Nil(t, response.Timing())
				return
			}
			timing := response.Timing()
			require.NotNil(t, timing)
			require.Equal(t, 1, timing.Version)
			transport := "streamable_http"
			if tc.kind == config.MCPTransportStdio {
				transport = "stdio"
			}
			require.Equal(t, transport, timing.Transport)
			if tc.preserved != nil {
				require.EqualValues(t, 125, timing.TargetElapsedUS)
			} else {
				require.GreaterOrEqual(t, timing.TargetElapsedUS, int64(1_000))
			}
		})
	}
}

func TestProcessorMeasuredWriteCancellationPostsNoTimingOrResponse(t *testing.T) {
	t.Parallel()
	for _, responseDeadline := range []bool{false, true} {
		name := "parent cancellation"
		if responseDeadline {
			name = "response deadline retires stdio"
		}
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			base := newDeadlineRetiringConnection()
			conn := &timingRetiringConnection{timingForwardingConnection: &timingForwardingConnection{
				ForwardingConnection: base, startedAt: time.Now(), completedAt: time.Now(),
			}, retiring: base}
			responder := &countingResponder{}
			processor := newDeadlineTestProcessor(t, &stubForwardingTransport{conn: conn}, responder)
			main := processor.channels[types.DefaultChannel]
			main.transportKind = config.MCPTransportStdio
			processor.channels[types.DefaultChannel] = main
			ctx, cancel := context.WithCancelCause(context.Background())
			defer cancel(context.Canceled)
			if responseDeadline {
				processor.withDeadlineCause = func(ctx context.Context, _ time.Time, _ error) (context.Context, context.CancelFunc) {
					deadlineCtx, cancelCause := context.WithCancelCause(ctx)
					cancel = cancelCause
					return deadlineCtx, func() { cancelCause(context.Canceled) }
				}
			}
			id, err := jsonrpc.MakeID("canceled-timed-call")
			require.NoError(t, err)
			cmd := &fakePolledCommand{
				id: "canceled-timed-command", message: &jsonrpc.Request{ID: id, Method: "ping"}, shardToken: "timed-shard",
				hasResponseDeadline: responseDeadline, responseDeadline: time.Now().Add(time.Hour),
			}
			done := make(chan error, 1)
			go func() { done <- processor.Process(ctx, cmd) }()
			waitForSignal(t, base.readStarted, "measured MCP write to reach response read")
			cause := context.Canceled
			if responseDeadline {
				cause = errResponseDeadlineExceeded
			}
			cancel(cause)
			require.NoError(t, waitForResult(t, done, "processor to stop after cancellation"))
			require.Zero(t, responder.calls.Load(), "a measured write without a terminal response must not post timing or synthesize a response")
			require.Equal(t, responseDeadline, base.retired.Load())
			if responseDeadline {
				select {
				case <-base.closed:
					t.Fatal("response deadline must retire the shared connection without closing it")
				default:
				}
			} else {
				waitForSignal(t, base.closed, "canceled MCP connection to close")
			}
		})
	}
}

func TestResponseTimingBoundsAndZero(t *testing.T) {
	t.Parallel()
	startedAt := time.Now()
	max := time.Duration(types.MaxTargetElapsedUS) * time.Microsecond
	for _, tc := range []struct {
		name     string
		elapsed  time.Duration
		wantUS   int64
		wantOmit bool
	}{
		{name: "zero", wantUS: 0},
		{name: "microseconds", elapsed: 1_234*time.Microsecond + 999*time.Nanosecond, wantUS: 1_234},
		{name: "maximum", elapsed: max, wantUS: types.MaxTargetElapsedUS},
		{name: "negative", elapsed: -time.Nanosecond, wantOmit: true},
		{name: "above maximum before rounding", elapsed: max + time.Nanosecond, wantOmit: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			response := types.NewTunnelResponse(types.DefaultChannel, nil, http.StatusOK, nil)
			response = withResponseTiming(response, config.MCPTransportStdio, startedAt, startedAt.Add(tc.elapsed))
			if tc.wantOmit {
				require.Nil(t, response.Timing())
			} else {
				require.Equal(t, &types.ResponseTiming{Version: 1, Transport: "stdio", TargetElapsedUS: tc.wantUS}, response.Timing())
			}
		})
	}
}

type timingForwardingConnection struct {
	mcpclient.ForwardingConnection
	startedAt, completedAt time.Time
}

func (c *timingForwardingConnection) Write(ctx context.Context, headers http.Header, message jsonrpc.Message) (mcpclient.ForwardingWriteResult, error) {
	result, err := c.ForwardingConnection.Write(ctx, headers, message)
	result.StartedAt, result.CompletedAt = c.startedAt, c.completedAt
	return result, err
}

type timingRetiringConnection struct {
	*timingForwardingConnection
	retiring mcpclient.ResponseDeadlineRetiringConnection
}

func (c *timingRetiringConnection) RetireResponseDeadline() bool {
	return c.retiring.RetireResponseDeadline()
}
