package internal

import (
	"context"
	"fmt"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"github.com/openai/tunnel-client/pkg/clientinstance"
	"github.com/openai/tunnel-client/pkg/mcpserverinfo"
	tctransport "github.com/openai/tunnel-client/pkg/transport"
	"github.com/openai/tunnel-client/pkg/version"
)

const (
	headerTunnelClientName    = "X-Tunnel-Client-Name"
	headerTunnelClientVersion = "X-Tunnel-Client-Version"
	headerOpenAIOrganization  = "OpenAI-Organization"
)

type responseReceiptRecorderContextKey struct{}

type responseReceiptRecorder struct {
	now        func() time.Time
	receivedAt time.Time
	recorded   bool
}

func newResponseReceiptRecorder(now func() time.Time) *responseReceiptRecorder {
	return &responseReceiptRecorder{now: now}
}

func (r *responseReceiptRecorder) record() {
	if r == nil || r.now == nil {
		return
	}
	r.receivedAt = r.now()
	r.recorded = true
}

func (r *responseReceiptRecorder) value() (time.Time, bool) {
	if r == nil {
		return time.Time{}, false
	}
	return r.receivedAt, r.recorded
}

func contextWithResponseReceiptRecorder(ctx context.Context, recorder *responseReceiptRecorder) context.Context {
	return context.WithValue(ctx, responseReceiptRecorderContextKey{}, recorder)
}

type responseReceiptRoundTripper struct {
	base http.RoundTripper
}

func newResponseReceiptRoundTripper(base http.RoundTripper) http.RoundTripper {
	return &responseReceiptRoundTripper{base: base}
}

func (r *responseReceiptRoundTripper) RoundTrip(req *http.Request) (*http.Response, error) {
	resp, err := r.base.RoundTrip(req)
	if resp != nil {
		if recorder, ok := req.Context().Value(responseReceiptRecorderContextKey{}).(*responseReceiptRecorder); ok {
			recorder.record()
		}
	}
	return resp, err
}

type controlPlaneRoundTripper struct {
	base           http.RoundTripper
	apiKey         string
	userAgent      string
	organizationID string
	mcpServerInfo  func() (string, error)
	extraHeaders   map[string]string
	logger         *slog.Logger
}

func newControlPlaneRoundTripper(base http.RoundTripper, apiKey, userAgent, organizationID string, mcpServerInfo func() (string, error), extraHeaders map[string]string, logger *slog.Logger) http.RoundTripper {
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
		mcpServerInfo:  mcpServerInfo,
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
	req.Header.Set(version.WireProtocolHeaderName, version.WireProtocolVersion)
	req.Header.Set(clientinstance.HeaderName, clientinstance.ID())
	if c.mcpServerInfo != nil {
		mcpServerInfo, err := c.mcpServerInfo()
		if err != nil {
			return nil, fmt.Errorf("control-plane round tripper: MCP server info: %w", err)
		}
		if mcpServerInfo != "" {
			req.Header.Set(mcpserverinfo.HeaderName, mcpServerInfo)
		}
	}
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
	if strings.EqualFold(strings.TrimSpace(key), mcpserverinfo.HeaderName) {
		return true
	}
	switch http.CanonicalHeaderKey(key) {
	case "Authorization", "Accept", "User-Agent", headerTunnelClientName, headerTunnelClientVersion, version.WireProtocolHeaderName, clientinstance.HeaderName:
		return true
	default:
		return false
	}
}
