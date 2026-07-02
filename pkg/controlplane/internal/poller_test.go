package internal

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/modelcontextprotocol/go-sdk/jsonrpc"
	"go.opentelemetry.io/otel/attribute"
	sdkmetric "go.opentelemetry.io/otel/sdk/metric"
	"go.opentelemetry.io/otel/sdk/metric/metricdata"

	"go.openai.org/api/tunnel-client/pkg/controlplane"
	"go.openai.org/api/tunnel-client/pkg/controlplane/apierror"
	"go.openai.org/api/tunnel-client/pkg/controlplane/wiretypes"
	"go.openai.org/api/tunnel-client/pkg/types"
)

type stubCommand struct {
	id types.RequestID
}

func (s stubCommand) RequestID() types.RequestID { return s.id }
func (s stubCommand) Message() jsonrpc.Message   { return &jsonrpc.Request{Method: "noop"} }
func (s stubCommand) EnqueuedAt() time.Time      { return time.Time{} }
func (s stubCommand) PolledAt() time.Time        { return time.Time{} }
func (s stubCommand) Headers() http.Header       { return nil }
func (s stubCommand) ShardToken() string         { return "" }
func (s stubCommand) Channel() types.Channel     { return types.DefaultChannel }
func (s stubCommand) SessionID() (string, bool) {
	return "", false
}
func (s stubCommand) commandType() wiretypes.CommandType {
	return wiretypes.CommandTypeJSONRPC
}

type agedCommand struct {
	stubCommand
	enqueuedAt time.Time
	polledAt   time.Time
}

func (c agedCommand) EnqueuedAt() time.Time { return c.enqueuedAt }
func (c agedCommand) PolledAt() time.Time   { return c.polledAt }

type recordingFetcher struct {
	t     *testing.T
	data  []controlplane.PolledCommand
	mu    sync.Mutex
	calls []int
}

func (f *recordingFetcher) Poll(ctx context.Context, limit int) ([]controlplane.PolledCommand, types.TunnelServiceRequestID, error) {
	if limit <= 0 {
		f.t.Fatalf("expected positive limit, got %d", limit)
	}

	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls = append(f.calls, limit)

	if len(f.data) == 0 {
		return nil, "", nil
	}
	n := limit
	if n > len(f.data) {
		n = len(f.data)
	}

	out := make([]controlplane.PolledCommand, n)
	copy(out, f.data[:n])
	f.data = f.data[n:]
	return out, "", nil
}

func TestPollerWritesAtMostQueueCapacity(t *testing.T) {
	queue := make(chan controlplane.PolledCommand, 2)
	queueAdapter := &chanQueue{ch: queue}
	fetcher := &recordingFetcher{
		t: t,
		data: []controlplane.PolledCommand{
			stubCommand{id: "1"},
			stubCommand{id: "2"},
			stubCommand{id: "3"},
		},
	}
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	reader := sdkmetric.NewManualReader()
	meterProvider := sdkmetric.NewMeterProvider(sdkmetric.WithReader(reader))
	defer func() {
		_ = meterProvider.Shutdown(context.Background())
	}()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	poller, err := NewPoller(queueAdapter, fetcher, logger, meterProvider.Meter("test"), time.Second, 0, 0, 0)
	if err != nil {
		t.Fatalf("new poller: %v", err)
	}

	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		poller.Run(ctx)
	}()

	received := make([]controlplane.PolledCommand, 0, 3)

	waitForQueue := func() controlplane.PolledCommand {
		select {
		case <-time.After(time.Second):
			t.Fatal("timed out waiting for command")
			return nil
		case cmd := <-queue:
			return cmd
		}
	}

	for i := 0; i < 3; i++ {
		received = append(received, waitForQueue())
	}

	cancel()
	wg.Wait()

	if len(received) != 3 {
		t.Fatalf("expected 3 commands, got %d", len(received))
	}

	fetcher.mu.Lock()
	defer fetcher.mu.Unlock()

	for _, limit := range fetcher.calls {
		if limit > cap(queue) {
			t.Fatalf("poll called with limit %d exceeding queue capacity %d", limit, cap(queue))
		}
	}

	var rm metricdata.ResourceMetrics
	if err := reader.Collect(context.Background(), &rm); err != nil {
		t.Fatalf("collect metrics: %v", err)
	}

	assertCounterValue(t, rm, metricNameCommandsPolled, int64(len(received)))
	assertCounterValue(t, rm, metricNameCommandsEnqueued, int64(len(received)))
	assertGaugeValue(t, rm, metricNameQueueCapacity, int64(cap(queue)))
	assertGaugeValue(t, rm, metricNameQueueLength, int64(len(queue)))
}

