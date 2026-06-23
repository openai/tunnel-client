package harpoon

import (
	"context"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/require"
	"go.opentelemetry.io/otel/attribute"
	sdkmetric "go.opentelemetry.io/otel/sdk/metric"
	"go.opentelemetry.io/otel/sdk/metric/metricdata"

	"go.openai.org/api/tunnel-client/pkg/config"
)

func TestHarpoonMetricsRecordSuccessAndInvalidInput(t *testing.T) {
	reader := sdkmetric.NewManualReader()
	provider := sdkmetric.NewMeterProvider(sdkmetric.WithReader(reader))
	t.Cleanup(func() {
		_ = provider.Shutdown(context.Background())
	})

	targetServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte("ok"))
	}))
	defer targetServer.Close()

	cfg := &config.HarpoonConfig{
		AllowPlaintextHTTP: true,
		MaxResponseBytes:   1024,
		MaxRedirects:       5,
		Targets: []config.HarpoonTarget{{
			Label:   "svc",
			BaseURL: mustParseURL(t, targetServer.URL),
		}},
	}
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	registry, err := NewRegistry(logger, cfg.AllowPlaintextHTTP, convertTargets(cfg.Targets))
	require.NoError(t, err)

	server, err := NewServer(cfg, registry, NewCallBuffer(), logger, WithMeter(provider.Meter("harpoon-test")))
	require.NoError(t, err)

	_, err = server.callTarget(context.Background(), callTargetRequest{
		Label:  "svc",
		Method: http.MethodGet,
	})
	require.NoError(t, err)

	_, err = server.callTarget(context.Background(), callTargetRequest{
		Label:  "svc",
		Method: "DELETE",
	})
	require.Error(t, err)

	var rm metricdata.ResourceMetrics
	require.NoError(t, reader.Collect(context.Background(), &rm))

	callTotal := findInt64Counter(t, rm, metricNameHarpoonCallTotal)
	require.EqualValues(t, 1, counterValueByOutcome(callTotal.DataPoints, metricOutcomeSuccess))
	require.EqualValues(t, 1, counterValueByOutcome(callTotal.DataPoints, metricOutcomeInvalidInput))

	callLatency := findFloat64Histogram(t, rm, metricNameHarpoonCallLatencyMS)
	require.EqualValues(t, 1, float64HistogramCountByOutcome(callLatency.DataPoints, metricOutcomeSuccess))
	require.EqualValues(t, 1, float64HistogramCountByOutcome(callLatency.DataPoints, metricOutcomeInvalidInput))

	responseSize := findInt64Histogram(t, rm, metricNameHarpoonResponseSizeB)
	require.EqualValues(t, 1, int64HistogramCountByOutcome(responseSize.DataPoints, metricOutcomeSuccess))
	require.EqualValues(t, 1, int64HistogramCountByOutcome(responseSize.DataPoints, metricOutcomeInvalidInput))
	require.EqualValues(t, len("ok"), int64HistogramSumByOutcome(responseSize.DataPoints, metricOutcomeSuccess))
	require.EqualValues(t, 0, int64HistogramSumByOutcome(responseSize.DataPoints, metricOutcomeInvalidInput))
}

func TestHarpoonMetricsCollapseUnknownTargetLabels(t *testing.T) {
	reader := sdkmetric.NewManualReader()
	provider := sdkmetric.NewMeterProvider(sdkmetric.WithReader(reader))
	t.Cleanup(func() {
		_ = provider.Shutdown(context.Background())
	})

	cfg := &config.HarpoonConfig{
		AllowPlaintextHTTP: true,
		MaxResponseBytes:   1024,
		MaxRedirects:       5,
		Targets: []config.HarpoonTarget{{
			Label:   "svc",
			BaseURL: mustParseURL(t, "http://example.test"),
		}},
	}
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	registry, err := NewRegistry(logger, cfg.AllowPlaintextHTTP, convertTargets(cfg.Targets))
	require.NoError(t, err)

	server, err := NewServer(cfg, registry, NewCallBuffer(), logger, WithMeter(provider.Meter("harpoon-test")))
	require.NoError(t, err)

	for _, label := range []string{"unknown-a", "unknown-b"} {
		_, err = server.callTarget(context.Background(), callTargetRequest{
			Label:  label,
			Method: http.MethodGet,
		})
		require.Error(t, err)
	}

	var rm metricdata.ResourceMetrics
	require.NoError(t, reader.Collect(context.Background(), &rm))

	callTotal := findInt64Counter(t, rm, metricNameHarpoonCallTotal)
	require.EqualValues(
		t,
		2,
		counterValueByOutcomeAndLabel(
			callTotal.DataPoints,
			metricOutcomeInvalidInput,
			defaultMetricsUnknownTargetLabel,
		),
	)
	require.Zero(t, counterValueByOutcomeAndLabel(callTotal.DataPoints, metricOutcomeInvalidInput, "unknown-a"))
	require.Zero(t, counterValueByOutcomeAndLabel(callTotal.DataPoints, metricOutcomeInvalidInput, "unknown-b"))
}

