package harpoon

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/invopop/jsonschema"
	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/openai/tunnel-client/pkg/config"
	tclog "github.com/openai/tunnel-client/pkg/log"
	"github.com/openai/tunnel-client/pkg/transport"
	"github.com/openai/tunnel-client/pkg/version"
)

const (
	defaultTimeout         = 30 * time.Second
	minTimeout             = 100 * time.Millisecond
	maxTimeout             = 120 * time.Second
	maxBodyLogFieldName    = "response_bytes"
	maxContentTypeLogBytes = 256
	headerNamePattern      = "^[!#$%&'*+.^_`|~0-9A-Za-z-]+$"
)

var (
	allowedMethods = map[string]struct{}{
		http.MethodGet:  {},
		http.MethodPost: {},
		http.MethodPut:  {},
	}
	blockedOutboundHeaders = map[string]struct{}{
		"accept-encoding":                   {},
		"cf-connecting-ip":                  {},
		"connection":                        {},
		"content-length":                    {},
		"cookie":                            {},
		"forwarded":                         {},
		"host":                              {},
		"keep-alive":                        {},
		"proxy-authenticate":                {},
		"proxy-authorization":               {},
		"proxy-connection":                  {},
		"te":                                {},
		"trailer":                           {},
		"transfer-encoding":                 {},
		"true-client-ip":                    {},
		"upgrade":                           {},
		"user-agent":                        {},
		"via":                               {},
		"x-client-ip":                       {},
		"x-cluster-client-ip":               {},
		"x-custom-cf-witness-actor":         {},
		"x-custom-cf-witness-authorization": {},
		"x-envoy-external-address":          {},
		"x-forwarded-for":                   {},
		"x-forwarded-host":                  {},
		"x-forwarded-port":                  {},
		"x-forwarded-proto":                 {},
		"x-openai-actor-authorization":      {},
		"x-openai-authorization":            {},
		"x-openai-authorization-error":      {},
		"x-openai-internal-caller":          {},
		"x-openai-skip-auth":                {},
		"x-original-forwarded-for":          {},
		"x-real-ip":                         {},
		"x-tunnel-traffic-source":           {},
	}
	listTargetsSchema       = buildListTargetsInputSchema()
	listTargetsOutputSchema = buildListTargetsOutputSchema()
)

// Server provides MCP tools for constrained HTTP access.
type Server struct {
	logger        *slog.Logger
	registry      *Registry
	cfg           *config.HarpoonConfig
	httpTransport http.RoundTripper
	callBuffer    *CallBuffer
	metrics       *serverMetrics
	unixMu        sync.Mutex
	unixBySocket  map[string]http.RoundTripper
}

type callTargetRequest struct {
	Label            string            `json:"label" jsonschema:"minLength=1,maxLength=64,pattern=^[a-z0-9][a-z0-9_-]{0\\,63}$,description=Allowlisted target label"`
	Method           string            `json:"method" jsonschema:"enum=GET,enum=POST,enum=PUT,description=HTTP method for the outbound request"`
	Headers          map[string]string `json:"headers,omitempty" jsonschema:"description=HTTP headers to include in the request; transport proxy forwarding and client-managed identity headers are blocked"`
	Body             string            `json:"body,omitempty" jsonschema:"description=Request body as a raw string"`
	TimeoutMS        *int              `json:"timeout_ms,omitempty" jsonschema:"description=Request timeout in milliseconds"`
	MaxResponseBytes *int              `json:"max_response_bytes,omitempty" jsonschema:"description=Maximum response bytes to read"`
	FollowRedirects  *bool             `json:"follow_redirects,omitempty" jsonschema:"description=Whether to follow HTTP redirects"`
	MaxRedirects     *int              `json:"max_redirects,omitempty" jsonschema:"description=Maximum redirects to follow when follow_redirects is true"`
}

type callTargetResponse struct {
	StatusCode int                 `json:"status_code" jsonschema:"description=HTTP status code returned by the target."`
	Headers    map[string][]string `json:"headers,omitempty" jsonschema:"description=Response headers returned by the target."`
	BodyBase64 string              `json:"body_base64,omitempty" jsonschema:"description=Base64-encoded response body bytes." jsonschema_extras:"contentEncoding=base64"`
	BodySize   int                 `json:"body_size_bytes" jsonschema:"description=Number of bytes in body_base64."`
	Truncated  bool                `json:"truncated,omitempty" jsonschema:"description=Whether the response body was truncated."`
}

type listTargetsResponse struct {
	Targets []targetInfo `json:"targets" jsonschema:"description=Allowlisted targets."`
}

type listTargetsRequest struct {
	Categories []string `json:"categories,omitempty" jsonschema:"description=Target categories to include."`
	Sources    []string `json:"sources,omitempty" jsonschema:"description=Target sources to include."`
	Tags       []string `json:"tags,omitempty" jsonschema:"description=Target tags to include (all tags must match)."`
}