func TestPollLimitCapsControlPlaneBatchSize(t *testing.T) {
	t.Parallel()

	if got := pollLimit(100); got != maxPollBatchSize {
		t.Fatalf("pollLimit(100) = %d, want %d", got, maxPollBatchSize)
	}

	if got := pollLimit(20); got != 20 {
		t.Fatalf("pollLimit(20) = %d, want 20", got)
	}
}

func TestPollerRecordsQueueDropsAndCommandAge(t *testing.T) {
	queue := &failingQueue{}
	fetcher := &recordingFetcher{
		t: t,
		data: []controlplane.PolledCommand{
			agedCommand{
				stubCommand: stubCommand{id: "1"},
				enqueuedAt:  time.Now().Add(-3 * time.Second),
				polledAt:    time.Now(),
			},
		},
	}
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	reader := sdkmetric.NewManualReader()
	meterProvider := sdkmetric.NewMeterProvider(sdkmetric.WithReader(reader))
	defer func() {
		_ = meterProvider.Shutdown(context.Background())
	}()

	poller, err := NewPoller(queue, fetcher, logger, meterProvider.Meter("test"), time.Second, 0, 0, 0)
	if err != nil {
		t.Fatalf("new poller: %v", err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 150*time.Millisecond)
	defer cancel()

	poller.Run(ctx)

	var rm metricdata.ResourceMetrics
	if err := reader.Collect(context.Background(), &rm); err != nil {
		t.Fatalf("collect metrics: %v", err)
	}

	assertCounterValue(t, rm, metricNameCommandsPolled, 1)
	assertCounterValueWithAttributes(t, rm, metricNameCommandsQueueDrops, attribute.String(attributeKeyDropReason, dropReasonContextClosed), 1)
	assertHistogramCount(t, rm, metricNameCommandsAge, 1)
	assertHistogramSumWithAttributes(t, rm, metricNameCommandsAge, attribute.KeyValue{}, 3)
}

type untypedCommand struct {
	id types.RequestID
}

func (u untypedCommand) RequestID() types.RequestID { return u.id }
func (u untypedCommand) Message() jsonrpc.Message   { return &jsonrpc.Request{Method: "noop"} }
func (u untypedCommand) EnqueuedAt() time.Time      { return time.Time{} }
func (u untypedCommand) PolledAt() time.Time        { return time.Time{} }
func (u untypedCommand) Headers() http.Header       { return nil }
func (u untypedCommand) ShardToken() string         { return "" }
func (u untypedCommand) Channel() types.Channel     { return types.DefaultChannel }
func (u untypedCommand) SessionID() (string, bool)  { return "", false }

func TestPollerRecordsInvalidCommandTypeDrops(t *testing.T) {
	queueCh := make(chan controlplane.PolledCommand, 2)
	queue := &chanQueue{ch: queueCh}
	fetcher := &recordingFetcher{
		t: t,
		data: []controlplane.PolledCommand{
			untypedCommand{id: "bad"},
			stubCommand{id: "ok"},
		},
	}
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	reader := sdkmetric.NewManualReader()
	meterProvider := sdkmetric.NewMeterProvider(sdkmetric.WithReader(reader))
	defer func() {
		_ = meterProvider.Shutdown(context.Background())
	}()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	poller, err := NewPoller(queue, fetcher, logger, meterProvider.Meter("test"), time.Second, 0, 0, 0)
	if err != nil {
		t.Fatalf("new poller: %v", err)
	}

	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		poller.Run(ctx)
	}()

	select {
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for enqueued command")
	case got := <-queueCh:
		if got.RequestID() != "ok" {
			t.Fatalf("unexpected enqueued command id: got %q, want %q", got.RequestID(), "ok")
		}
	}

	cancel()
	wg.Wait()

	var rm metricdata.ResourceMetrics
	if err := reader.Collect(context.Background(), &rm); err != nil {
		t.Fatalf("collect metrics: %v", err)
	}

	assertCounterValue(t, rm, metricNameCommandsPolled, 2)
	assertCounterValue(t, rm, metricNameCommandsEnqueued, 1)
	assertCounterValueWithAttributes(t, rm, metricNameCommandsQueueDrops, attribute.String(attributeKeyDropReason, dropReasonInvalidCommandType), 1)
}

