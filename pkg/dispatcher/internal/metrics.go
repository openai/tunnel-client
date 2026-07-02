package dispatcherinternal

import (
	"context"
	"fmt"
	"time"

	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/metric"

	"go.openai.org/api/tunnel-client/pkg/types"
)

const (
	metricNameCommandEndToEndLatency = "command_end_to_end_latency_milliseconds"
	metricNameUnsupportedChannel     = "command_unsupported_channel_total"
)

type processorMetrics struct {
	commandEndToEndLatency metric.Float64Histogram
	unsupportedChannel     metric.Int64Counter
}

type latencyFlags struct {
	enqueuedRecorded bool
	pollRecorded     bool
}

func newProcessorMetrics(meter metric.Meter) (*processorMetrics, error) {
	if meter == nil {
		return nil, fmt.Errorf("meter cannot be nil")
	}

	commandEndToEndLatency, err := meter.Float64Histogram(
		metricNameCommandEndToEndLatency,
		metric.WithDescription("Latency in milliseconds from control-plane enqueue to final response delivery."),
		metric.WithUnit("ms"),
	)
	if err != nil {
		return nil, err
	}

	unsupportedChannel, err := meter.Int64Counter(
		metricNameUnsupportedChannel,
		metric.WithDescription("Count of polled commands dropped due to unsupported channels."),
		metric.WithUnit("{count}"),
	)
	if err != nil {
		return nil, err
	}

	return &processorMetrics{
		commandEndToEndLatency: commandEndToEndLatency,
		unsupportedChannel:     unsupportedChannel,
	}, nil
}

func (m *processorMetrics) recordCommandEndToEndLatency(ctx context.Context, latency time.Duration, attrs []attribute.KeyValue) {
	m.commandEndToEndLatency.Record(ctx, float64(latency/time.Millisecond), metric.WithAttributes(attrs...))
}

func (m *processorMetrics) recordUnsupportedChannel(ctx context.Context, attrs []attribute.KeyValue) {
	m.unsupportedChannel.Add(ctx, 1, metric.WithAttributes(attrs...))
}

func (m *processorMetrics) recordCommandLatencies(ctx context.Context, tunnelID types.TunnelID, statusCode int, attrs []attribute.KeyValue, enqueuedAt, polledAt time.Time, flags *latencyFlags) {
	baseAttrs := append(attrs, attribute.String("tunnel_id", tunnelID.String()), attribute.Int("tunnel_service_status", statusCode))

	if flags.enqueuedRecorded {
		// already emitted both latencies for this command
	} else if latency, ok := computeEndToEndLatency(enqueuedAt); ok {
		m.recordCommandEndToEndLatency(ctx, latency, append(baseAttrs, attribute.String("latency_type", "enqueue_to_response")))
		flags.enqueuedRecorded = true
	}

	if !flags.pollRecorded {
		m.recordCommandEndToEndLatency(ctx, time.Since(polledAt), append(baseAttrs, attribute.String("latency_type", "poll_to_response")))
		flags.pollRecorded = true
	}
}

func computeEndToEndLatency(enqueuedAt time.Time) (time.Duration, bool) {
	if enqueuedAt.IsZero() {
		return 0, false
	}
	latency := time.Since(enqueuedAt)
	if latency < 0 {
		return 0, false
	}
	return latency, true
}