type targetInfo struct {
	Label          string   `json:"label" jsonschema:"minLength=1,maxLength=64,pattern=^[a-z0-9][a-z0-9_-]{0\\,63}$,description=Target label."`
	Description    string   `json:"description,omitempty" jsonschema:"description=Target description."`
	Category       string   `json:"category,omitempty" jsonschema:"description=Target category."`
	Source         string   `json:"source,omitempty" jsonschema:"description=Target source."`
	Tags           []string `json:"tags,omitempty" jsonschema:"description=Target tags."`
	AllowedMethods []string `json:"allowed_methods" jsonschema:"description=HTTP methods permitted for this target,enum=GET,enum=POST,enum=PUT"`
}

func (callTargetRequest) JSONSchemaExtend(schema *jsonschema.Schema) {
	if schema == nil {
		return
	}
	schema.Title = "Call Harpoon target"
	schema.Description = "Call an allowlisted HTTP target by label."
	if schema.Properties == nil {
		return
	}
	if headersSchema, ok := schema.Properties.Get("headers"); ok && headersSchema != nil {
		headersSchema.Default = map[string]string{}
		if headersSchema.PropertyNames == nil {
			headersSchema.PropertyNames = &jsonschema.Schema{Type: "string"}
		}
		headersSchema.PropertyNames.Pattern = headerNamePattern
	}
	if timeoutSchema, ok := schema.Properties.Get("timeout_ms"); ok && timeoutSchema != nil {
		timeoutSchema.Minimum = jsonNumber(int(minTimeout.Milliseconds()))
		timeoutSchema.Maximum = jsonNumber(int(maxTimeout.Milliseconds()))
		timeoutSchema.Default = jsonNumber(int(defaultTimeout.Milliseconds()))
	}
	if followSchema, ok := schema.Properties.Get("follow_redirects"); ok && followSchema != nil {
		followSchema.Default = true
	}
}

func (callTargetResponse) JSONSchemaExtend(schema *jsonschema.Schema) {
	if schema == nil {
		return
	}
	schema.Title = "Harpoon call result"
	schema.Description = "Response details from the target."
	if schema.Properties == nil {
		return
	}
	if statusSchema, ok := schema.Properties.Get("status_code"); ok && statusSchema != nil {
		statusSchema.Minimum = jsonNumber(100)
		statusSchema.Maximum = jsonNumber(599)
	}
	if headersSchema, ok := schema.Properties.Get("headers"); ok && headersSchema != nil {
		if headersSchema.PropertyNames == nil {
			headersSchema.PropertyNames = &jsonschema.Schema{Type: "string"}
		}
		headersSchema.PropertyNames.Pattern = headerNamePattern
	}
	if sizeSchema, ok := schema.Properties.Get("body_size_bytes"); ok && sizeSchema != nil {
		sizeSchema.Minimum = jsonNumber(0)
	}
}

func (listTargetsResponse) JSONSchemaExtend(schema *jsonschema.Schema) {
	if schema == nil {
		return
	}
	schema.Title = "Harpoon target list"
	schema.Description = "Allowlisted targets available to call_target."
}

func (listTargetsRequest) JSONSchemaExtend(schema *jsonschema.Schema) {
	if schema == nil {
		return
	}
	schema.Title = "List Harpoon targets"
	schema.Description = "List available allowlisted targets."
}

// NewServer constructs a harpoon MCP server.
func NewServer(cfg *config.HarpoonConfig, registry *Registry, buffer *CallBuffer, logger *slog.Logger, opts ...ServerOption) (*Server, error) {
	if cfg == nil {
		return nil, errors.New("harpoon: config is required")
	}
	if registry == nil {
		return nil, errors.New("harpoon: registry is required")
	}
	if logger == nil {
		return nil, errors.New("harpoon: logger is required")
	}
	if buffer == nil {
		buffer = NewCallBuffer()
	}
	serverOpts := resolveServerOptions(opts...)
	serverMetrics, err := newServerMetrics(serverOpts.meter)
	if err != nil {
		return nil, fmt.Errorf("harpoon: init metrics: %w", err)
	}
	if serverOpts.httpTransport == nil {
		serverOpts.httpTransport = transport.CloneDefault()
	}
	return &Server{
		logger:        logger.With(tclog.FieldComponent, tclog.ComponentHarpoon),
		registry:      registry,
		cfg:           cfg,
		httpTransport: serverOpts.httpTransport,
		callBuffer:    buffer,
		metrics:       serverMetrics,
	}, nil
}