func TestBuildBaseDefaultsChannel(t *testing.T) {
	raw := wiretypes.BaseRawPolledCommand{
		RequestID:   "req-1",
		ShardToken:  "shard-1",
		CommandType: wiretypes.CommandTypeJSONRPC,
		CreatedAt:   time.Now(),
	}

	base, _, err := buildBase(raw, time.Now())
	if err != nil {
		t.Fatalf("buildBase: %v", err)
	}
	if base.Channel() != types.DefaultChannel {
		t.Fatalf("expected default channel %q, got %q", types.DefaultChannel, base.Channel())
	}
}

func TestBuildBasePreservesChannel(t *testing.T) {
	raw := wiretypes.BaseRawPolledCommand{
		RequestID:   "req-2",
		ShardToken:  "shard-2",
		CommandType: wiretypes.CommandTypeJSONRPC,
		Channel:     "harpoon",
		CreatedAt:   time.Now(),
	}

	base, _, err := buildBase(raw, time.Now())
	if err != nil {
		t.Fatalf("buildBase: %v", err)
	}
	if base.Channel() != types.ChannelHarpoon {
		t.Fatalf("expected channel %q, got %q", types.ChannelHarpoon, base.Channel())
	}
}

func TestPollerRecordsContextCanceledQueueDrops(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	queue := &cancelingQueue{cancel: cancel}
	fetcher := &recordingFetcher{
		t: t,
		data: []controlplane.PolledCommand{
			stubCommand{id: "1"},
		},
	}
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	reader := sdkmetric.NewManualReader()
	meterProvider := sdkmetric.NewMeterProvider(sdkmetric.WithReader(reader))
	defer func() {
		_ = meterProvider.Shutdown(context.Background())
	}()

	poller, err := NewPoller(queue, fetcher, logger, meterProvider.Meter("test"), time.Second, 0, 0, 0)
	if err != nil {
		t.Fatalf("new poller: %v", err)
	}

	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		poller.Run(ctx)
	}()

	wg.Wait()

	var rm metricdata.ResourceMetrics
	if err := reader.Collect(context.Background(), &rm); err != nil {
		t.Fatalf("collect metrics: %v", err)
	}

	assertCounterValueWithAttributes(t, rm, metricNameCommandsQueueDrops, attribute.String(attributeKeyDropReason, dropReasonContextClosed), 1)
}

