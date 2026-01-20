package internal

import (
	"context"
	"fmt"
	"log/slog"
	"net/http"

	tctransport "go.openai.org/api/tunnel-client/pkg/transport"
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
	c.applyExtraHeaders(req.Context(), req.Header)

	return c.base.RoundTrip(req)
}

func (c *controlPlaneRoundTripper) applyExtraHeaders(ctx context.Context, headers http.Header) {
	if len(c.extraHeaders) == 0 {
		return
	}

	logger := c.logger
	if logger == nil {
		logger = slog.Default()
	}

	for k, v := range c.extraHeaders {
		if existing := headers.Get(k); existing != "" && existing != v {
			logger.WarnContext(
				ctx,
				"control-plane extra header overrides existing header",
				slog.String("header", k),
			)
		}
		headers.Set(k, v)
	}
}
