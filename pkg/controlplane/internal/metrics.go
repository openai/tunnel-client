package internal

import (
	"context"
	"fmt"
	"sync/atomic"

	"go.opentelemetry.io/otel/metric"
)

type pollerMetrics struct {
	totalCommandsPolled   metric.Int64Counter         // number of commands received from control plane
	totalCommandsEnqueued metric.Int64Counter         // number of commands successfully pushed into dispatcher queue
	totalCyclesStarted    metric.Int64Counter         // number of poll cycles initiated
	pollLatency           metric.Float64Histogram     // observed latency per poll request
	pollErrors            metric.Int64Counter         // number of poll errors, tagged by error kind
	queueDrops            metric.Int64Counter         // number of commands dropped when queue is unavailable
	commandAge            metric.Float64Histogram     // age of commands from enqueue to poll
	lastSuccessGauge      metric.Int64ObservableGauge // timestamp of last successful poll
	queueCapacityGauge    metric.Int64ObservableGauge // observed internal queue capacity
	queueLengthGauge      metric.Int64ObservableGauge // observed internal queue length

	lastSuccessUnixSeconds atomic.Int64 // cached timestamp backing lastSuccessGauge
}

const (
	metricNameCommandsPolled      = "commands_polled"
	metricNameCommandsEnqueued    = "commands_enqueued"
	metricNameCommandsPollCycles  = "commands_poll_cycles"
	metricNameCommandsPollLatency = "commands_poll_latency"
	metricNameCommandsPollErrors  = "commands_poll_errors"
	metricNameCommandsQueueDrops  = "commands_queue_drops"
	metricNameCommandsAge         = "commands_age"
	metricNamePollLastSuccess     = "commands_poll_last_successful_timestamp_seconds"
	metricNameQueueCapacity       = "commands_queue_capacity"
	metricNameQueueLength         = "commands_queue_length"

	attributeKeyErrorKind  = "error_kind"
	attributeKeyDropReason = "drop_reason"

	errorKindTimeout         = "timeout"
	errorKindContextCanceled = "context_canceled"
	errorKindOther           = "other"

	dropReasonQueueFull     = "queue_full"
	dropReasonContextClosed = "context_canceled"
)

func newPollerMetrics(meter metric.Meter, queue queue) (*pollerMetrics, error) {
	if meter == nil {
		return nil, fmt.Errorf("meter cannot be nil")
	}
	if queue == nil {
		return nil, fmt.Errorf("queue cannot be nil")
	}

	pm := &pollerMetrics{}

	var err error
	if pm.totalCommandsPolled, err = meter.Int64Counter(
		metricNameCommandsPolled,
		metric.WithDescription("Total number of control-plane commands fetched by the poller."),
		metric.WithUnit("{count}"),
	); err != nil {
		return nil, err
	}

	if pm.totalCommandsEnqueued, err = meter.Int64Counter(
		metricNameCommandsEnqueued,
		metric.WithDescription("Total number of control-plane commands successfully enqueued."),
		metric.WithUnit("{count}"),
	); err != nil {
		return nil, err
	}

	if pm.totalCyclesStarted, err = meter.Int64Counter(
		metricNameCommandsPollCycles,
		metric.WithDescription("Total number of poll cycles initiated by the poller."),
		metric.WithUnit("{count}"),
	); err != nil {
		return nil, err
	}

	if pm.pollLatency, err = meter.Float64Histogram(
		metricNameCommandsPollLatency,
		metric.WithDescription("Latency in seconds for completed poll requests."),
		metric.WithUnit("s"),
	); err != nil {
		return nil, err
	}

	if pm.pollErrors, err = meter.Int64Counter(
		metricNameCommandsPollErrors,
		metric.WithDescription("Total number of control-plane poll errors encountered."),
		metric.WithUnit("{count}"),
	); err != nil {
		return nil, err
	}

	if pm.queueDrops, err = meter.Int64Counter(
		metricNameCommandsQueueDrops,
		metric.WithDescription("Total number of commands dropped when enqueueing into the dispatcher queue."),
		metric.WithUnit("{count}"),
	); err != nil {
		return nil, err
	}

	if pm.commandAge, err = meter.Float64Histogram(
		metricNameCommandsAge,
		metric.WithDescription("Age in seconds between control-plane enqueue and client poll."),
		metric.WithUnit("s"),
	); err != nil {
		return nil, err
	}

	if pm.lastSuccessGauge, err = meter.Int64ObservableGauge(
		metricNamePollLastSuccess,
		metric.WithDescription("Unix timestamp in seconds of the last successful poll."),
		metric.WithUnit("s"),
	); err != nil {
		return nil, err
	}

	if pm.queueCapacityGauge, err = meter.Int64ObservableGauge(
		metricNameQueueCapacity,
		metric.WithDescription("Capacity of the dispatcher queue that receives commands from the poller."),
		metric.WithUnit("{count}"),
	); err != nil {
		return nil, err
	}

	if pm.queueLengthGauge, err = meter.Int64ObservableGauge(
		metricNameQueueLength,
		metric.WithDescription("Current occupancy (length) of the dispatcher queue that receives commands from the poller."),
		metric.WithUnit("{count}"),
	); err != nil {
		return nil, err
	}

	if _, err := meter.RegisterCallback(func(ctx context.Context, observer metric.Observer) error {
		observer.ObserveInt64(pm.lastSuccessGauge, pm.lastSuccessUnixSeconds.Load())
		observer.ObserveInt64(pm.queueCapacityGauge, int64(queue.Capacity()))
		observer.ObserveInt64(pm.queueLengthGauge, int64(queue.Length()))
		return nil
	}, pm.lastSuccessGauge, pm.queueCapacityGauge, pm.queueLengthGauge); err != nil {
		return nil, err
	}

	return pm, nil
}
