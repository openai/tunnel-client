package metrics

import (
	"context"
	"net/http"
	"net/http/httptest"
	"net/url"
	"testing"

	"github.com/stretchr/testify/require"
	"go.opentelemetry.io/contrib/instrumentation/net/http/otelhttp"
	"go.opentelemetry.io/otel/attribute"
	sdkmetric "go.opentelemetry.io/otel/sdk/metric"
	"go.opentelemetry.io/otel/sdk/metric/metricdata"
)

func TestMetricAttributesForRequest(t *testing.T) {
	t.Run("nil request", func(t *testing.T) {
		require.Nil(t, MetricAttributesForRequest(nil))
	})

	t.Run("nil URL", func(t *testing.T) {
		req := &http.Request{}
		require.Nil(t, MetricAttributesForRequest(req))
	})

	t.Run("empty path coerces to slash", func(t *testing.T) {
		req := &http.Request{URL: &url.URL{}}
		attrs := MetricAttributesForRequest(req)

		require.Len(t, attrs, 1)
		require.Equal(t, "http.route", string(attrs[0].Key))
		require.Equal(t, "/", attrs[0].Value.AsString())
	})

	t.Run("non-empty path uses escaped path", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodGet, "https://example.com/v1/hello%20world", nil)
		attrs := MetricAttributesForRequest(req)

		require.Len(t, attrs, 1)
		require.Equal(t, "http.route", string(attrs[0].Key))
		require.Equal(t, "/v1/hello%20world", attrs[0].Value.AsString())
	})
}

func TestHTTPClientMetricAttributesRoundTripper(t *testing.T) {
	for _, tc := range []struct {
		name      string
		url       string
		wantRoute string
	}{
		{name: "empty path coerces to slash", url: "https://example.com", wantRoute: "/"},
		{name: "non-empty path uses escaped path", url: "https://example.com/v1/hello%20world", wantRoute: "/v1/hello%20world"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ctx := context.Background()
			reader := sdkmetric.NewManualReader()
			provider := sdkmetric.NewMeterProvider(sdkmetric.WithReader(reader))
			t.Cleanup(func() {
				require.NoError(t, provider.Shutdown(ctx))
			})

			base := testRoundTripperFunc(func(req *http.Request) (*http.Response, error) {
				labeler, ok := otelhttp.LabelerFromContext(req.Context())
				require.True(t, ok)
				require.Contains(t, labeler.Get(), attribute.String("http.route", tc.wantRoute))
				return &http.Response{
					StatusCode: http.StatusNoContent,
					Header:     make(http.Header),
					Body:       http.NoBody,
					Request:    req,
				}, nil
			})
			transport := otelhttp.NewTransport(WithHTTPClientMetricAttributes(base), otelhttp.WithMeterProvider(provider))

			req, err := http.NewRequestWithContext(ctx, http.MethodGet, tc.url, http.NoBody)
			require.NoError(t, err)
			resp, err := transport.RoundTrip(req)
			require.NoError(t, err)
			require.NoError(t, resp.Body.Close())

			var rm metricdata.ResourceMetrics
			require.NoError(t, reader.Collect(ctx, &rm))
			require.True(t, hasHTTPRouteMetric(rm, tc.wantRoute))
		})
	}
}

func hasHTTPRouteMetric(rm metricdata.ResourceMetrics, wantRoute string) bool {
	for _, scope := range rm.ScopeMetrics {
		for _, metric := range scope.Metrics {
			if metric.Name != "http.client.request.duration" {
				continue
			}
			histogram, ok := metric.Data.(metricdata.Histogram[float64])
			if !ok {
				continue
			}
			for _, point := range histogram.DataPoints {
				if route, ok := point.Attributes.Value(attribute.Key("http.route")); ok && route.AsString() == wantRoute {
					return true
				}
			}
		}
	}
	return false
}

type testRoundTripperFunc func(*http.Request) (*http.Response, error)

func (f testRoundTripperFunc) RoundTrip(req *http.Request) (*http.Response, error) {
	return f(req)
}