// MCPServer builds an MCP server with harpoon tools registered.
func (s *Server) MCPServer() *mcp.Server {
	serverOptions := &mcp.ServerOptions{
		Instructions: "Harpoon provides a constrained outbound HTTP client. Use list_targets to see allowlisted targets and call_target to make GET/POST/PUT requests with strict size, timeout, and redirect limits. Harpoon cannot reach arbitrary hosts or paths outside the configured allowlist.",
		Capabilities: &mcp.ServerCapabilities{
			Tools: &mcp.ToolCapabilities{ListChanged: false},
		},
	}
	server := mcp.NewServer(&mcp.Implementation{
		Name:    "harpoon",
		Title:   "Harpoon (Constrained HTTP Client)",
		Version: version.Version,
	}, serverOptions)
	openWorldFalse := false
	openWorldTrue := true
	mcp.AddTool(server, &mcp.Tool{
		Name:        "list_targets",
		Title:       "List Harpoon targets",
		Description: "List available Harpoon targets by label.",
		Annotations: &mcp.ToolAnnotations{
			ReadOnlyHint:   true,
			IdempotentHint: true,
			OpenWorldHint:  &openWorldFalse,
		},
		InputSchema:  listTargetsSchema,
		OutputSchema: listTargetsOutputSchema,
	}, s.listTargetsHandler())
	mcp.AddTool(server, &mcp.Tool{
		Name:        "call_target",
		Title:       "Call Harpoon target",
		Description: "Call an allowlisted HTTP target by label.",
		Annotations: &mcp.ToolAnnotations{
			OpenWorldHint: &openWorldTrue,
		},
		InputSchema:  buildCallTargetSchema(s.cfg),
		OutputSchema: buildCallTargetOutputSchema(s.cfg),
	}, s.callTargetHandler())
	return server
}

func (s *Server) listTargetsHandler() mcp.ToolHandlerFor[map[string]any, any] {
	return func(ctx context.Context, _ *mcp.CallToolRequest, args map[string]any) (*mcp.CallToolResult, any, error) {
		var params listTargetsRequest
		if err := decodeArguments(args, &params); err != nil {
			return toolErrorResult("", "invalid parameters"), nil, nil
		}
		resp := s.listTargets(params)
		structured := map[string]any{"targets": resp.Targets}
		payload, err := json.Marshal(resp)
		if err != nil {
			return toolErrorResult("", "failed to encode response"), nil, nil
		}
		return &mcp.CallToolResult{Content: []mcp.Content{&mcp.TextContent{Text: string(payload)}}}, structured, nil
	}
}

func (s *Server) callTargetHandler() mcp.ToolHandlerFor[map[string]any, any] {
	return func(ctx context.Context, _ *mcp.CallToolRequest, args map[string]any) (*mcp.CallToolResult, any, error) {
		var params callTargetRequest
		if err := decodeArguments(args, &params); err != nil {
			return toolErrorResult("", "invalid parameters"), nil, nil
		}
		resp, err := s.callTarget(ctx, params)
		if err != nil {
			if toolErr := asToolError(err); toolErr != nil {
				return toolErrorResult(toolErr.label, toolErr.msg), nil, nil
			}
			return toolErrorResult(params.Label, "request failed"), nil, nil
		}
		structured := map[string]any{
			"status_code":     resp.StatusCode,
			"headers":         resp.Headers,
			"body_base64":     resp.BodyBase64,
			"body_size_bytes": resp.BodySize,
			"truncated":       resp.Truncated,
		}
		payload, err := json.Marshal(resp)
		if err != nil {
			return toolErrorResult(params.Label, "failed to encode response"), nil, nil
		}
		return &mcp.CallToolResult{Content: []mcp.Content{&mcp.TextContent{Text: string(payload)}}}, structured, nil
	}
}

func (s *Server) listTargets(params listTargetsRequest) listTargetsResponse {
	allowed := allowedMethodsList()
	targets := s.registry.Targets()
	filters := normalizeListTargetsFilters(params)
	out := make([]targetInfo, 0, len(targets))
	for _, target := range targets {
		if !filters.matches(target) {
			continue
		}
		out = append(out, targetInfo{
			Label:          target.Label,
			Description:    target.Description,
			Category:       target.Category,
			Source:         target.Source,
			Tags:           target.Tags,
			AllowedMethods: allowed,
		})
	}
	return listTargetsResponse{Targets: out}
}

