package harpoon

import (
	"context"
	"fmt"
	"net/http"
	"strings"
	"time"

	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/metric"
	noopmetric "go.opentelemetry.io/otel/metric/noop"
)

const (
	metricNameHarpoonCallTotal       = "harpoon_call_total"
	metricNameHarpoonCallLatencyMS   = "harpoon_call_latency_milliseconds"
	metricNameHarpoonResponseSizeB   = "harpoon_response_size_bytes"
	metricStatusClassNone            = "none"
	metricOutcomeSuccess             = "success"
	metricOutcomeInvalidInput        = "invalid_input"
	metricOutcomeRequestError        = "request_error"
	metricOutcomeResponseReadError   = "response_read_error"
	metricOutcomeResponseTooLarge    = "response_too_large"
	defaultMetricsMeterName          = "harpoon"
	defaultMetricsUnknownTargetLabel = "unknown"
)

type serverMetrics struct {
	callTotal    metric.Int64Counter
	callLatency  metric.Float64Histogram
	responseSize metric.Int64Histogram
}

type serverOptions struct {
	meter         metric.Meter
	httpTransport http.RoundTripper
}

// ServerOption configures optional server behavior.
type ServerOption func(*serverOptions)

// WithMeter configures the meter used for Harpoon metrics.
func WithMeter(meter metric.Meter) ServerOption {
	return func(o *serverOptions) {
		if o == nil {
			return
		}
		o.meter = meter
	}
}

// WithHTTPTransport sets the HTTP transport used for Harpoon outbound calls.
func WithHTTPTransport(rt http.RoundTripper) ServerOption {
	return func(o *serverOptions) {
		if o == nil {
			return
		}
		o.httpTransport = rt
	}
}

func resolveServerOptions(opts ...ServerOption) serverOptions {
	out := serverOptions{
		meter: noopmetric.NewMeterProvider().Meter(defaultMetricsMeterName),
	}
	for _, opt := range opts {
		if opt == nil {
			continue
		}
		opt(&out)
	}
	return out
}

func newServerMetrics(meter metric.Meter) (*serverMetrics, error) {
	if meter == nil {
		return nil, fmt.Errorf("harpoon metrics: meter is required")
	}

	callTotal, err := meter.Int64Counter(
		metricNameHarpoonCallTotal,
		metric.WithDescription("Count of Harpoon call_target invocations."),
		metric.WithUnit("{count}"),
	)
	if err != nil {
		return nil, err
	}

	callLatency, err := meter.Float64Histogram(
		metricNameHarpoonCallLatencyMS,
		metric.WithDescription("Latency of Harpoon call_target invocations in milliseconds."),
		metric.WithUnit("ms"),
	)
	if err != nil {
		return nil, err
	}

	responseSize, err := meter.Int64Histogram(
		metricNameHarpoonResponseSizeB,
		metric.WithDescription("Response payload size returned by Harpoon call_target in bytes."),
		metric.WithUnit("By"),
	)
	if err != nil {
		return nil, err
	}

	return &serverMetrics{
		callTotal:    callTotal,
		callLatency:  callLatency,
		responseSize: responseSize,
	}, nil
}

func (s *Server) recordCallMetrics(
	ctx context.Context,
	label string,
	statusCode int,
	outcome string,
	responseBytes int,
	startedAt time.Time,
) {
	if s == nil || s.metrics == nil {
		return
	}
	if startedAt.IsZero() {
		startedAt = time.Now()
	}
	latency := time.Since(startedAt)
	if latency < 0 {
		latency = 0
	}
	s.metrics.recordCall(ctx, label, statusCode, outcome, responseBytes, latency)
}

func (m *serverMetrics) recordCall(
	ctx context.Context,
	label string,
	statusCode int,
	outcome string,
	responseBytes int,
	latency time.Duration,
) {
	if m == nil {
		return
	}

	attrs := []attribute.KeyValue{
		attribute.String("label", metricLabel(label)),
		attribute.String("status_class", metricStatusClass(statusCode)),
		attribute.String("outcome", metricOutcome(outcome)),
	}

	m.callTotal.Add(ctx, 1, metric.WithAttributes(attrs...))
	m.callLatency.Record(ctx, float64(latency)/float64(time.Millisecond), metric.WithAttributes(attrs...))
	if responseBytes < 0 {
		responseBytes = 0
	}
	m.responseSize.Record(ctx, int64(responseBytes), metric.WithAttributes(attrs...))
}

func metricLabel(label string) string {
	trimmed := strings.TrimSpace(label)
	if trimmed == "" {
		return defaultMetricsUnknownTargetLabel
	}
	return trimmed
}

func metricStatusClass(statusCode int) string {
	if statusCode < 100 || statusCode > 599 {
		return metricStatusClassNone
	}
	return fmt.Sprintf("%dxx", statusCode/100)
}

func metricOutcome(outcome string) string {
	trimmed := strings.TrimSpace(outcome)
	if trimmed == "" {
		return metricOutcomeRequestError
	}
	return trimmed
}
