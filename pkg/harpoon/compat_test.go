package harpoon

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/openai/tunnel-client/pkg/config"
	"github.com/openai/tunnel-client/pkg/health"
	runtimeharpoon "github.com/openai/tunnel-client/pkg/runtimeharpoon"
)

// These package-local names exist only for the historical full-package test
// suite. Production behavior lives in runtimeharpoon; keeping the adapters in
// _test.go prevents the full binary from carrying unused duplicate surfaces.
const (
	metricNameHarpoonCallTotal       = "harpoon_call_total"
	metricNameHarpoonCallLatencyMS   = "harpoon_call_latency_milliseconds"
	metricNameHarpoonResponseSizeB   = "harpoon_response_size_bytes"
	metricOutcomeSuccess             = "success"
	metricOutcomeInvalidInput        = "invalid_input"
	defaultMetricsUnknownTargetLabel = "__unknown__"
	maxContentTypeLogBytes           = runtimeharpoon.MaxContentTypeLogBytes
)

const (
	redirectMismatchSchemeHTTPToHTTPS = runtimeharpoon.RedirectMismatchSchemeHTTPToHTTPS
	redirectMismatchSchemeHTTPSToHTTP = runtimeharpoon.RedirectMismatchSchemeHTTPSToHTTP
	redirectMismatchPath              = runtimeharpoon.RedirectMismatchPath
	redirectMismatchQuery             = runtimeharpoon.RedirectMismatchQuery
	redirectMismatchHost              = runtimeharpoon.RedirectMismatchHost
	redirectMismatchOther             = runtimeharpoon.RedirectMismatchOther
)

type callTargetRequest = runtimeharpoon.CallTargetRequest
type callTargetResponse = runtimeharpoon.CallTargetResponse
type listTargetsRequest = runtimeharpoon.ListTargetsRequest
type listTargetsResponse = runtimeharpoon.ListTargetsResponse
type urlRewriter = runtimeharpoon.URLRewriter

func (s *Server) listTargets(params listTargetsRequest) listTargetsResponse {
	if s == nil || s.core == nil {
		return listTargetsResponse{}
	}
	return s.core.ListTargets(params)
}

func (s *Server) callTarget(ctx context.Context, params callTargetRequest) (*callTargetResponse, error) {
	if s == nil || s.core == nil {
		return nil, errors.New("harpoon: server is nil")
	}
	return s.core.CallTarget(ctx, params)
}

func (s *Server) unixTransportCount() int {
	if s == nil || s.core == nil {
		return 0
	}
	return s.core.UnixTransportCount()
}

func filterOutboundHeaders(headers map[string]string) (http.Header, int, []string) {
	return runtimeharpoon.FilterOutboundHeaders(headers)
}

func isBlockedOutboundHeader(headerName string) bool {
	return runtimeharpoon.IsBlockedOutboundHeader(headerName)
}

func responseContentTypeForLog(contentType string) string {
	return runtimeharpoon.ResponseContentTypeForLog(contentType)
}

func newURLRewriter(targets []Target) *urlRewriter {
	return runtimeharpoon.NewURLRewriter(targets)
}

func transformJSONBody(body []byte, rewriter *urlRewriter) ([]byte, bool) {
	return runtimeharpoon.TransformJSONBody(body, rewriter)
}

func transformHeaders(headers http.Header, rewriter *urlRewriter) (http.Header, bool) {
	return runtimeharpoon.TransformHeaders(headers, rewriter)
}

func convertTargets(targets []config.HarpoonTarget) []Target {
	return runtimeharpoon.ConvertTargets(targets)
}

func buildHarpoonHTTPEndpoint(healthCfg *config.HealthConfig, svc health.Service, timeout time.Duration) string {
	return runtimeharpoon.BuildHarpoonHTTPEndpoint(healthCfg, svc, timeout)
}

func newRestartableInMemoryTransport(ctx context.Context, server *mcp.Server, logger *slog.Logger) mcp.Transport {
	return runtimeharpoon.NewRestartableInMemoryTransport(ctx, server, logger)
}