func (s *Server) callTarget(ctx context.Context, params callTargetRequest) (*callTargetResponse, error) {
	logger := tclog.LoggerWithContextIdentifiers(ctx, s.logger)
	start := time.Now()

	label := strings.TrimSpace(params.Label)
	metricsLabel := defaultMetricsUnknownTargetLabel
	recordMetrics := func(statusCode int, outcome string, responseBytes int) {
		s.recordCallMetrics(ctx, metricsLabel, statusCode, outcome, responseBytes, start)
	}
	if label == "" {
		recordMetrics(0, metricOutcomeInvalidInput, 0)
		return nil, newToolError(label, "label is required")
	}

	if _, ok := s.registry.Lookup(label); !ok {
		recordMetrics(0, metricOutcomeInvalidInput, 0)
		return nil, newToolError(label, "unknown target")
	}
	metricsLabel = label

	method := strings.ToUpper(strings.TrimSpace(params.Method))
	if _, ok := allowedMethods[method]; !ok {
		recordMetrics(0, metricOutcomeInvalidInput, 0)
		return nil, newToolError(label, "invalid method")
	}

	resolved, err := s.registry.Resolve(label)
	if err != nil {
		recordMetrics(0, metricOutcomeInvalidInput, 0)
		return nil, newToolError(label, "unknown target")
	}

	timeout, err := normalizeTimeout(params.TimeoutMS)
	if err != nil {
		recordMetrics(0, metricOutcomeInvalidInput, 0)
		return nil, newToolError(label, err.Error())
	}

	maxResponseBytes, err := s.normalizeMaxResponseBytes(params.MaxResponseBytes)
	if err != nil {
		recordMetrics(0, metricOutcomeInvalidInput, 0)
		return nil, newToolError(label, err.Error())
	}

	maxRedirects, followRedirects, err := s.normalizeRedirects(params.FollowRedirects, params.MaxRedirects)
	if err != nil {
		recordMetrics(0, metricOutcomeInvalidInput, 0)
		return nil, newToolError(label, err.Error())
	}

	bodyBytes := []byte(params.Body)
	if len(bodyBytes) > maxResponseBytes {
		recordMetrics(0, metricOutcomeInvalidInput, 0)
		return nil, newToolError(label, "request body exceeds size limit")
	}

	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	req, err := http.NewRequestWithContext(ctx, method, resolved.String(), bytes.NewReader(bodyBytes))
	if err != nil {
		recordMetrics(0, metricOutcomeRequestError, 0)
		return nil, newToolError(label, "request failed")
	}
	filteredHeaders, droppedHeaderCount, droppedHeaderClassifications := filterOutboundHeaders(params.Headers)
	for key, values := range filteredHeaders {
		for _, value := range values {
			req.Header.Add(key, value)
		}
	}
	req.Header.Set("User-Agent", version.UserAgent)
	if droppedHeaderCount > 0 {
		logger.InfoContext(ctx, "harpoon request dropped non-forwardable headers",
			slog.String("label", label),
			slog.String("target_label", label),
			slog.Int("dropped_header_count", droppedHeaderCount),
			slog.Any("dropped_header_classifications", droppedHeaderClassifications),
		)
	}

	client := &http.Client{Transport: targetRoundTripper{server: s}}
	client.CheckRedirect = s.redirectPolicy(label, maxRedirects, followRedirects)

	resp, err := client.Do(req)
	if err != nil {
		toolErr := asToolError(err)
		cause := classifyRequestError(err)
		logFields := []any{
			slog.String("label", label),
			slog.String("target_label", label),
			slog.String("url", resolved.String()),
			slog.String("method", method),
			slog.String("error", cause),
			slog.Int("latency_ms", int(time.Since(start).Milliseconds())),
			slog.Int("request_bytes", len(bodyBytes)),
			slog.Int("status_code", 0),
			slog.Int(maxBodyLogFieldName, 0),
		}
		if toolErr != nil && toolErr.redirectURL != "" {
			logFields = append(logFields,
				slog.String("redirect_url", toolErr.redirectURL),
				slog.String("redirect_reason", toolErr.reason),
			)
			if toolErr.redirectMismatchKind != "" {
				logFields = append(logFields, slog.String("redirect_mismatch_kind", string(toolErr.redirectMismatchKind)))
			}
			if toolErr.redirectExpectedURL != "" {
				logFields = append(logFields, slog.String("redirect_expected_url", toolErr.redirectExpectedURL))
			}
			if toolErr.redirectExpectedScheme != "" {
				logFields = append(logFields, slog.String("redirect_expected_scheme", toolErr.redirectExpectedScheme))
			}
			if toolErr.redirectActualScheme != "" {
				logFields = append(logFields, slog.String("redirect_actual_scheme", toolErr.redirectActualScheme))
			}
		}
		logger.InfoContext(ctx, "harpoon request failed",
			logFields...,
		)
		responseMsg := "request failed"
		if toolErr != nil {
			responseMsg = toolErr.msg
		}
		s.recordCall(callRecordInput{
			label:     label,
			url:       resolved.String(),
			method:    method,
			status:    0,
			reqBytes:  len(bodyBytes),
			respBytes: 0,
			errorMsg:  cause,
			startedAt: start,
			params:    params,
		})
		recordMetrics(0, metricOutcomeRequestError, 0)
		return nil, newToolError(label, responseMsg)
	}
	defer func() {
		if err := resp.Body.Close(); err != nil {
			logger.DebugContext(ctx, "harpoon response close failed", slog.String("error", err.Error()))
		}
	}()

	body, tooLarge, readErr := readLimited(resp.Body, maxResponseBytes)
	if readErr != nil {
		logger.InfoContext(ctx, "harpoon response read failed",
			slog.String("label", label),
			slog.String("target_label", label),
			slog.String("url", resp.Request.URL.String()),
			slog.String("method", method),
			slog.String("error", "response read failed"),
			slog.Int("latency_ms", int(time.Since(start).Milliseconds())),
			slog.Int("request_bytes", len(bodyBytes)),
			slog.Int("status_code", resp.StatusCode),
			slog.String("response_content_type", responseContentTypeForLog(resp.Header.Get("Content-Type"))),
			slog.Int(maxBodyLogFieldName, len(body)),
		)
		s.recordCall(callRecordInput{
			label:               label,
			url:                 resp.Request.URL.String(),
			method:              method,
			status:              resp.StatusCode,
			reqBytes:            len(bodyBytes),
			respBytes:           len(body),
			errorMsg:            "response read failed",
			startedAt:           start,
			params:              params,
			responseBody:        body,
			responseContentType: responseContentTypeForLog(resp.Header.Get("Content-Type")),
		})
		recordMetrics(resp.StatusCode, metricOutcomeResponseReadError, len(body))
		return nil, newToolError(label, "response read failed")
	}
	if tooLarge {
		logger.InfoContext(ctx, "harpoon response too large",
			slog.String("label", label),
			slog.String("target_label", label),
			slog.String("url", resp.Request.URL.String()),
			slog.String("method", method),
			slog.Int("latency_ms", int(time.Since(start).Milliseconds())),
			slog.Int("request_bytes", len(bodyBytes)),
			slog.Int("status_code", resp.StatusCode),
			slog.String("response_content_type", responseContentTypeForLog(resp.Header.Get("Content-Type"))),
			slog.Int(maxBodyLogFieldName, len(body)),
		)
		s.recordCall(callRecordInput{
			label:               label,
			url:                 resp.Request.URL.String(),
			method:              method,
			status:              resp.StatusCode,
			reqBytes:            len(bodyBytes),
			respBytes:           len(body),
			errorMsg:            "response exceeds size limit",
			startedAt:           start,
			params:              params,
			responseBody:        body,
			responseContentType: responseContentTypeForLog(resp.Header.Get("Content-Type")),
		})
		recordMetrics(resp.StatusCode, metricOutcomeResponseTooLarge, len(body))
		return nil, newToolError(label, "response exceeds size limit")
	}

	rewriter := newURLRewriter(s.registry.Targets())
	transformedHeaders, _ := transformHeaders(resp.Header, rewriter)
	transformedBody, bodyTransformed := transformJSONBody(body, rewriter)
	if !bodyTransformed {
		transformedBody = body
	}

	logger.InfoContext(ctx, "harpoon request completed",
		slog.String("label", label),
		slog.String("target_label", label),
		slog.String("url", resp.Request.URL.String()),
		slog.String("method", method),
		slog.Int("latency_ms", int(time.Since(start).Milliseconds())),
		slog.Int("status_code", resp.StatusCode),
		slog.String("response_content_type", responseContentTypeForLog(resp.Header.Get("Content-Type"))),
		slog.Int("request_bytes", len(bodyBytes)),
		slog.Int(maxBodyLogFieldName, len(body)),
	)

	s.recordCall(callRecordInput{
		label:               label,
		url:                 resp.Request.URL.String(),
		method:              method,
		status:              resp.StatusCode,
		reqBytes:            len(bodyBytes),
		respBytes:           len(body),
		errorMsg:            "",
		startedAt:           start,
		params:              params,
		responseBody:        body,
		responseContentType: responseContentTypeForLog(resp.Header.Get("Content-Type")),
		responseBodyTransformed: func() []byte {
			if bodyTransformed {
				return transformedBody
			}
			return nil
		}(),
	})
	recordMetrics(resp.StatusCode, metricOutcomeSuccess, len(body))

	return &callTargetResponse{
		StatusCode: resp.StatusCode,
		Headers:    transformedHeaders,
		BodyBase64: base64.StdEncoding.EncodeToString(transformedBody),
		BodySize:   len(body),
		Truncated:  false,
	}, nil
}

