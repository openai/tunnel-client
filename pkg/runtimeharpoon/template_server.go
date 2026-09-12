package runtimeharpoon

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"maps"
	"net/http"
	"regexp"
	"sort"
	"strings"
	"time"

	"github.com/invopop/jsonschema"
	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/openai/tunnel-client/pkg/version"
)

// A separate tool name makes unsupported executors reject template calls rather
// than interpreting them as legacy calls with ignored arguments.
const callTargetTemplateTool = "call_target_template"

type callTargetTemplateRequest struct {
	Label            string            `json:"label" jsonschema:"minLength=1,maxLength=64,pattern=^[a-z0-9][a-z0-9_-]{0\\,63}$"`
	Parameters       map[string]any    `json:"parameters" jsonschema:"minProperties=1,maxProperties=16,description=Required string values matching the target parameters_schema."`
	Headers          map[string]string `json:"headers,omitempty"`
	TimeoutMS        *int              `json:"timeout_ms,omitempty"`
	MaxResponseBytes *int              `json:"max_response_bytes,omitempty"`
}

// targetInvocation contains only public calling information. The input schema
// binds the label and parameter constraints to this target without revealing
// how the client maps them onto a private destination.
type targetInvocation struct {
	ToolName    string           `json:"tool_name" jsonschema:"description=MCP tool to call with arguments matching input_schema."`
	InputSchema map[string]any   `json:"input_schema" jsonschema:"description=Complete argument schema for this target, including optional call controls."`
	Examples    []map[string]any `json:"examples,omitempty" jsonschema:"description=Complete validated example argument objects; omit optional call controls to use their defaults."`
}

func (callTargetTemplateRequest) JSONSchemaExtend(schema *jsonschema.Schema) {
	(callTargetRequest{}).JSONSchemaExtend(schema)
	schema.Title = "Call Harpoon target template"
	schema.Description = "Call a configured GET operation with bounded string identifiers."
}

func (s *Server) addTemplateTool(server *mcp.Server) {
	for _, target := range s.registry.Targets() {
		if target.template == nil {
			continue
		}
		schema := s.templateCallInputSchema()
		server.AddTool(&mcp.Tool{
			Name: callTargetTemplateTool, Title: "Call Harpoon target template",
			Description: "Call a version-1 GET target template using only its declared string parameters and permitted headers. Discover the tool name, complete input schema, and available examples in each list_targets entry's invocation. The target fixes the destination and disables redirects.",
			InputSchema: schema, OutputSchema: buildCallTargetOutputSchema(s.cfg),
			Annotations: &mcp.ToolAnnotations{ReadOnlyHint: true, IdempotentHint: true},
		}, s.callTemplateHandler)
		return
	}
}

func (s *Server) templateCallInputSchema() *jsonschema.Schema {
	reflector := &jsonschema.Reflector{DoNotReference: true}
	schema := reflector.Reflect(callTargetTemplateRequest{})
	applyCallTargetSchemaBounds(schema, s.cfg)
	return schema
}

func (s *Server) templateInvocation(target Target) *targetInvocation {
	base := s.templateCallInputSchema()
	properties := make(map[string]any, base.Properties.Len())
	for pair := base.Properties.Oldest(); pair != nil; pair = pair.Next() {
		properties[pair.Key] = pair.Value
	}
	label, _ := base.Properties.Get("label")
	label.Const = target.Label
	properties["parameters"] = templateParametersSchema(target.template)
	// Advertise the canonical spellings. Runtime header matching remains
	// case-insensitive; pinned header names and values stay private.
	headers := make(map[string]any, len(target.template.allowedHeaders))
	for name := range target.template.allowedHeaders {
		headers[name] = map[string]any{
			"type": "string", "maxLength": maxTemplateHeaderBytes,
			"not": map[string]any{"pattern": "[\x00-\x1f\x7f]"},
		}
	}
	properties["headers"] = map[string]any{
		"type": "object", "properties": headers, "additionalProperties": false,
		"maxProperties": maxTemplateHeaders, "default": map[string]string{},
		"description": "Optional caller headers using the advertised spelling. Values must be valid UTF-8 without control characters. The client also enforces an 8192-byte total header budget including managed headers.",
	}
	invocation := &targetInvocation{
		ToolName: callTargetTemplateTool,
		InputSchema: map[string]any{
			"$schema": base.Version, "type": "object", "properties": properties,
			"required": base.Required, "additionalProperties": false,
			"description": "Arguments for this fixed-origin HTTPS GET operation. Redirects and request bodies are disabled. Supply raw parameter values without URL encoding; the client renders them. Optional call controls use the advertised defaults.",
		},
	}
	values := make(map[string]any, len(target.template.parameters))
	for name, parameter := range target.template.PublicParameters() {
		switch {
		case len(parameter.Examples) > 0:
			values[name] = parameter.Examples[0]
		case len(parameter.Enum) > 0:
			// Enum order is not part of the policy digest. Keep the derived
			// example stable across equivalent configurations as well.
			sort.Strings(parameter.Enum)
			values[name] = parameter.Enum[0]
		default:
			return invocation
		}
	}
	// Revalidate the complete public example with the same renderer as calls.
	if _, err := target.template.Render(values); err == nil {
		invocation.Examples = []map[string]any{{"label": target.Label, "parameters": values}}
	}
	return invocation
}

