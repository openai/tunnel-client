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
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/invopop/jsonschema"
	"github.com/modelcontextprotocol/go-sdk/mcp"

	"go.openai.org/api/tunnel-client/pkg/config"
	tclog "go.openai.org/api/tunnel-client/pkg/log"
	"go.openai.org/api/tunnel-client/pkg/transport"
	"go.openai.org/api/tunnel-client/pkg/version"
)

const (
	defaultTimeout      = 30 * time.Second
	minTimeout          = 100 * time.Millisecond
	maxTimeout          = 120 * time.Second
	maxBodyLogFieldName = "response_bytes"
	headerNamePattern   = "^[!#$%&'*+.^_`|~0-9A-Za-z-]+$"
)

var (
	allowedMethods = map[string]struct{}{
		http.MethodGet:  {},
		http.MethodPost: {},
		http.MethodPut:  {},
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
}

type callTargetRequest struct {
	Label            string            `json:"label" jsonschema:"minLength=1,maxLength=64,pattern=^[a-z0-9][a-z0-9_-]{0\\,63}$,description=Allowlisted target label"`
	Path             string            `json:"path,omitempty" jsonschema:"pattern=^[^#]*$,description=Relative path to append to the target base URL"`
	Method           string            `json:"method" jsonschema:"enum=GET,enum=POST,enum=PUT,description=HTTP method for the outbound request"`
	Headers          map[string]string `json:"headers,omitempty" jsonschema:"description=HTTP headers to include in the request"`
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

type targetInfo struct {
	Label          string   `json:"label" jsonschema:"minLength=1,maxLength=64,pattern=^[a-z0-9][a-z0-9_-]{0\\,63}$,description=Target label."`
	Description    string   `json:"description,omitempty" jsonschema:"description=Target description."`
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

// NewServer constructs a harpoon MCP server.
func NewServer(cfg *config.HarpoonConfig, registry *Registry, buffer *CallBuffer, logger *slog.Logger) (*Server, error) {
	if cfg == nil {
		return nil, errors.New("harpoon: config is required")
	}
	if registry == nil {
		return nil, errors.New("harpoon: registry is required")
	}
	if logger == nil {
		logger = slog.Default()
	}
	if buffer == nil {
		buffer = NewCallBuffer()
	}
	return &Server{
		logger:        logger.With(tclog.FieldComponent, tclog.ComponentHarpoon),
		registry:      registry,
		cfg:           cfg,
		httpTransport: transport.CloneDefault(),
		callBuffer:    buffer,
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
	return func(ctx context.Context, _ *mcp.CallToolRequest, _ map[string]any) (*mcp.CallToolResult, any, error) {
		resp := s.listTargets()
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

func (s *Server) listTargets() listTargetsResponse {
	allowed := allowedMethodsList()
	targets := s.registry.Targets()
	out := make([]targetInfo, 0, len(targets))
	for _, target := range targets {
		out = append(out, targetInfo{
			Label:          target.Label,
			Description:    target.Description,
			AllowedMethods: allowed,
		})
	}
	return listTargetsResponse{Targets: out}
}

func (s *Server) callTarget(ctx context.Context, params callTargetRequest) (*callTargetResponse, error) {
	logger := tclog.LoggerWithContextIdentifiers(ctx, s.logger)
	start := time.Now()

	label := strings.TrimSpace(params.Label)
	if label == "" {
		return nil, newToolError(label, "label is required")
	}

	if _, ok := s.registry.Lookup(label); !ok {
		return nil, newToolError(label, "unknown target")
	}

	method := strings.ToUpper(strings.TrimSpace(params.Method))
	if _, ok := allowedMethods[method]; !ok {
		return nil, newToolError(label, "invalid method")
	}

	resolved, err := s.registry.Resolve(label, params.Path)
	if err != nil {
		return nil, newToolError(label, "invalid path")
	}

	timeout, err := normalizeTimeout(params.TimeoutMS)
	if err != nil {
		return nil, newToolError(label, err.Error())
	}

	maxResponseBytes, err := s.normalizeMaxResponseBytes(params.MaxResponseBytes)
	if err != nil {
		return nil, newToolError(label, err.Error())
	}

	maxRedirects, followRedirects, err := s.normalizeRedirects(params.FollowRedirects, params.MaxRedirects)
	if err != nil {
		return nil, newToolError(label, err.Error())
	}

	bodyBytes := []byte(params.Body)
	if len(bodyBytes) > maxResponseBytes {
		return nil, newToolError(label, "request body exceeds size limit")
	}

	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	req, err := http.NewRequestWithContext(ctx, method, resolved.String(), bytes.NewReader(bodyBytes))
	if err != nil {
		return nil, newToolError(label, "request failed")
	}
	for key, value := range params.Headers {
		if strings.TrimSpace(key) == "" {
			continue
		}
		req.Header.Set(key, value)
	}

	client := &http.Client{Transport: s.httpTransport}
	client.CheckRedirect = s.redirectPolicy(label, maxRedirects, followRedirects)

	resp, err := client.Do(req)
	if err != nil {
		toolErr := asToolError(err)
		cause := classifyRequestError(err)
		logFields := []any{
			slog.String("label", label),
			slog.String("url", resolved.String()),
			slog.String("method", method),
			slog.String("error", cause),
			slog.Int("request_bytes", len(bodyBytes)),
			slog.Int("status_code", 0),
			slog.Int(maxBodyLogFieldName, 0),
		}
		if toolErr != nil && toolErr.redirectURL != "" {
			logFields = append(logFields,
				slog.String("redirect_url", toolErr.redirectURL),
				slog.String("redirect_reason", toolErr.reason),
			)
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
			slog.String("url", resp.Request.URL.String()),
			slog.String("method", method),
			slog.String("error", "response read failed"),
			slog.Int("request_bytes", len(bodyBytes)),
			slog.Int("status_code", resp.StatusCode),
			slog.Int(maxBodyLogFieldName, len(body)),
		)
		s.recordCall(callRecordInput{
			label:        label,
			url:          resp.Request.URL.String(),
			method:       method,
			status:       resp.StatusCode,
			reqBytes:     len(bodyBytes),
			respBytes:    len(body),
			errorMsg:     "response read failed",
			startedAt:    start,
			params:       params,
			responseBody: body,
		})
		return nil, newToolError(label, "response read failed")
	}
	if tooLarge {
		logger.InfoContext(ctx, "harpoon response too large",
			slog.String("label", label),
			slog.String("url", resp.Request.URL.String()),
			slog.String("method", method),
			slog.Int("request_bytes", len(bodyBytes)),
			slog.Int("status_code", resp.StatusCode),
			slog.Int(maxBodyLogFieldName, len(body)),
		)
		s.recordCall(callRecordInput{
			label:        label,
			url:          resp.Request.URL.String(),
			method:       method,
			status:       resp.StatusCode,
			reqBytes:     len(bodyBytes),
			respBytes:    len(body),
			errorMsg:     "response exceeds size limit",
			startedAt:    start,
			params:       params,
			responseBody: body,
		})
		return nil, newToolError(label, "response exceeds size limit")
	}

	logger.InfoContext(ctx, "harpoon request completed",
		slog.String("label", label),
		slog.String("url", resp.Request.URL.String()),
		slog.String("method", method),
		slog.Int("status_code", resp.StatusCode),
		slog.Int("request_bytes", len(bodyBytes)),
		slog.Int(maxBodyLogFieldName, len(body)),
	)

	s.recordCall(callRecordInput{
		label:        label,
		url:          resp.Request.URL.String(),
		method:       method,
		status:       resp.StatusCode,
		reqBytes:     len(bodyBytes),
		respBytes:    len(body),
		errorMsg:     "",
		startedAt:    start,
		params:       params,
		responseBody: body,
	})

	return &callTargetResponse{
		StatusCode: resp.StatusCode,
		Headers:    resp.Header,
		BodyBase64: base64.StdEncoding.EncodeToString(body),
		BodySize:   len(body),
		Truncated:  false,
	}, nil
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
			return newRedirectBlockedError(label, req.URL.String())
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
	return &jsonschema.Schema{
		Type:        "object",
		Title:       "List Harpoon targets",
		Description: "List available allowlisted targets.",
	}
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
	label       string
	msg         string
	redirectURL string
	reason      string
}

func newToolError(label, msg string) *toolError {
	return &toolError{label: label, msg: msg}
}

func newRedirectBlockedError(label, redirectURL string) *toolError {
	return &toolError{
		label:       label,
		msg:         "redirect blocked",
		redirectURL: redirectURL,
		reason:      "redirect target not in allow list",
	}
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
	label        string
	url          string
	method       string
	status       int
	reqBytes     int
	respBytes    int
	errorMsg     string
	startedAt    time.Time
	params       callTargetRequest
	responseBody []byte
}

func (s *Server) recordCall(input callRecordInput) {
	if s == nil || s.callBuffer == nil {
		return
	}
	entry := CallEntry{
		Timestamp: time.Now().UTC(),
		Label:     input.label,
		URL:       input.url,
		Method:    input.method,
		Status:    input.status,
		LatencyMS: int(time.Since(input.startedAt).Milliseconds()),
		ReqBytes:  input.reqBytes,
		RespBytes: input.respBytes,
		Error:     input.errorMsg,
	}
	if s.cfg != nil && s.cfg.CapturePayloads {
		entry.RequestBody = input.params.Body
		if len(input.responseBody) > 0 {
			bodyText, bodyIsBase64 := formatResponseBody(input.responseBody)
			entry.ResponseBody = bodyText
			entry.BodyIsBase64 = bodyIsBase64
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