func filterOutboundHeaders(headers map[string]string) (http.Header, int, []string) {
	if len(headers) == 0 {
		return http.Header{}, 0, nil
	}
	out := make(http.Header)
	dropped := 0
	classifications := make(map[string]struct{})
	for key, value := range headers {
		trimmedKey := strings.TrimSpace(key)
		if trimmedKey == "" {
			continue
		}
		canonical := http.CanonicalHeaderKey(trimmedKey)
		if isBlockedOutboundHeader(canonical) {
			dropped++
			classifications[classifyDroppedHeaderName(canonical)] = struct{}{}
			continue
		}
		out.Set(canonical, value)
	}
	return out, dropped, sortedKeys(classifications)
}

func isBlockedOutboundHeader(headerName string) bool {
	normalized := strings.ToLower(strings.TrimSpace(headerName))
	if normalized == "" {
		return true
	}
	_, ok := blockedOutboundHeaders[normalized]
	return ok
}

func classifyDroppedHeaderName(headerName string) string {
	normalized := strings.ToLower(strings.TrimSpace(headerName))
	if normalized == "" {
		return "empty"
	}
	if isSensitiveHeaderName(normalized) {
		return "sensitive-name"
	}
	if normalized == "user-agent" {
		return "user-agent"
	}
	if strings.HasPrefix(normalized, "x-") {
		return "custom"
	}
	return "not-forwardable"
}

func isSensitiveHeaderName(headerName string) bool {
	normalized := strings.NewReplacer("-", "_", ".", "_").Replace(strings.ToLower(headerName))
	for _, token := range strings.Split(normalized, "_") {
		switch token {
		case "authorization", "cookie", "key", "secret", "token", "password":
			return true
		}
	}
	return false
}