func TestPollerTagsPollErrors(t *testing.T) {
	queue := &chanQueue{ch: make(chan controlplane.PolledCommand, 1)}
	fetcher := &erroringFetcher{err: context.DeadlineExceeded, pollCh: make(chan struct{}, 1)}
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	reader := sdkmetric.NewManualReader()
	meterProvider := sdkmetric.NewMeterProvider(sdkmetric.WithReader(reader))
	defer func() {
		_ = meterProvider.Shutdown(context.Background())
	}()

	poller, err := NewPoller(queue, fetcher, logger, meterProvider.Meter("test"), 25*time.Millisecond, 0, 0, 0)
	if err != nil {
		t.Fatalf("new poller: %v", err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		poller.Run(ctx)
	}()

	fetcher.waitForPoll(t)
	cancel()
	wg.Wait()

	var rm metricdata.ResourceMetrics
	if err := reader.Collect(context.Background(), &rm); err != nil {
		t.Fatalf("collect metrics: %v", err)
	}

	assertCounterValueWithAttributes(t, rm, metricNameCommandsPollErrors, attribute.String(attributeKeyErrorKind, errorKindTimeout), 1)
	assertHistogramCountWithAttributes(t, rm, metricNameCommandsPollLatency, attribute.Bool("error", true), 1)
}

func TestPollerDoesNotDropCommandsWhenFetcherExceedsLimit(t *testing.T) {
	t.Parallel()

	// Real-life scenario: tunnel-service may (incorrectly) return more commands than the
	// requested limit. The poller must not drop those commands since the service may not
	// re-deliver them. Instead, the poller should apply backpressure and enqueue them as
	// the dispatcher drains the queue.

	queueCh := make(chan controlplane.PolledCommand, 1)
	queue := &chanQueue{ch: queueCh}
	fetcher := &overLimitFetcher{
		data: []controlplane.PolledCommand{
			stubCommand{id: "1"},
			stubCommand{id: "2"},
			stubCommand{id: "3"},
		},
	}

	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	reader := sdkmetric.NewManualReader()
	meterProvider := sdkmetric.NewMeterProvider(sdkmetric.WithReader(reader))
	t.Cleanup(func() {
		_ = meterProvider.Shutdown(context.Background())
	})

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	poller, err := NewPoller(queue, fetcher, logger, meterProvider.Meter("test"), time.Second, 0, 0, 0)
	if err != nil {
		t.Fatalf("new poller: %v", err)
	}

	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		poller.Run(ctx)
	}()

	// Drain slowly to force backpressure on the second/third enqueue while ensuring
	// all commands are eventually observed.
	got := make([]string, 0, 3)
	for i := 0; i < 3; i++ {
		select {
		case <-time.After(2 * time.Second):
			t.Fatalf("timed out waiting for command %d", i+1)
		case cmd := <-queueCh:
			got = append(got, cmd.RequestID().String())
			time.Sleep(25 * time.Millisecond)
		}
	}

	cancel()
	wg.Wait()

	if fetcher.maxLimitSeen > cap(queueCh) {
		t.Fatalf("fetcher should not be called with limit %d exceeding queue capacity %d", fetcher.maxLimitSeen, cap(queueCh))
	}

	want := []string{"1", "2", "3"}
	if len(got) != len(want) {
		t.Fatalf("got %d commands, want %d: %v", len(got), len(want), got)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("command order mismatch: got %v want %v", got, want)
		}
	}

	var rm metricdata.ResourceMetrics
	if err := reader.Collect(context.Background(), &rm); err != nil {
		t.Fatalf("collect metrics: %v", err)
	}
	assertCounterValue(t, rm, metricNameCommandsPolled, 3)
	assertCounterValue(t, rm, metricNameCommandsEnqueued, 3)
}

type chanQueue struct {
	ch chan controlplane.PolledCommand
}

func (q *chanQueue) Capacity() int {
	return cap(q.ch)
}

func (q *chanQueue) Length() int {
	return len(q.ch)
}

func (q *chanQueue) Enqueue(ctx context.Context, cmd controlplane.PolledCommand) bool {
	select {
	case <-ctx.Done():
		return false
	default:
	}

	select {
	case <-ctx.Done():
		return false
	case q.ch <- cmd:
		return true
	}
}

type failingQueue struct{}

func (f *failingQueue) Capacity() int                                            { return 1 }
func (f *failingQueue) Length() int                                              { return 0 }
func (f *failingQueue) Enqueue(context.Context, controlplane.PolledCommand) bool { return false }

type cancelingQueue struct {
	cancel context.CancelFunc
}

func (c *cancelingQueue) Capacity() int { return 1 }
func (c *cancelingQueue) Length() int   { return 0 }
func (c *cancelingQueue) Enqueue(ctx context.Context, _ controlplane.PolledCommand) bool {
	if c.cancel != nil {
		c.cancel()
	}
	select {
	case <-ctx.Done():
		return false
	default:
		return false
	}
}

func assertCounterValue(t *testing.T, rm metricdata.ResourceMetrics, name string, want int64) {
	t.Helper()
	got, ok := findCounterValue(rm, name)
	if !ok {
		t.Fatalf("metric %q not found", name)
	}
	if got != want {
		t.Fatalf("metric %q = %d, want %d", name, got, want)
	}
}

func findCounterValue(rm metricdata.ResourceMetrics, name string) (int64, bool) {
	for _, scope := range rm.ScopeMetrics {
		for _, m := range scope.Metrics {
			if m.Name != name {
				continue
			}
			sum, ok := m.Data.(metricdata.Sum[int64])
			if !ok {
				return 0, false
			}
			var total int64
			for _, dp := range sum.DataPoints {
				total += dp.Value
			}
			return total, true
		}
	}
	return 0, false
}

