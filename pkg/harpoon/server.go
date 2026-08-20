package harpoon

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/invopop/jsonschema"
	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/openai/tunnel-client/pkg/config"
	runtimeconfig "github.com/openai/tunnel-client/pkg/runtimeconfig"
	runtimeharpoon "github.com/openai/tunnel-client/pkg/runtimeharpoon"
)

const fullInstructions = "Harpoon provides a constrained outbound HTTP client. Use list_targets to see allowlisted targets and call_target to make GET/POST/PUT requests with strict size, timeout, and redirect limits. get_oauth_target_audience is a narrow opt-in lookup for OAuth token-endpoint private_key_jwt audiences. Harpoon cannot reach arbitrary hosts or paths outside the configured allowlist."

// Server is the full-client adapter over the dependency-clean runtime core.
// It adds only the admin call receipt and OAuth audience capabilities that are
// intentionally absent from the customer runtime binaries.
type Server struct {
	core       *runtimeharpoon.Server
	registry   *Registry
	cfg        *config.HarpoonConfig
	callBuffer *CallBuffer
}

type oauthTargetAudienceRequest struct {
	Label string `json:"label" jsonschema:"minLength=1,maxLength=64,pattern=^[a-z0-9][a-z0-9_-]{0\\,63}$,description=OAuth token-endpoint target label."`
}

type oauthTargetAudienceResponse struct {
	Audience string `json:"audience" jsonschema:"format=uri,description=Exact upstream OAuth token endpoint URL to use as a private_key_jwt audience."`
}

func (oauthTargetAudienceRequest) JSONSchemaExtend(schema *jsonschema.Schema) {
	if schema == nil {
		return
	}
	schema.Title = "Get OAuth target audience"
	schema.Description = "Resolve the exact upstream URL for an allowlisted OAuth token endpoint."
}

func (oauthTargetAudienceResponse) JSONSchemaExtend(schema *jsonschema.Schema) {
	if schema == nil {
		return
	}
	schema.Title = "OAuth target audience"
	schema.Description = "Exact private_key_jwt audience for an allowlisted OAuth token endpoint."
}

var (
	oauthTargetAudienceSchema       = buildOAuthTargetAudienceInputSchema()
	oauthTargetAudienceOutputSchema = buildOAuthTargetAudienceOutputSchema()
)

// NewServer constructs the full-client adapter over the shared runtime core.
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

	server := &Server{
		registry:   registry,
		cfg:        cfg,
		callBuffer: buffer,
	}
	coreOpts := append([]runtimeharpoon.ServerOption(nil), opts...)
	coreOpts = append(coreOpts,
		runtimeharpoon.WithInstructions(fullInstructions),
		runtimeharpoon.WithToolRegistrar(server.registerOAuthAudienceTool),
		runtimeharpoon.WithCallObserver(server.recordCall),
	)
	core, err := runtimeharpoon.NewServer(runtimeConfig(cfg), registry, logger, coreOpts...)
	if err != nil {
		return nil, err
	}
	server.core = core
	return server, nil
}

func runtimeConfig(cfg *config.HarpoonConfig) *runtimeconfig.HarpoonConfig {
	if cfg == nil {
		return nil
	}
	return &runtimeconfig.HarpoonConfig{
		AllowPlaintextHTTP:   cfg.AllowPlaintextHTTP,
		MaxResponseBytes:     cfg.MaxResponseBytes,
		MaxRedirects:         cfg.MaxRedirects,
		AdditionalTransports: append([]runtimeconfig.HarpoonTransportKind(nil), cfg.AdditionalTransports...),
		Targets:              append([]runtimeconfig.HarpoonTarget(nil), cfg.Targets...),
		HostClassifier:       cfg.HostClassifier,
		HTTPProxy:            cfg.HTTPProxy,
		HTTPProxySource:      cfg.HTTPProxySource,
	}
}

// MCPServer builds the full-client MCP server.
func (s *Server) MCPServer() *mcp.Server {
	if s == nil || s.core == nil {
		return nil
	}
	return s.core.MCPServer()
}

func (s *Server) registerOAuthAudienceTool(server *mcp.Server) {
	if s == nil || server == nil {
		return
	}
	openWorldFalse := false
	mcp.AddTool(server, &mcp.Tool{
		Name:        "get_oauth_target_audience",
		Title:       "Get OAuth target audience",
		Description: "Resolve the exact private_key_jwt audience for an OAuth token endpoint target.",
		Annotations: &mcp.ToolAnnotations{
			ReadOnlyHint:   true,
			IdempotentHint: true,
			OpenWorldHint:  &openWorldFalse,
		},
		InputSchema:  oauthTargetAudienceSchema,
		OutputSchema: oauthTargetAudienceOutputSchema,
	}, s.oauthTargetAudienceHandler())
}