func sortedKeys(values map[string]struct{}) []string {
	if len(values) == 0 {
		return nil
	}
	out := make([]string, 0, len(values))
	for value := range values {
		out = append(out, value)
	}
	sort.Strings(out)
	return out
}

func responseContentTypeForLog(contentType string) string {
	contentType = strings.TrimSpace(contentType)
	if contentType == "" {
		return ""
	}
	mediaType, _, found := strings.Cut(contentType, ";")
	if found {
		contentType = mediaType
	}
	return truncateUTF8String(strings.ToLower(strings.TrimSpace(contentType)), maxContentTypeLogBytes)
}

func truncateUTF8String(value string, maxBytes int) string {
	if maxBytes <= 0 || len(value) <= maxBytes {
		return value
	}
	truncated := value[:maxBytes]
	for !utf8.ValidString(truncated) {
		_, size := utf8.DecodeLastRuneInString(truncated)
		if size <= 0 || size > len(truncated) {
			return ""
		}
		truncated = truncated[:len(truncated)-size]
	}
	return truncated
}

func allowedMethodsList() []string {
	return []string{http.MethodGet, http.MethodPost, http.MethodPut}
}

func normalizeTimeout(timeoutMS *int) (time.Duration, error) {
	if timeoutMS == nil {
		return defaultTimeout, nil
	}
	if *timeoutMS <= 0 {
		return 0, errors.New("timeout must be positive")
	}
	timeout := time.Duration(*timeoutMS) * time.Millisecond
	if timeout < minTimeout {
		return 0, fmt.Errorf("timeout must be at least %dms", minTimeout.Milliseconds())
	}
	if timeout > maxTimeout {
		return 0, fmt.Errorf("timeout must be at most %dms", maxTimeout.Milliseconds())
	}
	return timeout, nil
}

func (s *Server) normalizeMaxResponseBytes(value *int) (int, error) {
	limit := s.cfg.MaxResponseBytes
	if limit <= 0 {
		limit = config.DefaultHarpoonMaxResponseBytes
	}
	if value == nil {
		return limit, nil
	}
	if *value <= 0 {
		return 0, errors.New("max_response_bytes must be positive")
	}
	if *value > limit {
		return 0, fmt.Errorf("max_response_bytes must be less than or equal to %d", limit)
	}
	return *value, nil
}

func (s *Server) normalizeRedirects(followRedirects *bool, maxRedirects *int) (int, bool, error) {
	follow := true
	if followRedirects != nil {
		follow = *followRedirects
	}
	if !follow {
		return 0, false, nil
	}
	limit := s.cfg.MaxRedirects
	if limit <= 0 {
		limit = config.DefaultHarpoonMaxRedirects
	}
	if maxRedirects == nil {
		return limit, true, nil
	}
	if *maxRedirects < 0 {
		return 0, false, errors.New("max_redirects must be non-negative")
	}
	if *maxRedirects > limit {
		return 0, false, fmt.Errorf("max_redirects must be less than or equal to %d", limit)
	}
	return *maxRedirects, true, nil
}

type targetRoundTripper struct {
	server *Server
}

func (t targetRoundTripper) RoundTrip(req *http.Request) (*http.Response, error) {
	httpTransport, err := t.server.transportForURL(req.URL)
	if err != nil {
		return nil, err
	}
	return httpTransport.RoundTrip(req)
}

func (s *Server) transportForURL(targetURL *url.URL) (http.RoundTripper, error) {
	target, ok := s.registry.TargetForURL(targetURL)
	if !ok {
		return nil, errors.New("harpoon: request url is not registered")
	}
	if target.UnixSocketPath == "" {
		return s.httpTransport, nil
	}

	s.unixMu.Lock()
	defer s.unixMu.Unlock()
	if s.unixBySocket == nil {
		s.unixBySocket = make(map[string]http.RoundTripper)
	}
	if httpTransport, ok := s.unixBySocket[target.UnixSocketPath]; ok {
		return httpTransport, nil
	}
	httpTransport, err := transport.ApplyUnixSocketPath(s.httpTransport, target.UnixSocketPath)
	if err != nil {
		return nil, err
	}
	s.unixBySocket[target.UnixSocketPath] = httpTransport
	return httpTransport, nil
}

func (s *Server) redirectPolicy(label string, maxRedirects int, followRedirects bool) func(*http.Request, []*http.Request) error {
	return func(req *http.Request, via []*http.Request) error {
		if !followRedirects {
			return http.ErrUseLastResponse
		}
		if len(via) > maxRedirects {
			return newToolError(label, "redirect limit exceeded")
		}
		if req == nil || req.URL == nil {
			return newToolError(label, "redirect blocked")
		}
		if !s.registry.AllowsURL(req.URL) {
			return newRedirectBlockedError(label, req.URL.String(), s.registry.ExplainBlockedRedirect(req.URL))
		}
		return nil
	}
}