func assertGaugeValue(t *testing.T, rm metricdata.ResourceMetrics, name string, want int64) {
	t.Helper()
	got, ok := findGaugeValue(rm, name)
	if !ok {
		t.Fatalf("metric %q not found", name)
	}
	if got != want {
		t.Fatalf("metric %q = %d, want %d", name, got, want)
	}
}

func findGaugeValue(rm metricdata.ResourceMetrics, name string) (int64, bool) {
	for _, scope := range rm.ScopeMetrics {
		for _, m := range scope.Metrics {
			if m.Name != name {
				continue
			}
			g, ok := m.Data.(metricdata.Gauge[int64])
			if !ok {
				return 0, false
			}
			var total int64
			for _, dp := range g.DataPoints {
				total += dp.Value
			}
			return total, true
		}
	}
	return 0, false
}

type overLimitFetcher struct {
	data []controlplane.PolledCommand

	mu           sync.Mutex
	called       bool
	maxLimitSeen int
}

func (f *overLimitFetcher) Poll(ctx context.Context, limit int) ([]controlplane.PolledCommand, types.TunnelServiceRequestID, error) {
	f.mu.Lock()
	if limit > f.maxLimitSeen {
		f.maxLimitSeen = limit
	}
	first := !f.called
	if first {
		f.called = true
	}
	f.mu.Unlock()

	if !first {
		return nil, "", nil
	}
	// Intentionally ignore limit to simulate a misbehaving control plane.
	out := make([]controlplane.PolledCommand, len(f.data))
	copy(out, f.data)
	return out, "", nil
}

func assertCounterValueWithAttributes(t *testing.T, rm metricdata.ResourceMetrics, name string, attr attribute.KeyValue, want int64) {
	t.Helper()
	got, ok := findCounterValueWithAttributes(rm, name, attr)
	if !ok {
		t.Fatalf("metric %q with attribute %q=%q not found", name, attr.Key, attr.Value.AsString())
	}
	if got != want {
		t.Fatalf("metric %q (%q=%q) = %d, want %d", name, attr.Key, attr.Value.AsString(), got, want)
	}
}

func findCounterValueWithAttributes(rm metricdata.ResourceMetrics, name string, attr attribute.KeyValue) (int64, bool) {
	for _, scope := range rm.ScopeMetrics {
		for _, m := range scope.Metrics {
			if m.Name != name {
				continue
			}
			sum, ok := m.Data.(metricdata.Sum[int64])
			if !ok {
				return 0, false
			}
			for _, dp := range sum.DataPoints {
				if attributesContain(dp.Attributes, attr) {
					return dp.Value, true
				}
			}
		}
	}
	return 0, false
}

func attributesContain(set attribute.Set, attr attribute.KeyValue) bool {
	iter := set.Iter()
	for iter.Next() {
		kv := iter.Attribute()
		if kv.Key == attr.Key && kv.Value == attr.Value {
			return true
		}
	}
	return false
}

func assertHistogramCount(t *testing.T, rm metricdata.ResourceMetrics, name string, want int64) {
	t.Helper()
	got, ok := findHistogramCount(rm, name)
	if !ok {
		t.Fatalf("metric %q not found", name)
	}
	if got != want {
		t.Fatalf("metric %q count = %d, want %d", name, got, want)
	}
}

func findHistogramCount(rm metricdata.ResourceMetrics, name string) (int64, bool) {
	for _, scope := range rm.ScopeMetrics {
		for _, m := range scope.Metrics {
			if m.Name != name {
				continue
			}
			h, ok := m.Data.(metricdata.Histogram[float64])
			if !ok {
				return 0, false
			}
			var count int64
			for _, dp := range h.DataPoints {
				count += int64(dp.Count)
			}
			return count, true
		}
	}
	return 0, false
}

func assertHistogramCountWithAttributes(t *testing.T, rm metricdata.ResourceMetrics, name string, attr attribute.KeyValue, want int64) {
	t.Helper()

	got, ok := findHistogramCountWithAttributes(rm, name, attr)
	if !ok {
		t.Fatalf("metric %q with attribute %q=%q not found", name, attr.Key, attr.Value.AsString())
	}

	if got != want {
		t.Fatalf("metric %q (%q=%q) count = %d, want %d", name, attr.Key, attr.Value.AsString(), got, want)
	}
}

