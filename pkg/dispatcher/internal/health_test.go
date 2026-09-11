package dispatcherinternal

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"math"
	"net/http"
	"net/http/httptest"
	"net/url"
	"testing"
	"time"

	"github.com/modelcontextprotocol/go-sdk/jsonrpc"
	"github.com/stretchr/testify/require"

	"github.com/openai/tunnel-client/pkg/controlplane"
	"github.com/openai/tunnel-client/pkg/healthstate"
	"github.com/openai/tunnel-client/pkg/runtimeconfig"
	"github.com/openai/tunnel-client/pkg/types"
)

func TestActivityHealthCapturesOAuthGatewayTimeoutWithValidMetadata(t *testing.T) {
	t.Parallel()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusGatewayTimeout)
		_, _ = fmt.Fprintf(w, `{"resource":"http://%s","scopes_supported":["read"]}`, r.Host)
	}))
	t.Cleanup(server.Close)
	serverURL, err := url.Parse(server.URL)
	require.NoError(t, err)
	responder := newRecordingResponder()
	processor := newDeadlineTestProcessor(t, &stubForwardingTransport{conn: &stubForwardingConnection{}}, responder)
	processor.oauthHTTPClient, processor.mcpServerURL = server.Client(), serverURL
	activity := NewActivityHealth(nil)
	activity.Accepting(true)
	listener := &QueueListener{processor: processor, activityHealth: activity}
	cmd := &fakeOauthDiscoveryCommand{id: "oauth-health", shardToken: "shard-health"}
	require.NoError(t, listener.processObserved(t.Context(), cmd), "preserve the forwarded response and nil return")
	response := responder.waitForResponse(t)
	require.Equal(t, http.StatusGatewayTimeout, response.response.ResponseCode())
	require.Equal(t, types.ResponseTypeOAuthDiscovery, response.response.Type())
	snapshot := activity.Snapshot(time.Now())
	require.Equal(t, healthstate.StatusDegraded, snapshot.Status)
	details := snapshot.Details.(healthstate.DispatcherDetails)
	require.Equal(t, uint64(1), details.Failures)
	require.Equal(t, uint64(1), details.Timeouts)
	require.Zero(t, details.Active)
}

func TestActivityHealthCapturesSwallowedResponseDeadline(t *testing.T) {
	t.Parallel()
	for _, expired := range []bool{true, false} {
		t.Run(map[bool]string{true: "before_dispatch", false: "during_read"}[expired], func(t *testing.T) {
			conn := newDeadlineBlockingConnection()
			processor := newDeadlineTestProcessor(t, &stubForwardingTransport{conn: conn}, &countingResponder{})
			var expire context.CancelCauseFunc
			deadline := time.Now().Add(-time.Second)
			if !expired {
				deadline = time.Now().Add(time.Hour)
				processor.withDeadlineCause = func(ctx context.Context, _ time.Time, _ error) (context.Context, context.CancelFunc) {
					child, cancel := context.WithCancelCause(ctx)
					expire = cancel
					return child, func() { cancel(context.Canceled) }
				}
			}
			id, err := jsonrpc.MakeID("test")
			require.NoError(t, err)
			command := &fakePolledCommand{id: "test", message: &jsonrpc.Request{ID: id, Method: "tools/list"}, shardToken: "test", responseDeadline: deadline, hasResponseDeadline: true}
			activity := NewActivityHealth(nil)
			listener := &QueueListener{processor: processor, activityHealth: activity}
			done := make(chan error, 1)
			go func() { done <- listener.processObserved(t.Context(), command) }()
			if !expired {
				waitForSignal(t, conn.readStarted, "read to begin")
				expire(errResponseDeadlineExceeded)
			}
			require.NoError(t, waitForResult(t, done, "deadline drop to complete"), "preserve intentional nil return")
			d := activity.Snapshot(time.Now()).Details.(healthstate.DispatcherDetails)
			require.Equal(t, uint64(1), d.Timeouts)
			require.Equal(t, uint64(1), d.Failures)
			require.Zero(t, d.Active)
		})
	}
}

func TestActivityHealthBoundsLargePoolAndCopies(t *testing.T) {
	t.Parallel()
	h := NewActivityHealth(&runtimeconfig.MCPConfig{MaxConcurrentRequests: math.MaxInt})
	require.Len(t, h.starts, 1024)
	now := time.Now()
	h.Accepting(true)
	slot := h.begin(now)
	snapshot := h.Snapshot(now.Add(time.Second))
	require.True(t, snapshot.Limited)
	d := snapshot.Details.(healthstate.DispatcherDetails)
	require.Equal(t, math.MaxInt, d.PoolLimit)
	require.Equal(t, uint64(1), d.Active)
	require.Equal(t, 1.0, d.OldestActiveAgeSeconds)
	*d.LastStart = time.Time{}
	require.False(t, h.Snapshot(now).Details.(healthstate.DispatcherDetails).LastStart.IsZero())
	h.finish(slot, now.Add(time.Second), true, true)
	d = h.Snapshot(now).Details.(healthstate.DispatcherDetails)
	require.Zero(t, d.Active)
	require.Equal(t, uint64(1), d.Timeouts)
	require.Equal(t, uint64(1), d.Failures)
}

func TestActivityHealthCountsExecutionAndInlineFallback(t *testing.T) {
	t.Parallel()
	for _, inline := range []bool{false, true} {
		t.Run(map[bool]string{false: "pool", true: "inline"}[inline], func(t *testing.T) {
			cfg := newTestMCPConfigQueue(t, 1)
			activity := NewActivityHealth(cfg)
			queue := make(controlplane.PolledCommandQueue, 1)
			queueHealth := controlplane.NewQueueHealth(queue)
			processor := &stubProcessor{started: make(chan types.RequestID, 1), block: make(chan struct{})}
			logger := slog.New(slog.NewTextHandler(io.Discard, nil))
			var listener *QueueListener
			var err error
			if inline {
				listener, err = newQueueListener(logger, processor, queue, cfg, newManualMeterProvider(t), func(int) (workerPool, error) {
					return &fakeWorkerPool{submitErr: errors.New("submission rejected"), capacity: 1, running: 19}, nil
				})
			} else {
				listener, err = NewQueueListener(logger, processor, queue, cfg, newManualMeterProvider(t))
			}
			require.NoError(t, err)
			listener.ObserveHealth(queueHealth, activity)
			listener.Start(t.Context())
			queue <- newTestCommand(0)
			select {
			case <-processor.started:
			case <-time.After(time.Second):
				t.Fatal("work did not start")
			}
			d := activity.Snapshot(time.Now()).Details.(healthstate.DispatcherDetails)
			require.Equal(t, uint64(1), d.Active)
			require.Equal(t, uint64(1), queueHealth.Snapshot(time.Now()).Details.(healthstate.QueueDetails).Dequeued)
			close(processor.block)
			close(queue)
			listener.Wait()
			d = activity.Snapshot(time.Now()).Details.(healthstate.DispatcherDetails)
			require.Zero(t, d.Active)
			require.Equal(t, uint64(1), d.Completed)
			require.False(t, d.Accepting)
		})
	}
}