func readLimited(reader io.Reader, limit int) ([]byte, bool, error) {
	if limit <= 0 {
		return nil, false, errors.New("limit must be positive")
	}
	limited := io.LimitReader(reader, int64(limit)+1)
	data, err := io.ReadAll(limited)
	if err != nil {
		return data, false, err
	}
	if len(data) > limit {
		return data[:limit], true, nil
	}
	return data, false, nil
}

func decodeArguments(args map[string]any, out any) error {
	if out == nil {
		return errors.New("output is nil")
	}
	if args == nil {
		return nil
	}
	payload, err := json.Marshal(args)
	if err != nil {
		return err
	}
	return json.Unmarshal(payload, out)
}

func buildCallTargetSchema(cfg *config.HarpoonConfig) *jsonschema.Schema {
	reflector := &jsonschema.Reflector{DoNotReference: true}
	schema := reflector.Reflect(callTargetRequest{})
	if schema.Type == "" {
		schema.Type = "object"
	}
	applyCallTargetSchemaBounds(schema, cfg)
	return schema
}

func buildCallTargetOutputSchema(cfg *config.HarpoonConfig) *jsonschema.Schema {
	reflector := &jsonschema.Reflector{DoNotReference: true}
	schema := reflector.Reflect(callTargetResponse{})
	if schema.Type == "" {
		schema.Type = "object"
	}
	applyCallTargetOutputSchemaBounds(schema, cfg)
	return schema
}

func buildListTargetsOutputSchema() *jsonschema.Schema {
	reflector := &jsonschema.Reflector{DoNotReference: true}
	schema := reflector.Reflect(listTargetsResponse{})
	if schema.Type == "" {
		schema.Type = "object"
	}
	return schema
}

func buildListTargetsInputSchema() *jsonschema.Schema {
	reflector := &jsonschema.Reflector{DoNotReference: true}
	schema := reflector.Reflect(listTargetsRequest{})
	if schema.Type == "" {
		schema.Type = "object"
	}
	return schema
}

type listTargetsFilters struct {
	categories map[string]struct{}
	sources    map[string]struct{}
	tags       []string
}

func normalizeListTargetsFilters(params listTargetsRequest) listTargetsFilters {
	categories := normalizeFilterValues(params.Categories)
	sources := normalizeFilterValues(params.Sources)
	tags := normalizeFilterValues(params.Tags)
	return listTargetsFilters{
		categories: toStringSet(categories),
		sources:    toStringSet(sources),
		tags:       tags,
	}
}

func normalizeFilterValues(values []string) []string {
	if len(values) == 0 {
		return nil
	}
	out := make([]string, 0, len(values))
	for _, value := range values {
		normalized := normalizeToken(value)
		if normalized == "" {
			continue
		}
		out = append(out, normalized)
	}
	return out
}

func toStringSet(values []string) map[string]struct{} {
	if len(values) == 0 {
		return nil
	}
	out := make(map[string]struct{}, len(values))
	for _, value := range values {
		out[value] = struct{}{}
	}
	return out
}

func (f listTargetsFilters) matches(target Target) bool {
	if len(f.categories) == 0 && len(f.sources) == 0 && len(f.tags) == 0 {
		return true
	}
	if len(f.categories) > 0 {
		if _, ok := f.categories[target.Category]; !ok {
			return false
		}
	}
	if len(f.sources) > 0 {
		if _, ok := f.sources[target.Source]; !ok {
			return false
		}
	}
	if len(f.tags) > 0 && !hasAllTags(target.Tags, f.tags) {
		return false
	}
	return true
}

func hasAllTags(targetTags, required []string) bool {
	if len(required) == 0 {
		return true
	}
	if len(targetTags) == 0 {
		return false
	}
	tagSet := make(map[string]struct{}, len(targetTags))
	for _, tag := range targetTags {
		tagSet[normalizeToken(tag)] = struct{}{}
	}
	for _, requiredTag := range required {
		if _, ok := tagSet[requiredTag]; !ok {
			return false
		}
	}
	return true
}

func applyCallTargetSchemaBounds(schema *jsonschema.Schema, cfg *config.HarpoonConfig) {
	if schema == nil || schema.Properties == nil {
		return
	}
	maxResponseBytes := config.DefaultHarpoonMaxResponseBytes
	if cfg != nil && cfg.MaxResponseBytes > 0 {
		maxResponseBytes = cfg.MaxResponseBytes
	}
	maxRedirects := config.DefaultHarpoonMaxRedirects
	if cfg != nil && cfg.MaxRedirects > 0 {
		maxRedirects = cfg.MaxRedirects
	}
	if maxResponseSchema, ok := schema.Properties.Get("max_response_bytes"); ok && maxResponseSchema != nil {
		maxResponseSchema.Minimum = jsonNumber(1)
		maxResponseSchema.Maximum = jsonNumber(maxResponseBytes)
		maxResponseSchema.Default = jsonNumber(maxResponseBytes)
	}
	if maxRedirectsSchema, ok := schema.Properties.Get("max_redirects"); ok && maxRedirectsSchema != nil {
		maxRedirectsSchema.Minimum = jsonNumber(0)
		maxRedirectsSchema.Maximum = jsonNumber(maxRedirects)
		maxRedirectsSchema.Default = jsonNumber(maxRedirects)
	}
}