func (s *Server) oauthTargetAudienceHandler() mcp.ToolHandlerFor[map[string]any, any] {
	return func(_ context.Context, _ *mcp.CallToolRequest, args map[string]any) (*mcp.CallToolResult, any, error) {
		var params oauthTargetAudienceRequest
		if err := decodeArguments(args, &params); err != nil {
			return audienceToolErrorResult("", "invalid parameters"), nil, nil
		}
		resp, err := s.getOAuthTargetAudience(params)
		if err != nil {
			var toolErr *audienceToolError
			if errors.As(err, &toolErr) {
				return audienceToolErrorResult(toolErr.label, toolErr.message), nil, nil
			}
			return audienceToolErrorResult(params.Label, "failed to resolve audience"), nil, nil
		}
		structured := map[string]any{"audience": resp.Audience}
		payload, err := json.Marshal(resp)
		if err != nil {
			return audienceToolErrorResult(params.Label, "failed to encode response"), nil, nil
		}
		return &mcp.CallToolResult{Content: []mcp.Content{&mcp.TextContent{Text: string(payload)}}}, structured, nil
	}
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

func (s *Server) getOAuthTargetAudience(params oauthTargetAudienceRequest) (*oauthTargetAudienceResponse, error) {
	label := strings.TrimSpace(params.Label)
	if label == "" {
		return nil, newAudienceToolError(label, "label is required")
	}
	if s == nil || s.registry == nil {
		return nil, newAudienceToolError(label, "unknown target")
	}
	target, ok := s.registry.Lookup(label)
	if !ok {
		return nil, newAudienceToolError(label, "unknown target")
	}
	if runtimeharpoon.NormalizeToken(target.Category) != "oauth" ||
		!hasAllTags(target.Tags, []string{"auth-server-metadata", "token-endpoint"}) {
		return nil, newAudienceToolError(label, "target is not an OAuth token endpoint")
	}
	audienceURL, ok := s.registry.ExactURL(label)
	if !ok || audienceURL == nil {
		return nil, newAudienceToolError(label, "target has no URL")
	}
	audienceScheme := strings.ToLower(audienceURL.Scheme)
	if (audienceScheme != "http" && audienceScheme != "https") ||
		audienceURL.Host == "" || audienceURL.User != nil || audienceURL.Fragment != "" {
		return nil, newAudienceToolError(label, "target URL cannot be used as an OAuth audience")
	}
	return &oauthTargetAudienceResponse{Audience: audienceURL.String()}, nil
}

func hasAllTags(targetTags, required []string) bool {
	if len(required) == 0 {
		return true
	}
	available := make(map[string]struct{}, len(targetTags))
	for _, tag := range targetTags {
		normalized := runtimeharpoon.NormalizeToken(tag)
		if normalized != "" {
			available[normalized] = struct{}{}
		}
	}
	for _, tag := range required {
		if _, ok := available[runtimeharpoon.NormalizeToken(tag)]; !ok {
			return false
		}
	}
	return true
}

type audienceToolError struct {
	label   string
	message string
}

func newAudienceToolError(label, message string) *audienceToolError {
	return &audienceToolError{label: label, message: message}
}

func (e *audienceToolError) Error() string {
	if e == nil {
		return ""
	}
	label := e.label
	if label == "" {
		label = "unknown"
	}
	return fmt.Sprintf("label %s: %s", label, e.message)
}

func audienceToolErrorResult(label, message string) *mcp.CallToolResult {
	if label == "" {
		label = "unknown"
	}
	return &mcp.CallToolResult{
		IsError: true,
		Content: []mcp.Content{&mcp.TextContent{Text: fmt.Sprintf("label %s: %s", label, message)}},
	}
}

func buildOAuthTargetAudienceInputSchema() *jsonschema.Schema {
	reflector := &jsonschema.Reflector{DoNotReference: true}
	schema := reflector.Reflect(oauthTargetAudienceRequest{})
	if schema.Type == "" {
		schema.Type = "object"
	}
	return schema
}

func buildOAuthTargetAudienceOutputSchema() *jsonschema.Schema {
	reflector := &jsonschema.Reflector{DoNotReference: true}
	schema := reflector.Reflect(oauthTargetAudienceResponse{})
	if schema.Type == "" {
		schema.Type = "object"
	}
	return schema
}

func (s *Server) recordCall(event runtimeharpoon.CallEvent) {
	if s == nil || s.callBuffer == nil {
		return
	}
	entry := CallEntry{
		Timestamp:           time.Now().UTC(),
		Label:               event.Label,
		URL:                 event.URL,
		Method:              event.Method,
		Status:              event.Status,
		LatencyMS:           int(time.Since(event.StartedAt).Milliseconds()),
		ResponseContentType: event.ResponseContentType,
		ReqBytes:            event.RequestBytes,
		RespBytes:           event.ResponseBytes,
		Error:               event.Error,
	}
	if s.cfg != nil && s.cfg.CapturePayloads {
		entry.RequestBody = event.RequestBody
		if len(event.ResponseBody) > 0 {
			bodyText, bodyIsBase64 := formatResponseBody(event.ResponseBody)
			entry.ResponseBody = bodyText
			entry.BodyIsBase64 = bodyIsBase64
		}
		if len(event.ResponseBodyTransformed) > 0 {
			entry.ResponseBodyTransformed = string(event.ResponseBodyTransformed)
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
