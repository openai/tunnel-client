package internal

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"time"

	"github.com/jpillora/backoff"
	"github.com/openai/tunnel-client/pkg/config"
	"github.com/openai/tunnel-client/pkg/controlplane"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/metric"

	tclog "github.com/openai/tunnel-client/pkg/log"
)

const (
	defaultQueueFullDelay = 100 * time.Millisecond
	defaultBackoffMin     = 200 * time.Millisecond
	defaultBackoffMax     = 10 * time.Second
	maxPollBatchSize      = 25

	dropReasonInvalidCommandType = "invalid_command_type"
)

// queue exposes the minimal methods the poller needs from the dispatcher queue.
type queue interface {
	Capacity() int
	Length() int
	Enqueue(ctx context.Context, cmd controlplane.PolledCommand) bool
}

// Poller exposes the polling loop to callers outside this package.
type Poller interface {
	Run(ctx context.Context)
}

// poller coordinates polling the control plane and publishing work items to the
// dispatcher queue. It manages basic retry/backoff behavior and ensures it does
// not enqueue more work than the queue can hold.
type poller struct {
	queue          queue
	fetcher        controlplane.Fetcher
	logger         *slog.Logger
	backoff        *backoff.Backoff
	queueFullDelay time.Duration
	pollTimeout    time.Duration
	pollGuardrail  time.Duration
	metrics        *pollerMetrics
	hadPollError   bool
}

// NewPoller builds a Poller with sensible defaults for retry and queue
// backpressure handling. A nil logger defaults to slog.Default(). backoffMin /
// backoffMax override the default retry window when non-zero; zero values
// preserve defaults.
func NewPoller(q queue, fetcher controlplane.Fetcher, logger *slog.Logger, meter metric.Meter, pollTimeout, pollDeadlineGuardrail, backoffMin, backoffMax time.Duration) (Poller, error) {
	if q == nil {
		return nil, fmt.Errorf("controlplane internal poller: queue cannot be nil")
	}
	if fetcher == nil {
		return nil, fmt.Errorf("controlplane internal poller: fetcher cannot be nil")
	}
	if logger == nil {
		logger = slog.Default()
	}
	pollConfig := config.ControlPlaneConfig{
		PollTimeout:           pollTimeout,
		PollDeadlineGuardrail: pollDeadlineGuardrail,
	}
	pollTimeout = pollConfig.PollTimeoutOrDefault()
	pollDeadlineGuardrail = pollConfig.PollDeadlineGuardrailOrDefault()

	minBackoff := defaultBackoffMin
	if backoffMin > 0 {
		minBackoff = backoffMin
	}
	maxBackoff := defaultBackoffMax
	if backoffMax > 0 {
		maxBackoff = backoffMax
	}

	p := &poller{
		queue:   q,
		fetcher: fetcher,
		logger:  logger,
		backoff: &backoff.Backoff{
			Min:    minBackoff,
			Max:    maxBackoff,
			Factor: 2,
			Jitter: true,
		},
		queueFullDelay: defaultQueueFullDelay,
		pollTimeout:    pollTimeout,
		pollGuardrail:  pollDeadlineGuardrail,
	}
	if m, err := newPollerMetrics(meter, q); err != nil {
		return nil, err
	} else {
		p.metrics = m
		return p, nil
	}
}

