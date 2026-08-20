package harpoon

import (
	"net/http"

	"go.opentelemetry.io/otel/metric"

	runtimeharpoon "github.com/openai/tunnel-client/pkg/runtimeharpoon"
)

// ServerOption is the shared runtime-core option type.
type ServerOption = runtimeharpoon.ServerOption

// WithMeter configures the meter used for Harpoon metrics.
func WithMeter(meter metric.Meter) ServerOption {
	return runtimeharpoon.WithMeter(meter)
}

// WithHTTPTransport sets the HTTP transport used for Harpoon outbound calls.
func WithHTTPTransport(rt http.RoundTripper) ServerOption {
	return runtimeharpoon.WithHTTPTransport(rt)
}