func (s *Server) callTemplateHandler(ctx context.Context, req *mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	var params callTargetTemplateRequest
	if req == nil || req.Params == nil || decodeTemplateArguments(req.Params.Arguments, &params) != nil {
		return toolErrorResult("", "invalid template arguments"), nil
	}
	response, err := s.callTargetTemplate(ctx, params)
	if err != nil {
		if toolErr := asToolError(err); toolErr != nil {
			return toolErrorResult(toolErr.label, toolErr.msg), nil
		}
		return toolErrorResult("", "template request failed"), nil
	}
	payload, err := json.Marshal(response)
	if err != nil {
		return toolErrorResult(params.Label, "failed to encode response"), nil
	}
	return &mcp.CallToolResult{
		Content:           []mcp.Content{&mcp.TextContent{Text: string(payload)}},
		StructuredContent: response,
	}, nil
}

// Decode the original argument bytes before converting objects to maps, which
// would erase duplicate keys. Bound work independently of the MCP transport.
func decodeTemplateArguments(raw json.RawMessage, out *callTargetTemplateRequest) error {
	if len(raw) > 32768 {
		return errors.New("invalid template arguments")
	}
	raw = bytes.TrimSpace(raw)
	if len(raw) == 0 || raw[0] != '{' {
		return errors.New("invalid template arguments")
	}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.UseNumber()
	if err := checkTemplateJSONValue(decoder, 0); err != nil {
		return err
	}
	if _, err := decoder.Token(); err != io.EOF {
		return errors.New("invalid template arguments")
	}
	// encoding/json accepts case-insensitive struct field names. The tool's
	// published contract accepts only these exact keys, including their case.
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(raw, &fields); err != nil {
		return errors.New("invalid template arguments")
	}
	for name := range fields {
		if bytes.Equal(bytes.TrimSpace(fields[name]), []byte("null")) {
			return errors.New("null template argument")
		}
		switch name {
		case "label", "parameters", "headers", "timeout_ms", "max_response_bytes":
		default:
			return errors.New("unknown template argument")
		}
	}
	if rawHeaders, present := fields["headers"]; present {
		var headers map[string]json.RawMessage
		if err := json.Unmarshal(rawHeaders, &headers); err != nil {
			return errors.New("invalid template headers")
		}
		for _, value := range headers {
			if trimmed := bytes.TrimSpace(value); len(trimmed) == 0 || trimmed[0] != '"' {
				return errors.New("template header values must be strings")
			}
		}
	}
	decoder = json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	decoder.UseNumber()
	if err := decoder.Decode(out); err != nil {
		return errors.New("invalid template arguments")
	}
	if out.Parameters == nil || !labelPattern.MatchString(out.Label) {
		return errors.New("invalid template arguments")
	}
	return nil
}

func checkTemplateJSONValue(decoder *json.Decoder, depth int) error {
	if depth > 4 {
		return errors.New("invalid template arguments")
	}
	token, err := decoder.Token()
	if err != nil {
		return err
	}
	delim, ok := token.(json.Delim)
	if !ok {
		return nil
	}
	if delim != '{' && delim != '[' {
		return errors.New("invalid template arguments")
	}
	seen := make(map[string]struct{})
	for decoder.More() {
		if delim == '{' {
			key, err := decoder.Token()
			if err != nil {
				return err
			}
			name, ok := key.(string)
			if !ok {
				return errors.New("invalid template arguments")
			}
			if _, exists := seen[name]; exists {
				return errors.New("duplicate template argument")
			}
			seen[name] = struct{}{}
		}
		if err := checkTemplateJSONValue(decoder, depth+1); err != nil {
			return err
		}
	}
	_, err = decoder.Token()
	return err
}