func findInt64Counter(t *testing.T, rm metricdata.ResourceMetrics, name string) metricdata.Sum[int64] {
	t.Helper()
	for _, scope := range rm.ScopeMetrics {
		for _, m := range scope.Metrics {
			if m.Name != name {
				continue
			}
			sum, ok := m.Data.(metricdata.Sum[int64])
			require.True(t, ok, "metric %q should be int64 sum", name)
			return sum
		}
	}
	t.Fatalf("metric %q not found", name)
	return metricdata.Sum[int64]{}
}

func findFloat64Histogram(t *testing.T, rm metricdata.ResourceMetrics, name string) metricdata.Histogram[float64] {
	t.Helper()
	for _, scope := range rm.ScopeMetrics {
		for _, m := range scope.Metrics {
			if m.Name != name {
				continue
			}
			hist, ok := m.Data.(metricdata.Histogram[float64])
			require.True(t, ok, "metric %q should be float64 histogram", name)
			return hist
		}
	}
	t.Fatalf("metric %q not found", name)
	return metricdata.Histogram[float64]{}
}

func findInt64Histogram(t *testing.T, rm metricdata.ResourceMetrics, name string) metricdata.Histogram[int64] {
	t.Helper()
	for _, scope := range rm.ScopeMetrics {
		for _, m := range scope.Metrics {
			if m.Name != name {
				continue
			}
			hist, ok := m.Data.(metricdata.Histogram[int64])
			require.True(t, ok, "metric %q should be int64 histogram", name)
			return hist
		}
	}
	t.Fatalf("metric %q not found", name)
	return metricdata.Histogram[int64]{}
}

func counterValueByOutcome(dataPoints []metricdata.DataPoint[int64], outcome string) int64 {
	for _, dp := range dataPoints {
		if attr, ok := dp.Attributes.Value(attribute.Key("outcome")); ok && attr.AsString() == outcome {
			return dp.Value
		}
	}
	return 0
}

func counterValueByOutcomeAndLabel(dataPoints []metricdata.DataPoint[int64], outcome string, label string) int64 {
	for _, dp := range dataPoints {
		outcomeAttr, outcomeOK := dp.Attributes.Value(attribute.Key("outcome"))
		labelAttr, labelOK := dp.Attributes.Value(attribute.Key("label"))
		if outcomeOK && labelOK && outcomeAttr.AsString() == outcome && labelAttr.AsString() == label {
			return dp.Value
		}
	}
	return 0
}

func float64HistogramCountByOutcome(dataPoints []metricdata.HistogramDataPoint[float64], outcome string) uint64 {
	for _, dp := range dataPoints {
		if attr, ok := dp.Attributes.Value(attribute.Key("outcome")); ok && attr.AsString() == outcome {
			return dp.Count
		}
	}
	return 0
}

func int64HistogramCountByOutcome(dataPoints []metricdata.HistogramDataPoint[int64], outcome string) uint64 {
	for _, dp := range dataPoints {
		if attr, ok := dp.Attributes.Value(attribute.Key("outcome")); ok && attr.AsString() == outcome {
			return dp.Count
		}
	}
	return 0
}

func int64HistogramSumByOutcome(dataPoints []metricdata.HistogramDataPoint[int64], outcome string) int64 {
	for _, dp := range dataPoints {
		if attr, ok := dp.Attributes.Value(attribute.Key("outcome")); ok && attr.AsString() == outcome {
			return dp.Sum
		}
	}
	return 0
}
