package proxyhealth

import (
	"context"

	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/metric"
	sdkmetric "go.opentelemetry.io/otel/sdk/metric"

	"go.openai.org/api/tunnel-client/pkg/proxy"
)

type proxyMetrics struct {
	healthGauge      metric.Int64ObservableGauge
	lastCheckGauge   metric.Int64ObservableGauge
	lastSuccessGauge metric.Int64ObservableGauge
	phaseDuration    metric.Float64Histogram
	failureCounter   metric.Int64Counter
}

func newProxyMetrics(meterProvider *sdkmetric.MeterProvider, checker *Checker) (*proxyMetrics, error) {
	if meterProvider == nil {
		return nil, nil
	}
	meter := meterProvider.Meter("proxyhealth")
	healthGauge, err := meter.Int64ObservableGauge(
		"proxy_health_state",
		metric.WithDescription("Reports 1 when the proxy is healthy and 0 when unhealthy"),
	)
	if err != nil {
		return nil, err
	}
	lastCheckGauge, err := meter.Int64ObservableGauge(
		"proxy_last_check_timestamp",
		metric.WithDescription("Unix timestamp of the last proxy check"),
	)
	if err != nil {
		return nil, err
	}
	lastSuccessGauge, err := meter.Int64ObservableGauge(
		"proxy_last_success_timestamp",
		metric.WithDescription("Unix timestamp of the last successful proxy check"),
	)
	if err != nil {
		return nil, err
	}
	phaseDuration, err := meter.Float64Histogram(
		"proxy_check_phase_duration_seconds",
		metric.WithDescription("Proxy check phase durations"),
	)
	if err != nil {
		return nil, err
	}
	failureCounter, err := meter.Int64Counter(
		"proxy_check_failures",
		metric.WithDescription("Proxy check failures by phase"),
	)
	if err != nil {
		return nil, err
	}

	metrics := &proxyMetrics{
		healthGauge:      healthGauge,
		lastCheckGauge:   lastCheckGauge,
		lastSuccessGauge: lastSuccessGauge,
		phaseDuration:    phaseDuration,
		failureCounter:   failureCounter,
	}

	_, err = meter.RegisterCallback(func(ctx context.Context, observer metric.Observer) error {
		if checker == nil {
			return nil
		}
		snapshots := checker.healthSnapshot()
		for _, snapshot := range snapshots {
			if snapshot.route.ProxyID == "" {
				continue
			}
			attrs := attribute.NewSet(attribute.String("proxy_id", snapshot.route.ProxyID))
			observer.ObserveInt64(metrics.healthGauge, healthStateValue(snapshot.healthState), metric.WithAttributeSet(attrs))
			if !snapshot.lastCheck.IsZero() {
				observer.ObserveInt64(metrics.lastCheckGauge, snapshot.lastCheck.Unix(), metric.WithAttributeSet(attrs))
			}
			if !snapshot.lastSuccess.IsZero() {
				observer.ObserveInt64(metrics.lastSuccessGauge, snapshot.lastSuccess.Unix(), metric.WithAttributeSet(attrs))
			}
		}
		return nil
	}, healthGauge, lastCheckGauge, lastSuccessGauge)
	if err != nil {
		return nil, err
	}

	return metrics, nil
}

func (m *proxyMetrics) recordCheck(route proxy.Route, record CheckRecord) {
	if m == nil || route.ProxyID == "" {
		return
	}
	attrs := attribute.NewSet(
		attribute.String("proxy_id", route.ProxyID),
		attribute.String("phase", "tcp"),
	)
	if record.TCPDurationMS > 0 {
		m.phaseDuration.Record(context.Background(), float64(record.TCPDurationMS)/1000.0, metric.WithAttributeSet(attrs))
	}
	if record.ConnectDurationMS > 0 {
		connectAttrs := attribute.NewSet(
			attribute.String("proxy_id", route.ProxyID),
			attribute.String("phase", "connect"),
		)
		m.phaseDuration.Record(context.Background(), float64(record.ConnectDurationMS)/1000.0, metric.WithAttributeSet(connectAttrs))
	}
}

func (m *proxyMetrics) recordFailure(route proxy.Route, phase, reason string) {
	if m == nil || route.ProxyID == "" {
		return
	}
	attrs := attribute.NewSet(
		attribute.String("proxy_id", route.ProxyID),
		attribute.String("phase", phase),
		attribute.String("reason", reason),
	)
	m.failureCounter.Add(context.Background(), 1, metric.WithAttributeSet(attrs))
}

func (c *Checker) healthSnapshot() []routeStatus {
	c.statusMu.RLock()
	defer c.statusMu.RUnlock()
	out := make([]routeStatus, 0, len(c.routeStatus))
	for _, status := range c.routeStatus {
		out = append(out, *status)
	}
	return out
}

func healthStateValue(state HealthState) int64 {
	if state == HealthStateHealthy {
		return 1
	}
	return 0
}