func findHistogramCountWithAttributes(rm metricdata.ResourceMetrics, name string, attr attribute.KeyValue) (int64, bool) {
	for _, scope := range rm.ScopeMetrics {
		for _, m := range scope.Metrics {
			if m.Name != name {
				continue
			}
			histogram, ok := m.Data.(metricdata.Histogram[float64])
			if !ok {
				return 0, false
			}
			if attr.Key == "" {
				var count int64
				for _, dp := range histogram.DataPoints {
					count += int64(dp.Count)
				}
				return count, count > 0
			}
			var count int64
			for _, dp := range histogram.DataPoints {
				if attributesContain(dp.Attributes, attr) {
					count += int64(dp.Count)
				}
			}
			return count, count > 0
		}
	}
	return 0, false
}

func assertHistogramSumWithAttributes(t *testing.T, rm metricdata.ResourceMetrics, name string, attr attribute.KeyValue, wantSeconds float64) {
	t.Helper()

	got, ok := findHistogramSumWithAttributes(rm, name, attr)
	if !ok {
		if attr.Key == "" {
			t.Fatalf("metric %q not found", name)
		}
		t.Fatalf("metric %q with attribute %q=%q not found", name, attr.Key, attr.Value.AsString())
	}

	if got <= 0 {
		if attr.Key == "" {
			t.Fatalf("metric %q sum was non-positive", name)
		}
		t.Fatalf("metric %q (%q=%q) sum was non-positive", name, attr.Key, attr.Value.AsString())
	}

	if diff := got - wantSeconds; diff > 1 || diff < -1 {
		if attr.Key == "" {
			t.Fatalf("metric %q sum = %f, want around %f", name, got, wantSeconds)
		}
		t.Fatalf("metric %q (%q=%q) sum = %f, want around %f", name, attr.Key, attr.Value.AsString(), got, wantSeconds)
	}
}

func findHistogramSumWithAttributes(rm metricdata.ResourceMetrics, name string, attr attribute.KeyValue) (float64, bool) {
	for _, scope := range rm.ScopeMetrics {
		for _, m := range scope.Metrics {
			if m.Name != name {
				continue
			}
			histogram, ok := m.Data.(metricdata.Histogram[float64])
			if !ok {
				return 0, false
			}
			if attr.Key == "" {
				if len(histogram.DataPoints) == 0 {
					return 0, false
				}
				return histogram.DataPoints[0].Sum, true
			}
			for _, dp := range histogram.DataPoints {
				if attributesContain(dp.Attributes, attr) {
					return dp.Sum, true
				}
			}
		}
	}
	return 0, false
}

type timeoutRecordingFetcher struct {
	mu        sync.Mutex
	callCount int
	errs      []error
	deadlines []time.Time
	pollCh    chan struct{}
}

type cancelAwareFetcher struct {
	pollStarted chan struct{}
}

type erroringFetcher struct {
	err    error
	pollCh chan struct{}
}

func (f *erroringFetcher) Poll(ctx context.Context, limit int) ([]controlplane.PolledCommand, types.TunnelServiceRequestID, error) {
	if f.pollCh != nil {
		select {
		case f.pollCh <- struct{}{}:
		default:
		}
	}
	return nil, "", f.err
}

func (f *erroringFetcher) waitForPoll(t *testing.T) {
	t.Helper()
	if f.pollCh == nil {
		t.Fatal("erroringFetcher poll channel was nil")
	}
	select {
	case <-f.pollCh:
	case <-time.After(time.Second):
		t.Fatal("poll was not invoked")
	}
}

func (f *timeoutRecordingFetcher) Poll(ctx context.Context, limit int) ([]controlplane.PolledCommand, types.TunnelServiceRequestID, error) {
	deadline, hasDeadline := ctx.Deadline()
	<-ctx.Done()
	err := ctx.Err()

	f.mu.Lock()
	f.callCount++
	if hasDeadline {
		f.deadlines = append(f.deadlines, deadline)
	}
	f.errs = append(f.errs, err)
	f.mu.Unlock()

	if f.pollCh != nil {
		select {
		case f.pollCh <- struct{}{}:
		default:
		}
	}
	return nil, "", err
}

