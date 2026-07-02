package dispatcherinternal

import (
	"context"
	"fmt"
	"log/slog"
	"sync"
	"time"

	"github.com/panjf2000/ants/v2"
	"go.opentelemetry.io/otel/metric"
	sdkmetric "go.opentelemetry.io/otel/sdk/metric"

	"go.openai.org/api/tunnel-client/pkg/config"
	"go.openai.org/api/tunnel-client/pkg/controlplane"
	tclog "go.openai.org/api/tunnel-client/pkg/log"
	"go.openai.org/api/tunnel-client/pkg/tunnelctx"
)

const poolReleaseTimeout = 10 * time.Second

type workerPool interface {
	Submit(func()) error
	ReleaseTimeout(time.Duration) error
	Cap() int
	Running() int
}

type antsWorkerPool struct {
	base *ants.Pool
}

func (p *antsWorkerPool) Submit(task func()) error {
	return p.base.Submit(task)
}

func (p *antsWorkerPool) ReleaseTimeout(timeout time.Duration) error {
	return p.base.ReleaseTimeout(timeout)
}

func (p *antsWorkerPool) Cap() int { return p.base.Cap() }

func (p *antsWorkerPool) Running() int { return p.base.Running() }

// QueueListener drains the polled command queue and forwards work to the processor
// using a bounded worker pool.
type QueueListener struct {
	logger    *slog.Logger
	processor Processor
	queue     controlplane.PolledCommandQueue
	pool      workerPool
	metrics   *queueListenerMetrics

	listenerWG sync.WaitGroup
}

type queueListenerMetrics struct {
	workerPoolCapacity  metric.Int64ObservableGauge
	workerPoolOccupancy metric.Int64ObservableGauge
}

func newQueueListenerMetrics(meter metric.Meter, pool workerPool) (*queueListenerMetrics, error) {
	if meter == nil {
		return nil, fmt.Errorf("dispatcher queue listener: nil meter")
	}
	if pool == nil {
		return nil, fmt.Errorf("dispatcher queue listener: nil worker pool")
	}

	workerPoolCapacity, err := meter.Int64ObservableGauge(
		"dispatcher_worker_pool_capacity",
		metric.WithDescription("Capacity of the dispatcher worker pool."),
		metric.WithUnit("{count}"),
	)
	if err != nil {
		return nil, err
	}

	workerPoolOccupancy, err := meter.Int64ObservableGauge(
		"dispatcher_worker_pool_occupancy",
		metric.WithDescription("Current occupancy (running workers) of the dispatcher worker pool."),
		metric.WithUnit("{count}"),
	)
	if err != nil {
		return nil, err
	}

	if _, err := meter.RegisterCallback(func(ctx context.Context, observer metric.Observer) error {
		observer.ObserveInt64(workerPoolCapacity, int64(pool.Cap()))
		observer.ObserveInt64(workerPoolOccupancy, int64(pool.Running()))
		return nil
	}, workerPoolCapacity, workerPoolOccupancy); err != nil {
		return nil, err
	}

	return &queueListenerMetrics{
		workerPoolCapacity:  workerPoolCapacity,
		workerPoolOccupancy: workerPoolOccupancy,
	}, nil
}

type poolFactory func(maxConcurrent int) (workerPool, error)

// NewQueueListener constructs a QueueListener with a worker pool sized according to the MCP configuration.
func NewQueueListener(logger *slog.Logger, processor Processor, queue controlplane.PolledCommandQueue, mcpConfig *config.MCPConfig, meterProvider *sdkmetric.MeterProvider) (*QueueListener, error) {
	return newQueueListener(logger, processor, queue, mcpConfig, meterProvider, func(maxConcurrent int) (workerPool, error) {
		pool, err := ants.NewPool(maxConcurrent)
		if err != nil {
			return nil, fmt.Errorf("dispatcher queue listener: create worker pool: %w", err)
		}
		return &antsWorkerPool{base: pool}, nil
	})
}

func newQueueListener(logger *slog.Logger, processor Processor, queue controlplane.PolledCommandQueue, mcpConfig *config.MCPConfig, meterProvider *sdkmetric.MeterProvider, makePool poolFactory) (*QueueListener, error) {
	if logger == nil {
		return nil, fmt.Errorf("dispatcher queue listener: nil logger")
	}
	if processor == nil {
		return nil, fmt.Errorf("dispatcher queue listener: nil processor")
	}
	if queue == nil {
		return nil, fmt.Errorf("dispatcher queue listener: nil queue")
	}
	if mcpConfig == nil {
		return nil, fmt.Errorf("dispatcher queue listener: nil MCP config")
	}
	if mcpConfig.MaxConcurrentRequests <= 0 {
		return nil, fmt.Errorf("dispatcher queue listener: non-positive max concurrent requests")
	}
	if meterProvider == nil {
		return nil, fmt.Errorf("dispatcher queue listener: nil meter provider")
	}

	if makePool == nil {
		return nil, fmt.Errorf("dispatcher queue listener: nil pool factory")
	}

	pool, err := makePool(mcpConfig.MaxConcurrentRequests)
	if err != nil {
		return nil, err
	}

	baseLogger := logger.With(tclog.FieldComponent, tclog.ComponentDispatcher)
	metrics, err := newQueueListenerMetrics(meterProvider.Meter("dispatcher"), pool)
	if err != nil {
		return nil, fmt.Errorf("dispatcher queue listener: %w", err)
	}

	return &QueueListener{
		logger:    baseLogger,
		processor: processor,
		queue:     queue,
		pool:      pool,
		metrics:   metrics,
	}, nil
}

// Start begins draining the queue until the provided context is canceled or the queue is closed.
func (l *QueueListener) Start(ctx context.Context) {
	l.listenerWG.Add(1)
	go func() {
		defer l.listenerWG.Done()
		l.run(ctx)
	}()
}

// Wait blocks until the listener has stopped processing commands.
func (l *QueueListener) Wait() {
	l.listenerWG.Wait()
}

func (l *QueueListener) run(ctx context.Context) {
	defer func() {
		if err := l.pool.ReleaseTimeout(poolReleaseTimeout); err != nil {
			l.logger.WarnContext(ctx, "failed to release dispatcher worker pool",
				slog.String("error", err.Error()))
		}
	}()

	for {
		select {
		case <-ctx.Done():
			return
		case cmd, ok := <-l.queue:
			if !ok {
				return
			}

			requestID := cmd.RequestID().String()
			cmdCopy := cmd
			cmdCtx := tunnelctx.ContextWithRequestID(ctx, requestID)
			if sessionID, ok := cmd.SessionID(); ok {
				cmdCtx = tunnelctx.ContextWithSessionID(cmdCtx, sessionID)
			}
			cmdLogger := tclog.LoggerWithContextIdentifiers(cmdCtx, l.logger)

			if err := l.pool.Submit(func() {
				if err := l.processor.Process(cmdCtx, cmdCopy); err != nil {
					cmdLogger.WarnContext(cmdCtx, "failed to process polled command",
						slog.String("error", err.Error()))
				}
			}); err != nil {
				cmdLogger.ErrorContext(cmdCtx, "failed to submit polled command to worker pool",
					slog.String("error", err.Error()))
				// We already pulled the command off the queue, and tunnel-service will not re-deliver.
				// Fall back to processing in-line to avoid dropping the request on the floor.
				if err := l.processor.Process(cmdCtx, cmdCopy); err != nil {
					cmdLogger.WarnContext(cmdCtx, "failed to process polled command (inline fallback after submit failure)",
						slog.String("error", err.Error()))
				}
				continue
			}
		}
	}
}
