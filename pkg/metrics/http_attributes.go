package metrics

import (
	"net/http"

	"go.opentelemetry.io/contrib/instrumentation/net/http/otelhttp"
	"go.opentelemetry.io/otel/attribute"
)

// MetricAttributesForRequest derives common HTTP attributes for Prometheus/OpenTelemetry metrics.
func MetricAttributesForRequest(req *http.Request) []attribute.KeyValue {
	if req == nil || req.URL == nil {
		return nil
	}

	path := req.URL.EscapedPath()
	if path == "" {
		path = "/"
	}

	return []attribute.KeyValue{
		attribute.String("http.route", path),
	}
}

type httpClientMetricAttributesRoundTripper struct {
	base http.RoundTripper
}

// WithHTTPClientMetricAttributes adds common HTTP attributes to the enclosing otelhttp transport.
func WithHTTPClientMetricAttributes(base http.RoundTripper) http.RoundTripper {
	return &httpClientMetricAttributesRoundTripper{base: base}
}

func (t *httpClientMetricAttributesRoundTripper) RoundTrip(req *http.Request) (*http.Response, error) {
	if labeler, ok := otelhttp.LabelerFromContext(req.Context()); ok {
		labeler.Add(MetricAttributesForRequest(req)...)
	}
	return t.base.RoundTrip(req)
}