func (f *timeoutRecordingFetcher) waitForCalls(t *testing.T, want int) {
	t.Helper()
	if f.pollCh == nil {
		t.Fatal("timeoutRecordingFetcher poll channel was nil")
	}
	for i := 0; i < want; i++ {
		select {
		case <-f.pollCh:
		case <-time.After(2 * time.Second):
			t.Fatalf("timed out waiting for poll call %d of %d", i+1, want)
		}
	}
}

func (f *cancelAwareFetcher) Poll(ctx context.Context, limit int) ([]controlplane.PolledCommand, types.TunnelServiceRequestID, error) {
	if f.pollStarted != nil {
		select {
		case f.pollStarted <- struct{}{}:
		default:
		}
	}
	<-ctx.Done()
	return nil, "", ctx.Err()
}

type sequenceFetcher struct {
	mu     sync.Mutex
	errs   []error
	cancel context.CancelFunc
}

func (f *sequenceFetcher) Poll(ctx context.Context, limit int) ([]controlplane.PolledCommand, types.TunnelServiceRequestID, error) {
	f.mu.Lock()
	defer f.mu.Unlock()

	if len(f.errs) == 0 {
		return nil, "", nil
	}

	err := f.errs[0]
	f.errs = f.errs[1:]
	if err == nil && f.cancel != nil {
		f.cancel()
	}
	return nil, "", err
}

func TestPollerLogsRecoveryAfterError(t *testing.T) {
	queue := &chanQueue{ch: make(chan controlplane.PolledCommand, 1)}
	var output strings.Builder
	logger := slog.New(slog.NewTextHandler(&output, nil))
	reader := sdkmetric.NewManualReader()
	meterProvider := sdkmetric.NewMeterProvider(sdkmetric.WithReader(reader))
	defer func() {
		_ = meterProvider.Shutdown(context.Background())
	}()

	ctx, cancel := context.WithCancel(context.Background())
	fetcher := &sequenceFetcher{
		errs:   []error{errors.New("poll failed"), nil},
		cancel: cancel,
	}
	poller, err := NewPoller(queue, fetcher, logger, meterProvider.Meter("test"), 25*time.Millisecond, 0, time.Millisecond, 2*time.Millisecond)
	if err != nil {
		t.Fatalf("new poller: %v", err)
	}

	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		poller.Run(ctx)
	}()
	wg.Wait()

	if !strings.Contains(output.String(), "poller recovered; polling operational") {
		t.Fatalf("expected recovery log message, got %q", output.String())
	}
}

func TestPollerLogsAPIStatusErrorDetails(t *testing.T) {
	queue := &chanQueue{ch: make(chan controlplane.PolledCommand, 1)}
	var output strings.Builder
	logger := slog.New(slog.NewTextHandler(&output, nil))
	reader := sdkmetric.NewManualReader()
	meterProvider := sdkmetric.NewMeterProvider(sdkmetric.WithReader(reader))
	defer func() {
		_ = meterProvider.Shutdown(context.Background())
	}()

	ctx, cancel := context.WithCancel(context.Background())
	fetcher := &sequenceFetcher{
		errs: []error{
			&APIStatusError{
				prefix:     "controlplane client: unexpected status",
				statusCode: http.StatusUnauthorized,
				status:     "401 Unauthorized",
				info: apierror.Info{
					Code:    "certificate_required",
					Type:    "invalid_request_error",
					Message: "missing client certificate",
				},
			},
			nil,
		},
		cancel: cancel,
	}
	poller, err := NewPoller(queue, fetcher, logger, meterProvider.Meter("test"), 25*time.Millisecond, 0, time.Millisecond, 2*time.Millisecond)
	if err != nil {
		t.Fatalf("new poller: %v", err)
	}

	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		poller.Run(ctx)
	}()
	wg.Wait()

	logs := output.String()
	for _, want := range []string{
		"status_code=401",
		"status=\"401 Unauthorized\"",
		"error_code=certificate_required",
		"error_message=\"missing client certificate\"",
	} {
		if !strings.Contains(logs, want) {
			t.Fatalf("expected poller log to contain %q, got %q", want, logs)
		}
	}
}

