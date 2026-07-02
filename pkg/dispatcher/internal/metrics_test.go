package dispatcherinternal

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"go.opentelemetry.io/otel/attribute"
	sdkmetric "go.opentelemetry.io/otel/sdk/metric"
	"go.opentelemetry.io/otel/sdk/metric/metricdata"

	"go.openai.org/api/tunnel-client/pkg/types"
)

func TestComputeEndToEndLatency(t *testing.T) {
	t.Helper()

	if _, ok := computeEndToEndLatency(time.Time{}); ok {
		t.Fatal("expected zero time to be rejected")
	}

	if _, ok := computeEndToEndLatency(time.Now().Add(5 * time.Second)); ok {
		t.Fatal("expected future enqueue time to be rejected")
	}

	latency, ok := computeEndToEndLatency(time.Now().Add(-50 * time.Millisecond))
	if !ok {
		t.Fatal("expected past enqueue time to be accepted")
	}
	if latency <= 0 {
		t.Fatalf("expected positive latency, got %v", latency)
	}
}

func collectHistogram(t *testing.T, reader *sdkmetric.ManualReader, name string) metricdata.Histogram[float64] {
	t.Helper()

	var rm metricdata.ResourceMetrics
	require.NoError(t, reader.Collect(context.Background(), &rm))

	for _, scope := range rm.ScopeMetrics {
		for _, m := range scope.Metrics {
			if m.Name != name {
				continue
			}
			h, ok := m.Data.(metricdata.Histogram[float64])
			require.True(t, ok, "metric %q was not a float64 histogram", name)
			return h
		}
	}

	t.Fatalf("metric %q not found", name)
	return metricdata.Histogram[float64]{}
}

func dataPointsByLatencyTypeForMetricsTest(t *testing.T, dps []metricdata.HistogramDataPoint[float64]) map[string]metricdata.HistogramDataPoint[float64] {
	t.Helper()
	out := make(map[string]metricdata.HistogramDataPoint[float64])
	for _, dp := range dps {
		latencyType, ok := dp.Attributes.Value(attribute.Key("latency_type"))
		if !ok {
			continue
		}
		out[latencyType.AsString()] = dp
	}
	return out
}

func TestProcessorMetricsRecordCommandLatenciesOnlyRecordsOnce(t *testing.T) {
	t.Helper()

	reader := sdkmetric.NewManualReader()
	provider := sdkmetric.NewMeterProvider(sdkmetric.WithReader(reader))
	t.Cleanup(func() {
		_ = provider.Shutdown(context.Background())
	})

	m, err := newProcessorMetrics(provider.Meter("test"))
	require.NoError(t, err)

	flags := &latencyFlags{}
	now := time.Now()
	enqueuedAt := now.Add(-25 * time.Millisecond)
	polledAt := now.Add(-10 * time.Millisecond)

	m.recordCommandLatencies(context.Background(), types.TunnelID("tunnel-1"), 200, nil, enqueuedAt, polledAt, flags)
	require.True(t, flags.enqueuedRecorded)
	require.True(t, flags.pollRecorded)

	// A second invocation for the same command should be a no-op.
	m.recordCommandLatencies(context.Background(), types.TunnelID("tunnel-1"), 200, nil, enqueuedAt, polledAt, flags)

	h := collectHistogram(t, reader, metricNameCommandEndToEndLatency)
	dpByType := dataPointsByLatencyTypeForMetricsTest(t, h.DataPoints)
	require.Len(t, dpByType, 2, "expected enqueue_to_response and poll_to_response points")

	require.EqualValues(t, 1, dpByType["enqueue_to_response"].Count)
	require.EqualValues(t, 1, dpByType["poll_to_response"].Count)
}

func TestProcessorMetricsRecordCommandLatenciesSkipsEnqueueLatencyWhenMissing(t *testing.T) {
	t.Helper()

	reader := sdkmetric.NewManualReader()
	provider := sdkmetric.NewMeterProvider(sdkmetric.WithReader(reader))
	t.Cleanup(func() {
		_ = provider.Shutdown(context.Background())
	})

	m, err := newProcessorMetrics(provider.Meter("test"))
	require.NoError(t, err)

	flags := &latencyFlags{}

	m.recordCommandLatencies(context.Background(), types.TunnelID("tunnel-1"), 200, nil, time.Time{}, time.Now().Add(-5*time.Millisecond), flags)

	require.False(t, flags.enqueuedRecorded)
	require.True(t, flags.pollRecorded)

	h := collectHistogram(t, reader, metricNameCommandEndToEndLatency)
	dpByType := dataPointsByLatencyTypeForMetricsTest(t, h.DataPoints)
	require.NotContains(t, dpByType, "enqueue_to_response")
	require.Contains(t, dpByType, "poll_to_response")
	require.EqualValues(t, 1, dpByType["poll_to_response"].Count)
}
