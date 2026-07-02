package internal

import (
	"context"
	"fmt"
	"log/slog"
	"net/http"

	"github.com/openai/tunnel-client/pkg/clientinstance"
	tctransport "github.com/openai/tunnel-client/pkg/transport"
	"github.com/openai/tunnel-client/pkg/version"
)

const (
	headerTunnelClientName    = "X-Tunnel-Client-Name"
	headerTunnelClientVersion = "X-Tunnel-Client-Version"
	headerOpenAIOrganization  = "OpenAI-Organization"
)

type controlPlaneRoundTripper struct {
	base           http.RoundTripper
	apiKey         string
	userAgent      string
	organizationID string
	extraHeaders   map[string]string
	logger         *slog.Logger
}

func newControlPlaneRoundTripper(base http.RoundTripper, apiKey, userAgent, organizationID string, extraHeaders map[string]string, logger *slog.Logger) http.RoundTripper {
	if base == nil {
		base = tctransport.CloneDefault()
	}
	if logger == nil {
		panic("control-plane round tripper: logger is required")
	}
	return &controlPlaneRoundTripper{
		base:           base,
		apiKey:         apiKey,
		userAgent:      userAgent,
		organizationID: organizationID,
		extraHeaders:   extraHeaders,
		logger:         logger,
	}
}

func (c *controlPlaneRoundTripper) RoundTrip(req *http.Request) (*http.Response, error) {
	req.Header.Set("Authorization", fmt.Sprintf("Bearer %s", c.apiKey))
	req.Header.Set("Accept", "application/json")
	req.Header.Set("User-Agent", c.userAgent)
	req.Header.Set(headerTunnelClientName, version.ClientName)
	req.Header.Set(headerTunnelClientVersion, version.Version)
	req.Header.Set(clientinstance.HeaderName, clientinstance.ID())
	c.applyExtraHeaders(req.Context(), req.Header)
	if c.organizationID != "" {
		req.Header.Set(headerOpenAIOrganization, c.organizationID)
	}

	return c.base.RoundTrip(req)
}

func (c *controlPlaneRoundTripper) applyExtraHeaders(ctx context.Context, headers http.Header) {
	if len(c.extraHeaders) == 0 {
		return
	}

	for k, v := range c.extraHeaders {
		if isProtectedControlPlaneHeader(k) {
			c.logger.WarnContext(
				ctx,
				"control-plane extra header cannot override protected header",
				slog.String("header", k),
			)
			continue
		}
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

func isProtectedControlPlaneHeader(key string) bool {
	switch http.CanonicalHeaderKey(key) {
	case "Authorization", "Accept", "User-Agent", headerTunnelClientName, headerTunnelClientVersion, clientinstance.HeaderName:
		return true
	default:
		return false
	}
}