func TestPollerPollsWithGuardrailedTimeoutAndRetries(t *testing.T) {
	queue := &chanQueue{ch: make(chan controlplane.PolledCommand, 1)}
	fetcher := &timeoutRecordingFetcher{pollCh: make(chan struct{}, 8)}
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	reader := sdkmetric.NewManualReader()
	meterProvider := sdkmetric.NewMeterProvider(sdkmetric.WithReader(reader))
	defer func() {
		_ = meterProvider.Shutdown(context.Background())
	}()

	pollTimeout := 50 * time.Millisecond
	pollGuardrail := 10 * time.Millisecond
	runner, err := NewPoller(queue, fetcher, logger, meterProvider.Meter("test"), pollTimeout, pollGuardrail, 0, 0)
	if err != nil {
		t.Fatalf("new poller: %v", err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		runner.Run(ctx)
	}()

	fetcher.waitForCalls(t, 2)
	cancel()
	wg.Wait()

	fetcher.mu.Lock()
	defer fetcher.mu.Unlock()

	if fetcher.callCount < 2 {
		t.Fatalf("expected poller to retry after timeout, got %d calls", fetcher.callCount)
	}

	if len(fetcher.deadlines) != fetcher.callCount {
		t.Fatalf("expected every poll call to receive a deadline, got %d deadlines for %d calls", len(fetcher.deadlines), fetcher.callCount)
	}
	for i, err := range fetcher.errs {
		if !errors.Is(err, context.DeadlineExceeded) {
			t.Fatalf("call %d ended with %v, want %v", i, err, context.DeadlineExceeded)
		}
	}
}

func TestPollerRetriesOnCanceledErrorWithoutStop(t *testing.T) {
	queue := &chanQueue{ch: make(chan controlplane.PolledCommand, 1)}
	fetcher := &erroringFetcher{err: context.Canceled, pollCh: make(chan struct{}, 4)}
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	reader := sdkmetric.NewManualReader()
	meterProvider := sdkmetric.NewMeterProvider(sdkmetric.WithReader(reader))
	defer func() {
		_ = meterProvider.Shutdown(context.Background())
	}()

	poller, err := NewPoller(queue, fetcher, logger, meterProvider.Meter("test"), 25*time.Millisecond, 0, time.Millisecond, 2*time.Millisecond)
	if err != nil {
		t.Fatalf("new poller: %v", err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()

	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		poller.Run(ctx)
	}()

	fetcher.waitForPoll(t)
	select {
	case <-fetcher.pollCh:
	case <-time.After(50 * time.Millisecond):
		t.Fatal("poller did not retry after context.Canceled error")
	}

	cancel()
	wg.Wait()

	var rm metricdata.ResourceMetrics
	if err := reader.Collect(context.Background(), &rm); err != nil {
		t.Fatalf("collect metrics: %v", err)
	}
	if got, ok := findCounterValueWithAttributes(rm, metricNameCommandsPollErrors, attribute.String(attributeKeyErrorKind, errorKindContextCanceled)); !ok || got == 0 {
		t.Fatalf("expected context_canceled poll error metric, got %d (ok=%v)", got, ok)
	}
}

func TestPollerStopsWithoutBackoffOnCancel(t *testing.T) {
	queue := &chanQueue{ch: make(chan controlplane.PolledCommand, 1)}
	fetcher := &cancelAwareFetcher{pollStarted: make(chan struct{}, 1)}
	var output strings.Builder
	logger := slog.New(slog.NewTextHandler(&output, nil))
	reader := sdkmetric.NewManualReader()
	meterProvider := sdkmetric.NewMeterProvider(sdkmetric.WithReader(reader))
	defer func() {
		_ = meterProvider.Shutdown(context.Background())
	}()

	poller, err := NewPoller(queue, fetcher, logger, meterProvider.Meter("test"), time.Second, 0, 5*time.Second, 10*time.Second)
	if err != nil {
		t.Fatalf("new poller: %v", err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		poller.Run(ctx)
	}()

	select {
	case <-fetcher.pollStarted:
	case <-time.After(time.Second):
		t.Fatal("poll was not invoked")
	}
	cancel()
	wg.Wait()

	if strings.Contains(output.String(), "poll failed; backing off") {
		t.Fatalf("expected no backoff log on cancel, got %q", output.String())
	}
	if strings.Contains(output.String(), "poll timed out; backing off") {
		t.Fatalf("expected no timeout backoff log on cancel, got %q", output.String())
	}
}
