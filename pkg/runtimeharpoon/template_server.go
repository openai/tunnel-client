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
		reflector := &jsonschema.Reflector{DoNotReference: true}
		schema := reflector.Reflect(callTargetTemplateRequest{})
		applyCallTargetSchemaBounds(schema, s.cfg)
		server.AddTool(&mcp.Tool{
			Name: callTargetTemplateTool, Title: "Call Harpoon target template",
			Description: "Call a version-1 GET target template using only its declared string parameters and permitted headers. Discover parameters_schema with list_targets. The target fixes the destination and disables redirects.",
			InputSchema: schema, OutputSchema: buildCallTargetOutputSchema(s.cfg),
			Annotations: &mcp.ToolAnnotations{ReadOnlyHint: true, IdempotentHint: true},
		}, s.callTemplateHandler)
		return
	}
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
	for key, values := range target.template.FixedHeaders() {
		headers[key] = values
	}
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
