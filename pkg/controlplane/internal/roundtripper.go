package internal

import (
	"context"
	"fmt"
	"log/slog"
	"net/http"

	tctransport "go.openai.org/api/tunnel-client/pkg/transport"
	"go.openai.org/api/tunnel-client/pkg/version"
)

const (
	headerTunnelClientName    = "X-Tunnel-Client-Name"
	headerTunnelClientVersion = "X-Tunnel-Client-Version"
)

type controlPlaneRoundTripper struct {
	base         http.RoundTripper
	apiKey       string
	userAgent    string
	extraHeaders map[string]string
	logger       *slog.Logger
}

func newControlPlaneRoundTripper(base http.RoundTripper, apiKey, userAgent string, extraHeaders map[string]string, logger *slog.Logger) http.RoundTripper {
	if base == nil {
		base = tctransport.CloneDefault()
	}
	if logger == nil {
		panic("control-plane round tripper: logger is required")
	}
	return &controlPlaneRoundTripper{
		base:         base,
		apiKey:       apiKey,
		userAgent:    userAgent,
		extraHeaders: extraHeaders,
		logger:       logger,
	}
}

func (c *controlPlaneRoundTripper) RoundTrip(req *http.Request) (*http.Response, error) {
	req.Header.Set("Authorization", fmt.Sprintf("Bearer %s", c.apiKey))
	req.Header.Set("Accept", "application/json")
	req.Header.Set("User-Agent", c.userAgent)
	req.Header.Set(headerTunnelClientName, version.ClientName)
	req.Header.Set(headerTunnelClientVersion, version.Version)
	c.applyExtraHeaders(req.Context(), req.Header)

	return c.base.RoundTrip(req)
}

func (c *controlPlaneRoundTripper) applyExtraHeaders(ctx context.Context, headers http.Header) {
	if len(c.extraHeaders) == 0 {
		return
	}

	for k, v := range c.extraHeaders {
		if existing := headers.Get(k); existing != "" && existing != v {
			c.logger.WarnContext(
				ctx,
				"control-plane extra header overrides existing header",
				slog.String("header", k),
			)
		}
		headers.Set(k, v)
	}
}