func (s *Server) callTargetTemplate(ctx context.Context, params callTargetTemplateRequest) (*callTargetResponse, error) {
	start := time.Now()
	label := defaultMetricsUnknownTargetLabel
	status, responseBytes, outcome := 0, 0, metricOutcomeInvalidInput
	defer func() {
		s.recordCallMetrics(ctx, label, status, outcome, responseBytes, start)
		// Template values and credentials must never enter routine logs or the
		// optional full-client payload observers.
		s.logger.InfoContext(ctx, "harpoon template request completed",
			slog.String("label", label), slog.String("outcome", outcome),
			slog.Int("status_code", status), slog.Int64("latency_ms", time.Since(start).Milliseconds()))
	}()
	target, ok := s.registry.Lookup(params.Label)
	if !ok || target.template == nil {
		return nil, newToolError("", "unknown template target")
	}
	label = target.Label
	parameters := maps.Clone(params.Parameters)
	resolved, err := target.template.Render(parameters)
	if err != nil {
		return nil, newToolError(label, err.Error())
	}
	headers, err := target.template.ValidateCallerHeaders(params.Headers)
	if err != nil {
		return nil, newToolError(label, err.Error())
	}
	maps.Copy(headers, target.template.FixedHeaders())
	headers.Set("User-Agent", version.UserAgent)
	timeout, err := normalizeTimeout(params.TimeoutMS)
	if err != nil {
		return nil, newToolError(label, err.Error())
	}
	limit, err := s.normalizeMaxResponseBytes(params.MaxResponseBytes)
	if err != nil {
		return nil, newToolError(label, err.Error())
	}
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, resolved.String(), nil)
	if err != nil {
		return nil, newToolError(label, "invalid template request")
	}
	req.Header = headers
	client := &http.Client{
		Transport:     templateRoundTripper{policy: target.template, parameters: parameters, base: s.httpTransport},
		CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
	}
	outcome = metricOutcomeRequestError
	resp, err := client.Do(req)
	if err != nil {
		return nil, newToolError(label, "template request failed")
	}
	defer func() { _ = resp.Body.Close() }()
	status = resp.StatusCode
	body, tooLarge, err := readLimited(resp.Body, limit)
	responseBytes = len(body)
	if err != nil {
		outcome = metricOutcomeResponseReadError
		return nil, newToolError(label, "response read failed")
	}
	if tooLarge {
		outcome = metricOutcomeResponseTooLarge
		return nil, newToolError(label, "response exceeds size limit")
	}
	outcome = metricOutcomeSuccess
	return &callTargetResponse{StatusCode: status, Headers: resp.Header,
		BodyBase64: base64.StdEncoding.EncodeToString(body), BodySize: len(body)}, nil
}

type templateRoundTripper struct {
	policy     *TargetTemplate
	parameters map[string]any
	base       http.RoundTripper
}

func (t templateRoundTripper) RoundTrip(req *http.Request) (*http.Response, error) {
	if err := t.policy.ValidateRequest(req, t.parameters); err != nil {
		return nil, err
	}
	return t.base.RoundTrip(req)
}

func templateParametersSchema(policy *TargetTemplate) map[string]any {
	properties := make(map[string]any)
	parameters := policy.PublicParameters()
	required := make([]string, 0, len(parameters))
	for name, parameter := range parameters {
		property := map[string]any{"type": "string", "minLength": parameter.MinLength, "maxLength": parameter.MaxLength,
			"allOf": []any{map[string]any{"not": map[string]any{"pattern": "[^A-Za-z0-9_.~-]"}}, map[string]any{"not": map[string]any{"enum": []string{".", ".."}}}},
		}
		if parameter.Description != "" {
			property["description"] = parameter.Description
		}
		if len(parameter.Examples) > 0 {
			property["examples"] = parameter.Examples
		}
		if parameter.Pattern != "" {
			property["pattern"] = "^(?:" + parameter.Pattern + ")$"
		}
		if len(parameter.Enum) > 0 {
			property["enum"] = parameter.Enum
		}
		if len(parameter.ReservedValues) > 0 {
			patterns := make([]string, 0, len(parameter.ReservedValues))
			for _, reserved := range parameter.ReservedValues {
				var pattern strings.Builder
				for _, char := range strings.ToLower(reserved) {
					if char >= 'a' && char <= 'z' {
						pattern.WriteString("[" + string(char) + strings.ToUpper(string(char)) + "]")
					} else {
						pattern.WriteString(regexp.QuoteMeta(string(char)))
					}
				}
				patterns = append(patterns, pattern.String())
			}
			property["not"] = map[string]any{"pattern": "^(?:" + strings.Join(patterns, "|") + ")$"}
		}
		properties[name] = property
		required = append(required, name)
	}
	sort.Strings(required)
	return map[string]any{"type": "object", "properties": properties, "required": required, "additionalProperties": false}
}
