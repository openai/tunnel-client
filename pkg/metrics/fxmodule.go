package metrics

import (
	"context"
	"net/http"

	"github.com/prometheus/client_golang/prometheus/promhttp"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/exporters/prometheus"
	"go.opentelemetry.io/otel/sdk/metric"
	"go.opentelemetry.io/otel/sdk/trace"
	"go.uber.org/fx"
)

// MetricModule provides the OpenTelemetry MeterProvider and the Prometheus HTTP handler.
var MetricModule = fx.Module("metrics", fx.Options(
	fx.Provide(
		fx.Annotate(
			NewMetricsHandler,
			fx.As(new(MetricsExporter)),
		)),
	fx.Provide(NewMeterProvider),
	fx.Provide(func() *trace.TracerProvider {
		return trace.NewTracerProvider()
	}),
))

// MetricsExporter implements the http.Handler interface for Prometheus metrics.
type MetricsExporter interface {
	http.Handler
}

// NewMeterProvider creates an OpenTelemetry MeterProvider with the Prometheus exporter.
func NewMeterProvider(lc fx.Lifecycle) (*metric.MeterProvider, error) {
	exporter, err := prometheus.New()
	if err != nil {
		return nil, err
	}

	provider := metric.NewMeterProvider(metric.WithReader(exporter))
	otel.SetMeterProvider(provider)

	// Register lifecycle hooks to shut down the MeterProvider cleanly.
	lc.Append(fx.Hook{
		OnStop: func(ctx context.Context) error {
			return provider.Shutdown(ctx)
		},
	})

	return provider, nil
}

// NewMetricsHandler creates an HTTP handler for the Prometheus metrics.
func NewMetricsHandler() http.Handler {
	return promhttp.Handler()
}
