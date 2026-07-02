package fx

import (
	"context"
	"io"
	"log/slog"
	"testing"
	"time"

	sdkmetric "go.opentelemetry.io/otel/sdk/metric"
	"go.uber.org/fx"
	"go.uber.org/fx/fxtest"

	"github.com/openai/tunnel-client/pkg/controlplane"
	"github.com/openai/tunnel-client/pkg/controlplane/internal"
	"github.com/openai/tunnel-client/pkg/types"
)

func TestRunPollerStartsEvenWhenFetcherBlocks(t *testing.T) {
	queue := make(controlplane.PolledCommandQueue, 1)
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))

	fetcher := &blockingFetcher{started: make(chan struct{}, 1)}
	queueAdapter := &queueAdapter{
		queue:  queue,
		logger: logger,
	}
	meterProvider := sdkmetric.NewMeterProvider()
	t.Cleanup(func() {
		_ = meterProvider.Shutdown(context.Background())
	})

	poller, err := internal.NewPoller(queueAdapter, fetcher, logger, meterProvider.Meter("test"), time.Millisecond*50, 0, 0, 0)
	if err != nil {
		t.Fatalf("new poller: %v", err)
	}

	app := fxtest.New(
		t,
		fx.Supply(logger),
		fx.Supply(fx.Annotate(poller, fx.As(new(internal.Poller)))),
		fx.Invoke(runPoller),
	)

	app.RequireStart()
	select {
	case <-fetcher.started:
	case <-time.After(time.Second):
		t.Fatal("poller did not start poll loop")
	}
	app.RequireStop()
}

type blockingFetcher struct {
	started chan struct{}
}

func (f *blockingFetcher) Poll(ctx context.Context, limit int) ([]controlplane.PolledCommand, types.TunnelServiceRequestID, error) {
	select {
	case f.started <- struct{}{}:
	default:
	}
	<-ctx.Done()
	return nil, "", ctx.Err()
}