func applyCallTargetOutputSchemaBounds(schema *jsonschema.Schema, cfg *config.HarpoonConfig) {
	if schema == nil || schema.Properties == nil {
		return
	}
	maxResponseBytes := config.DefaultHarpoonMaxResponseBytes
	if cfg != nil && cfg.MaxResponseBytes > 0 {
		maxResponseBytes = cfg.MaxResponseBytes
	}
	if sizeSchema, ok := schema.Properties.Get("body_size_bytes"); ok && sizeSchema != nil {
		sizeSchema.Maximum = jsonNumber(maxResponseBytes)
	}
}

type toolError struct {
	label                  string
	msg                    string
	redirectURL            string
	reason                 string
	redirectMismatchKind   redirectMismatchKind
	redirectExpectedURL    string
	redirectExpectedScheme string
	redirectActualScheme   string
}

func newToolError(label, msg string) *toolError {
	return &toolError{label: label, msg: msg}
}

func newRedirectBlockedError(label, redirectURL string, details *redirectMismatchDetails) *toolError {
	err := &toolError{
		label:       label,
		msg:         "redirect blocked",
		redirectURL: redirectURL,
		reason:      "redirect target not in allow list",
	}
	if details == nil {
		return err
	}
	err.redirectMismatchKind = details.Kind
	err.redirectExpectedURL = details.ExpectedURL
	err.redirectExpectedScheme = details.ExpectedScheme
	err.redirectActualScheme = details.ActualScheme
	if details.Reason != "" {
		err.reason = details.Reason
	}
	if details.Kind == redirectMismatchSchemeHTTPToHTTPS || details.Kind == redirectMismatchSchemeHTTPSToHTTP {
		err.msg = fmt.Sprintf("redirect blocked: scheme mismatch (allowlisted %s, redirected to %s)", details.ExpectedScheme, details.ActualScheme)
	}
	return err
}

func (e *toolError) Error() string {
	label := e.label
	if label == "" {
		label = "unknown"
	}
	return fmt.Sprintf("label %s: %s", label, e.msg)
}

func asToolError(err error) *toolError {
	var te *toolError
	if errors.As(err, &te) {
		return te
	}
	return nil
}

func toolErrorResult(label, msg string) *mcp.CallToolResult {
	if label == "" {
		label = "unknown"
	}
	return &mcp.CallToolResult{
		IsError: true,
		Content: []mcp.Content{&mcp.TextContent{Text: fmt.Sprintf("label %s: %s", label, msg)}},
	}
}

func classifyRequestError(err error) string {
	if err == nil {
		return "request failed"
	}
	var te *toolError
	if errors.As(err, &te) {
		if te.redirectMismatchKind == redirectMismatchSchemeHTTPToHTTPS || te.redirectMismatchKind == redirectMismatchSchemeHTTPSToHTTP {
			return te.msg
		}
		if te.redirectURL != "" {
			return fmt.Sprintf("%s: %s not in allow list", te.msg, te.redirectURL)
		}
		return te.msg
	}
	if errors.Is(err, context.DeadlineExceeded) {
		return "timeout"
	}
	if strings.Contains(err.Error(), "redirect") {
		return "redirect blocked"
	}
	return "request failed"
}

func jsonNumber(value int) json.Number {
	return json.Number(strconv.Itoa(value))
}

type callRecordInput struct {
	label                   string
	url                     string
	method                  string
	status                  int
	reqBytes                int
	respBytes               int
	errorMsg                string
	startedAt               time.Time
	params                  callTargetRequest
	responseBody            []byte
	responseContentType     string
	responseBodyTransformed []byte
}

func (s *Server) recordCall(input callRecordInput) {
	if s == nil || s.callBuffer == nil {
		return
	}
	entry := CallEntry{
		Timestamp:           time.Now().UTC(),
		Label:               input.label,
		URL:                 input.url,
		Method:              input.method,
		Status:              input.status,
		LatencyMS:           int(time.Since(input.startedAt).Milliseconds()),
		ResponseContentType: input.responseContentType,
		ReqBytes:            input.reqBytes,
		RespBytes:           input.respBytes,
		Error:               input.errorMsg,
	}
	if s.cfg != nil && s.cfg.CapturePayloads {
		entry.RequestBody = input.params.Body
		if len(input.responseBody) > 0 {
			bodyText, bodyIsBase64 := formatResponseBody(input.responseBody)
			entry.ResponseBody = bodyText
			entry.BodyIsBase64 = bodyIsBase64
		}
		if len(input.responseBodyTransformed) > 0 {
			entry.ResponseBodyTransformed = string(input.responseBodyTransformed)
		}
	}
	s.callBuffer.RecordCall(entry)
}

func formatResponseBody(body []byte) (string, bool) {
	if len(body) == 0 {
		return "", false
	}
	if utf8.Valid(body) {
		return string(body), false
	}
	return base64.StdEncoding.EncodeToString(body), true
}