// Run starts the polling loop and blocks until the context is cancelled.
func (p *poller) Run(ctx context.Context) {
	p.logger.InfoContext(ctx, "poller started")
	defer func() {
		if err := ctx.Err(); err != nil {
			p.logger.InfoContext(ctx, "poller stopped", slog.String("reason", err.Error()))
			return
		}
		p.logger.InfoContext(ctx, "poller stopped")
	}()

	for {
		select {
		case <-ctx.Done():
			return
		default:
		}

		available := p.availableSlots()
		if available <= 0 {
			if !p.waitForQueue(ctx) {
				return
			}
			continue
		}

		limit := pollLimit(available)
		p.logger.DebugContext(ctx, "poll cycle started", slog.Int("limit", limit))
		p.metrics.totalCyclesStarted.Add(ctx, 1)

		pollStart := time.Now()
		pollDeadline := (config.ControlPlaneConfig{
			PollTimeout:           p.pollTimeout,
			PollDeadlineGuardrail: p.pollGuardrail,
		}).PollDeadlineTimeoutOrDefault()
		pollCtx, cancel := context.WithTimeout(ctx, pollDeadline)
		commands, tunnelServiceRequestID, err := p.fetcher.Poll(pollCtx, limit)
		cancel()
		p.metrics.pollLatency.Record(ctx, time.Since(pollStart).Seconds(), metric.WithAttributes(attribute.Bool("error", err != nil)))
		if err != nil {
			if ctx.Err() != nil && errors.Is(err, context.Canceled) {
				return
			}
			p.hadPollError = true
			p.metrics.pollErrors.Add(ctx, 1, metric.WithAttributes(attribute.String(attributeKeyErrorKind, pollErrorKind(err))))
			delay := p.backoff.Duration()
			requestIDValue := tunnelServiceRequestID.String()
			if requestIDValue == "" {
				requestIDValue = "missing_request_id"
			}
			attrs := []any{
				slog.String("error", err.Error()),
				slog.Int64("retry_in_ms", delay.Milliseconds()),
				slog.String(tclog.FieldTunnelServiceRequestID, requestIDValue),
			}
			var statusErr *APIStatusError
			if errors.As(err, &statusErr) {
				attrs = append(attrs,
					slog.Int("status_code", statusErr.StatusCode()),
					slog.String("status", statusErr.Status()),
				)
				if statusErr.Code() != "" {
					attrs = append(attrs, slog.String("error_code", statusErr.Code()))
				}
				if statusErr.Message() != "" {
					attrs = append(attrs, slog.String("error_message", statusErr.Message()))
				}
				if statusErr.Mitigation() != "" {
					attrs = append(attrs, slog.String("mitigation", statusErr.Mitigation()))
				}
			}
			if errors.Is(err, context.DeadlineExceeded) {
				attrs = append(attrs, slog.Int64("poll_timeout_ms", pollTimeoutMilliseconds(p.pollTimeout)))
				attrs = append(attrs, slog.Int64("poll_deadline_ms", pollDeadline.Milliseconds()))
				p.logger.WarnContext(ctx, "poll timed out; backing off", attrs...)
			} else {
				p.logger.WarnContext(ctx, "poll failed; backing off", attrs...)
			}
			if !p.sleep(ctx, delay) {
				return
			}
			continue
		}

		if p.hadPollError {
			attrs := []any{}
			if tunnelServiceRequestID != "" {
				attrs = append(attrs, slog.String(tclog.FieldTunnelServiceRequestID, tunnelServiceRequestID.String()))
			}
			p.logger.InfoContext(ctx, "poller recovered; polling operational", attrs...)
			p.hadPollError = false
		}

		p.backoff.Reset()
		p.metrics.lastSuccessUnixSeconds.Store(time.Now().Unix())

		pulled := len(commands)
		if pulled == 0 {
			p.logger.DebugContext(ctx, "poll cycle complete", slog.Int("commands_polled", 0), slog.Int("commands_enqueued", 0))
			continue
		}

		if pulled > available {
			p.logger.ErrorContext(ctx, "more commands polled than available slots. "+
				"tunnel-service is not respecting limit request and overflowing client", slog.Int("polled", pulled), slog.Int("available", available))
		}

		enqueued := 0
		droppedInvalidType := 0
		droppedContextClosed := 0
		for idx, cmd := range commands {
			tc, ok := cmd.(typedCommand)
			if !ok || tc.commandType() == "" {
				p.logger.WarnContext(ctx, "dropping command with unknown type")
				droppedInvalidType++
				continue
			}

			p.recordCommandAge(ctx, tc)
			if !p.enqueueWithBackpressure(ctx, tc) {
				// We already pulled this command from the control-plane; if the client is
				// shutting down we can't safely block forever. Count the remaining
				// commands as dropped due to context closure.
				droppedContextClosed++
				for _, rest := range commands[idx+1:] {
					restTC, ok := rest.(typedCommand)
					if !ok || restTC.commandType() == "" {
						droppedInvalidType++
						continue
					}
					droppedContextClosed++
				}
				break
			}
			enqueued++
		}

		p.metrics.totalCommandsPolled.Add(ctx, int64(pulled))
		p.metrics.totalCommandsEnqueued.Add(ctx, int64(enqueued))
		if droppedInvalidType > 0 {
			p.metrics.queueDrops.Add(ctx, int64(droppedInvalidType), metric.WithAttributes(attribute.String(attributeKeyDropReason, dropReasonInvalidCommandType)))
		}
		if droppedContextClosed > 0 {
			p.metrics.queueDrops.Add(ctx, int64(droppedContextClosed), metric.WithAttributes(attribute.String(attributeKeyDropReason, dropReasonContextClosed)))
		}

		p.logger.DebugContext(ctx, "poll cycle complete",
			slog.Int("commands_polled", pulled),
			slog.Int("commands_enqueued", enqueued),
		)
	}
}

func (p *poller) enqueue(ctx context.Context, cmd controlplane.PolledCommand) bool {
	return p.queue.Enqueue(ctx, cmd)
}

func (p *poller) enqueueWithBackpressure(ctx context.Context, cmd controlplane.PolledCommand) bool {
	for {
		if ctx.Err() != nil {
			return false
		}

		if p.enqueue(ctx, cmd) {
			return true
		}

		if !p.waitForQueue(ctx) {
			return false
		}
	}
}

func (p *poller) availableSlots() int {
	capacity := p.queue.Capacity()
	if capacity == 0 {
		// Treat unbuffered channels as having a single available slot to avoid zero limits.
		return 1
	}
	available := capacity - p.queue.Length()
	if available < 0 {
		return 0
	}
	return available
}

func pollLimit(available int) int {
	if available > maxPollBatchSize {
		return maxPollBatchSize
	}
	return available
}

func (p *poller) waitForQueue(ctx context.Context) bool {
	timer := time.NewTimer(p.queueFullDelay)
	defer timer.Stop()

	select {
	case <-ctx.Done():
		return false
	case <-timer.C:
		return true
	}
}

func (p *poller) sleep(ctx context.Context, d time.Duration) bool {
	if d <= 0 {
		d = defaultBackoffMin
	}
	timer := time.NewTimer(d)
	defer timer.Stop()

	select {
	case <-ctx.Done():
		return false
	case <-timer.C:
		return true
	}
}

func pollErrorKind(err error) string {
	switch {
	case errors.Is(err, context.DeadlineExceeded):
		return errorKindTimeout
	case errors.Is(err, context.Canceled):
		return errorKindContextCanceled
	default:
		var netErr net.Error
		if errors.As(err, &netErr) && netErr.Timeout() {
			return errorKindTimeout
		}
		return errorKindOther
	}
}

func (p *poller) recordCommandAge(ctx context.Context, cmd controlplane.PolledCommand) {
	enqueuedAt := cmd.EnqueuedAt()
	polledAt := cmd.PolledAt()
	if enqueuedAt.IsZero() || polledAt.IsZero() {
		return
	}

	age := polledAt.Sub(enqueuedAt).Seconds()
	if age < 0 {
		return
	}

	p.metrics.commandAge.Record(ctx, age)
}
