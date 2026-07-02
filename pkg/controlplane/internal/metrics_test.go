package internal

import (
	"context"
	"testing"

	noopmetric "go.opentelemetry.io/otel/metric/noop"

	"github.com/openai/tunnel-client/pkg/controlplane"

	"github.com/stretchr/testify/require"
)

type stubQueue struct{}

func (stubQueue) Capacity() int { return 1 }
func (stubQueue) Length() int   { return 0 }
func (stubQueue) Enqueue(ctx context.Context, cmd controlplane.PolledCommand) bool {
	return true
}

func TestNewPollerMetricsRejectsNilMeter(t *testing.T) {
	t.Parallel()

	_, err := newPollerMetrics(nil, stubQueue{})
	require.Error(t, err)
	require.Contains(t, err.Error(), "meter cannot be nil")
}

func TestNewPollerMetricsRejectsNilQueue(t *testing.T) {
	t.Parallel()

	meterProvider := noopmetric.NewMeterProvider()
	_, err := newPollerMetrics(meterProvider.Meter("test"), nil)
	require.Error(t, err)
	require.Contains(t, err.Error(), "queue cannot be nil")
}
