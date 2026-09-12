package e2e_test

import (
	"encoding/json"
	"encoding/pem"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/openai/tunnel-client/pkg/controlplane/wiretypes"
	"github.com/openai/tunnel-client/testsupport/mockmcpserver"
	"github.com/openai/tunnel-client/testsupport/mocktunnelservice"
)

// Exercise serialized profile bytes and tool calls through an actual client
// process, including its poll/response transport and verified HTTPS callout.
func TestHarpoonTemplatesRuntimeE2E(t *testing.T) {
	for _, subject := range runtimeSubjectsWithBinaries(t, runtimeFullSubject(), runtimeCustomerSubject()) {
		t.Run(subject.name, func(t *testing.T) {
			runHarpoonTemplatesRuntime(t, subject)
		})
	}
}

type templateRuntimeRequest struct {
	method, escapedPath, rawQuery, body string
	headers                             http.Header
}

type templateRuntimeRecorder struct {
	mu       sync.Mutex
	requests []templateRuntimeRequest
}

func (r *templateRuntimeRecorder) record(req *http.Request) {
	body, _ := io.ReadAll(req.Body)
	r.mu.Lock()
	defer r.mu.Unlock()
	r.requests = append(r.requests, templateRuntimeRequest{
		method: req.Method, escapedPath: req.URL.EscapedPath(), rawQuery: req.URL.RawQuery,
		body: string(body), headers: req.Header.Clone(),
	})
}

func (r *templateRuntimeRecorder) snapshot() []templateRuntimeRequest {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]templateRuntimeRequest(nil), r.requests...)
}

func runHarpoonTemplatesRuntime(t *testing.T, subject runtimeSubject) {
	t.Helper()
	const (
		auth         = "Bearer template-e2e-private-credential"
		caseID       = "private-case-123"
		serialNumber = "private-serial-456"
		sessionID    = "private-session-789"
		deniedID     = "private-denied-object"
	)
	recorder := &templateRuntimeRecorder{}
	upstream := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		recorder.record(r)
		if r.URL.Path == "/legacy" {
			_, _ = w.Write([]byte("legacy-ok"))
			return
		}
		if r.Header.Get("Authorization") != auth {
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		switch r.URL.Path {
		case "/resource/" + deniedID:
			// A syntactically valid identifier still requires upstream object
			// authorization. Harpoon must preserve that denial for the caller.
			http.Error(w, "forbidden", http.StatusForbidden)
		case "/redirect/" + caseID:
			// Even a same-origin, independently allowlisted exact target must
			// not grant a redirect permission to a template operation.
			http.Redirect(w, r, "/legacy", http.StatusFound)
		default:
			_, _ = w.Write([]byte("template-ok"))
		}
	}))
	t.Cleanup(upstream.Close)
	certPEM := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: upstream.Certificate().Raw})
	caPath := filepath.Join(t.TempDir(), "upstream-ca.pem")
	require.NoError(t, os.WriteFile(caPath, certPEM, 0o600))

	type callCase struct {
		name       string
		tool       string
		arguments  string
		statusCode int
		wantError  bool
		redirect   bool
	}
	validArgs := fmt.Sprintf(`{"label":"get_resource","parameters":{"id":%q}}`, caseID)
	cases := []callCase{
		{name: "resource", arguments: fmt.Sprintf(`{"label":"get_resource","parameters":{"id":%q},"headers":{"X-Request-Tag":"allowed-tag"}}`, caseID), statusCode: http.StatusOK},
		{name: "search", arguments: fmt.Sprintf(`{"label":"search_by_serial","parameters":{"serialNumber":%q}}`, serialNumber), statusCode: http.StatusOK},
		{name: "multiple_path_and_query_parameters", arguments: `{"label":"get_case","parameters":{"tenant":"private-tenant","case_id":"private-case","query_id":"private-query","region_id":"private-region"}}`, statusCode: http.StatusOK},
		{name: "profile_by_session", arguments: fmt.Sprintf(`{"label":"get_profile","parameters":{"session_id":%q}}`, sessionID), statusCode: http.StatusOK},
		{name: "case_orders", arguments: fmt.Sprintf(`{"label":"get_case_orders","parameters":{"case_id":%q}}`, caseID), statusCode: http.StatusOK},
		{name: "case_status_with_fixed_queries", arguments: fmt.Sprintf(`{"label":"get_case_status","parameters":{"case_id":%q}}`, caseID), statusCode: http.StatusOK},
		{name: "authorization_denied", arguments: fmt.Sprintf(`{"label":"get_resource","parameters":{"id":%q}}`, deniedID), statusCode: http.StatusForbidden},
		{name: "redirect", arguments: fmt.Sprintf(`{"label":"redirect_resource","parameters":{"id":%q}}`, caseID), redirect: true},
		{name: "legacy_exact", tool: "call_target", arguments: `{"label":"legacy","method":"GET"}`, statusCode: http.StatusOK},
		{name: "missing", arguments: `{"label":"get_resource","parameters":{}}`, wantError: true},
		{name: "unknown_parameter", arguments: `{"label":"get_resource","parameters":{"id":"abc","extra":"abc"}}`, wantError: true},
		{name: "duplicate_parameter", arguments: `{"label":"get_resource","parameters":{"id":"first","id":"second"}}`, wantError: true},
		{name: "missing_parameters", arguments: `{"label":"get_resource"}`, wantError: true},
		{name: "unknown_target", arguments: `{"label":"not_configured","parameters":{"id":"abc"}}`, wantError: true},
		{name: "exact_target_via_template_tool", arguments: `{"label":"legacy","parameters":{}}`, wantError: true},
		{name: "template_target_via_legacy_tool", tool: "call_target", arguments: `{"label":"get_resource","method":"GET"}`, wantError: true},
		{name: "alternate_method", arguments: strings.TrimSuffix(validArgs, "}") + `,"method":"POST"}`, wantError: true},
		{name: "get_body", arguments: strings.TrimSuffix(validArgs, "}") + `,"body":"private-request-body"}`, wantError: true},
		{name: "redirect_override", arguments: strings.TrimSuffix(validArgs, "}") + `,"follow_redirects":true}`, wantError: true},
		{name: "query_override", arguments: strings.TrimSuffix(validArgs, "}") + `,"query":{"admin":"true"}}`, wantError: true},
		{name: "url_override", arguments: strings.TrimSuffix(validArgs, "}") + `,"url":"https://other.example/"}`, wantError: true},
		{name: "excessive_timeout", arguments: strings.TrimSuffix(validArgs, "}") + `,"timeout_ms":120001}`, wantError: true},
		{name: "excessive_response_limit", arguments: strings.TrimSuffix(validArgs, "}") + `,"max_response_bytes":2147483647}`, wantError: true},
	}
	multipleParameters := map[string]string{
		"tenant": "private-tenant", "case_id": "private-case", "query_id": "private-query", "region_id": "private-region",
	}
	for _, parameter := range []struct{ name, invalid string }{
		{"tenant", "private-tenant/escape"},
		{"case_id", "private-case%2fescape"},
		{"query_id", "private-query&admin=true"},
		{"region_id", "private-region?extra=1"},
	} {
		for _, failure := range []string{"missing", "invalid"} {
			values := make(map[string]string, len(multipleParameters))
			for name, value := range multipleParameters {
				values[name] = value
			}
			if failure == "missing" {
				delete(values, parameter.name)
			} else {
				values[parameter.name] = parameter.invalid
			}
			arguments, err := json.Marshal(map[string]any{"label": "get_case", "parameters": values})
			require.NoError(t, err)
			cases = append(cases, callCase{
				name: "multiple_parameters_" + failure + "_" + parameter.name, arguments: string(arguments), wantError: true,
			})
		}
	}
	for _, value := range []struct{ name, raw string }{
		{"null", "null"}, {"number", "123"}, {"boolean", "true"}, {"array", `["abc"]`}, {"object", `{"id":"abc"}`},
	} {
		cases = append(cases, callCase{name: value.name, arguments: fmt.Sprintf(`{"label":"get_resource","parameters":{"id":%s}}`, value.raw), wantError: true})
	}
	for _, value := range []struct{ name, value string }{
		{"empty", ""}, {"overlength", strings.Repeat("x", 65)}, {"reserved", "admin"},
		{"dot", "."}, {"traversal", ".."}, {"slash", "x/y"}, {"backslash", `x\y`},
		{"encoded_separator", "%2f"}, {"double_encoded_separator", "%252f"},
		{"query_injection", "SN1&admin=true"}, {"query_delimiter", "x?admin=true"},
		{"fragment", "x#admin"}, {"control", "x\ny"}, {"format", "abc def"},
	} {
		cases = append(cases, callCase{name: value.name, arguments: fmt.Sprintf(`{"label":"get_resource","parameters":{"id":%q}}`, value.value), wantError: true})
	}
	for _, header := range []string{"Authorization", "Host", "X-Forwarded-Host", "X-OpenAI-Actor-Authorization", "X-HTTP-Method-Override", "X-Undeclared"} {
		cases = append(cases, callCase{
			name:      "header_" + header,
			arguments: strings.TrimSuffix(validArgs, "}") + fmt.Sprintf(`,"headers":{%q:"private-header-override"}}`, header),
			wantError: true,
		})
	}

	ready := make(chan struct{})
	initialize := runtimeArtifactChannelCommandResponse(t,
		"template-initialize", "harpoon", runtimeArtifactInitializePayload("template-initialize"),
		string(wiretypes.ResponsePayloadJSONRPC))
	initialize.DeliverAfter = ready
	commands := []mocktunnelservice.CommandResponse{
		initialize,
		templateRuntimeCommand(t, "template-tools-list", "tools/list", nil, ready),
		templateRuntimeCommand(t, "template-list-targets", "tools/call", json.RawMessage(`{"name":"list_targets","arguments":{}}`), ready),
	}
	for _, tc := range cases {
		tool := tc.tool
		if tool == "" {
			tool = "call_target_template"
		}
		params := json.RawMessage(fmt.Sprintf(`{"name":%q,"arguments":%s}`, tool, tc.arguments))
		commands = append(commands, templateRuntimeCommand(t, "template-"+tc.name, "tools/call", params, ready))
	}
	controlPlane := mocktunnelservice.NewMockTunnelService(
		mocktunnelservice.WithAPIKey(runtimeArtifactAPIKey),
		mocktunnelservice.WithTunnelID(runtimeArtifactTunnelID),
		mocktunnelservice.WithCommandResponses(commands...),
	)
	controlPlane.Start(t)
	mcpServer := mockmcpserver.NewMockMCPServer(mockmcpserver.WithOAuthDiscoveryResources())
	mcpServer.Start(t)

	profilePath := filepath.Join(t.TempDir(), "templates.yaml")
	healthURLFile := filepath.Join(t.TempDir(), "health.url")
	profile := fmt.Sprintf(`config_version: 2
ca_bundle: %s
control_plane:
  base_url: %s
  tunnel_id: %s
  api_key: env:CONTROL_PLANE_API_KEY
  poll_channels: [main, harpoon]
mcp:
  server_urls:
    - channel: main
      url: %s
harpoon:
  targets:
    - label: legacy
      url: %s
%s
health:
  listen_addr: 127.0.0.1:0
  url_file: %s
admin_ui:
  open_browser: false
log:
  level: info
  format: struct-text
`, runtimeArtifactYAMLScalar(caPath), runtimeArtifactYAMLScalar(controlPlane.BaseURL().String()),
		runtimeArtifactTunnelID, runtimeArtifactYAMLScalar(mcpServer.BaseURL().String()),
		runtimeArtifactYAMLScalar(upstream.URL+"/legacy"), templateRuntimeTargets(upstream.URL),
		runtimeArtifactYAMLScalar(healthURLFile))
	require.NoError(t, os.WriteFile(profilePath, []byte(profile), 0o600))
	proc := startRuntimeArtifactWithEnv(t, subject.binary, map[string]string{"HP_AUTH": auth}, "run", "--config", profilePath)
	_ = waitForRuntimeArtifactHealthURL(t, proc, healthURLFile)
	close(ready)
	waitForRuntimeArtifactIdle(t, proc, controlPlane)
	require.NoError(t, proc.stop())

	responses := controlPlane.ReceivedResponses(mocktunnelservice.ResponseMatchMatched)
	require.Len(t, responses, len(commands))
	require.Empty(t, controlPlane.ReceivedResponses(mocktunnelservice.ResponseMatchUnexpected))
	byID := make(map[string]mocktunnelservice.ReceivedResponse, len(responses))
	for _, response := range responses {
		byID[response.RequestID] = response
	}
	initialized := templateRuntimeResult(t, byID["template-initialize"])
	instructions, ok := initialized["instructions"].(string)
	require.True(t, ok, "initialize must advertise Harpoon instructions")
	for _, guidance := range []string{"call_target_template", "template_version", "parameters_schema", "label", "all parameters"} {
		require.Contains(t, instructions, guidance, "initialize must explain how to call template targets")
	}
	tools := templateRuntimeResult(t, byID["template-tools-list"])
	encodedTools, err := json.Marshal(tools)
	require.NoError(t, err)
	require.Contains(t, string(encodedTools), `"call_target_template"`)
	require.Contains(t, string(encodedTools), `"call_target"`)
	discovery := templateRuntimeResult(t, byID["template-list-targets"])
	structured, ok := discovery["structuredContent"].(map[string]any)
	require.True(t, ok, "list_targets must expose structured content")
	targets, ok := structured["targets"].([]any)
	require.True(t, ok)
	targetByLabel := make(map[string]map[string]any)
	for _, target := range targets {
		entry, ok := target.(map[string]any)
		require.True(t, ok)
		label, ok := entry["label"].(string)
		require.True(t, ok)
		targetByLabel[label] = entry
	}
	resource := targetByLabel["get_resource"]
	require.Equal(t, float64(1), resource["template_version"])
	require.Equal(t, []any{"GET"}, resource["allowed_methods"])
	schema, ok := resource["parameters_schema"].(map[string]any)
	require.True(t, ok, "template parameter schema missing")
	require.Equal(t, "object", schema["type"])
	require.Equal(t, false, schema["additionalProperties"])
	require.Equal(t, []any{"id"}, schema["required"])
	multipleSchema, ok := targetByLabel["get_case"]["parameters_schema"].(map[string]any)
	require.True(t, ok, "multiple-parameter discovery schema missing")
	require.Equal(t, []any{"case_id", "query_id", "region_id", "tenant"}, multipleSchema["required"])
	multipleProperties, ok := multipleSchema["properties"].(map[string]any)
	require.True(t, ok, "multiple-parameter discovery properties missing")
	require.Len(t, multipleProperties, 4)
	for _, name := range []string{"tenant", "case_id", "query_id", "region_id"} {
		require.Contains(t, multipleProperties, name)
	}
	for _, label := range []string{"get_profile", "get_case_orders", "get_case_status"} {
		require.Contains(t, targetByLabel, label)
	}
	require.NotContains(t, targetByLabel["legacy"], "template_version")
	require.NotContains(t, targetByLabel["legacy"], "parameters_schema")
	encodedDiscovery, err := json.Marshal(discovery)
	require.NoError(t, err)
	for _, sensitive := range []string{upstream.URL, "/resource/", "/search", "/tenants/", "/profiles", "/cases/orders", "/casestatus/", auth, "HP_AUTH"} {
		require.NotContains(t, instructions, sensitive)
		require.NotContains(t, string(encodedDiscovery), sensitive)
	}

	rejectedCalls := 0
	for _, tc := range cases {
		if tc.wantError {
			rejectedCalls++
		}
		t.Run(tc.name, func(t *testing.T) {
			response, found := byID["template-"+tc.name]
			require.True(t, found)
			var envelope struct {
				Result map[string]any  `json:"result"`
				Error  json.RawMessage `json:"error"`
			}
			require.NoError(t, json.Unmarshal(response.JSONResponse, &envelope))
			failed := len(envelope.Error) > 0 || envelope.Result["isError"] == true
			if tc.wantError {
				require.True(t, failed, "invalid invocation accepted: %s", response.JSONResponse)
				for _, sensitive := range []string{upstream.URL, caseID, auth, "private-header-override", "private-request-body", "private-tenant", "private-case", "private-query", "private-region"} {
					require.NotContains(t, string(response.JSONResponse), sensitive)
				}
				return
			}
			require.False(t, failed, "valid invocation failed: %s", response.JSONResponse)
			structured, ok := envelope.Result["structuredContent"].(map[string]any)
			require.True(t, ok, "missing structured result: %s", response.JSONResponse)
			wantStatus := tc.statusCode
			if tc.redirect {
				wantStatus = http.StatusFound
			}
			require.Equal(t, float64(wantStatus), structured["status_code"])
		})
	}

	// All control-plane calls have settled and the process has exited. Exact
	// counts therefore prove rejected calls and redirects emitted no request,
	// without waiting an arbitrary time for a request that must never happen.
	requests := recorder.snapshot()
	require.Len(t, requests, 9, "invalid inputs or redirects reached the upstream: %#v", requests)
	byPath := make(map[string]templateRuntimeRequest)
	for _, request := range requests {
		require.Equal(t, http.MethodGet, request.method)
		require.Empty(t, request.body)
		require.NotContains(t, byPath, request.escapedPath, "unexpected duplicate upstream request")
		byPath[request.escapedPath] = request
		if request.escapedPath != "/legacy" {
			require.Equal(t, auth, request.headers.Get("Authorization"))
		}
	}
	require.Contains(t, byPath, "/resource/"+caseID)
	require.Empty(t, byPath["/resource/"+caseID].rawQuery)
	require.Equal(t, "allowed-tag", byPath["/resource/"+caseID].headers.Get("X-Request-Tag"))
	require.Equal(t, "serialNumber="+serialNumber, byPath["/search"].rawQuery)
	require.Equal(t, "archive=false&queryId=private-query&regionId=private-region", byPath["/tenants/private-tenant/cases/private-case"].rawQuery)
	require.Equal(t, "session-id="+sessionID, byPath["/profiles"].rawQuery)
	require.Equal(t, "CaseId="+caseID, byPath["/cases/orders"].rawQuery)
	require.Equal(t, "complaints=true&elevations=true", byPath["/casestatus/"+caseID].rawQuery)
	require.Contains(t, byPath, "/resource/"+deniedID)
	require.Contains(t, byPath, "/redirect/"+caseID)
	require.Contains(t, byPath, "/legacy")
	require.Empty(t, byPath["/legacy"].headers.Get("Authorization"), "template credential leaked into exact target")
	for _, sensitive := range []string{auth, caseID, serialNumber, sessionID, deniedID, "private-tenant", "private-query", "private-region", "private-header-override", "private-request-body", upstream.URL + "/resource/"} {
		require.NotContains(t, proc.output.String(), sensitive, "routine logs exposed private request material")
	}
	t.Logf("completed %d tool invocations (%d rejected), %d total control-plane commands, and exactly %d HTTPS requests", len(cases), rejectedCalls, len(commands), len(requests))
	t.Run("version_one_rejects_templates_at_startup", func(t *testing.T) {
		// An operator cannot accidentally activate templates using the legacy
		// config version. The rejection must happen before any request is sent.
		legacyProfile := strings.Replace(profile, "config_version: 2", "config_version: 1", 1)
		legacyPath := filepath.Join(t.TempDir(), "legacy-config.yaml")
		require.NoError(t, os.WriteFile(legacyPath, []byte(legacyProfile), 0o600))
		rejected := startRuntimeArtifactWithEnv(t, subject.binary, map[string]string{"HP_AUTH": auth}, "run", "--config", legacyPath)
		err, exited := rejected.wait(runtimeArtifactSignalTimeout)
		require.True(t, exited, "legacy-version template config did not fail at startup")
		require.Error(t, err)
		require.Contains(t, rejected.output.String(), "config_version")
		require.NotContains(t, rejected.output.String(), auth)
		require.Len(t, recorder.snapshot(), len(requests), "invalid config caused an upstream request")
	})
}

func templateRuntimeTargets(origin string) string {
	type target struct {
		label, path, query string
		parameters         []string
	}
	var rendered strings.Builder
	for _, target := range []target{
		{"get_resource", "/resource/{id}", "", []string{"id"}},
		{"search_by_serial", "/search", "        query:\n          serialNumber: '{serialNumber}'\n", []string{"serialNumber"}},
		{"get_case", "/tenants/{tenant}/cases/{case_id}", "        query:\n          archive: 'false'\n          queryId: '{query_id}'\n          regionId: '{region_id}'\n", []string{"tenant", "case_id", "query_id", "region_id"}},
		{"get_profile", "/profiles", "        query:\n          session-id: '{session_id}'\n", []string{"session_id"}},
		{"get_case_orders", "/cases/orders", "        query:\n          CaseId: '{case_id}'\n", []string{"case_id"}},
		{"get_case_status", "/casestatus/{case_id}", "        query:\n          elevations: 'true'\n          complaints: 'true'\n", []string{"case_id"}},
		{"redirect_resource", "/redirect/{id}", "", []string{"id"}},
	} {
		fmt.Fprintf(&rendered, `    - label: %s
      description: A bounded inventory operation
      template:
        version: 1
        origin: %s
        method: GET
        path_template: %s
%s        parameters:
`, target.label, runtimeArtifactYAMLScalar(origin), runtimeArtifactYAMLScalar(target.path), target.query)
		for _, name := range target.parameters {
			fmt.Fprintf(&rendered, `          %s:
            type: string
            required: true
            pattern: '[A-Za-z0-9_-]+'
            max_length: 64
            reserved_values: [admin]
`, name)
		}
		rendered.WriteString(`        headers:
          Authorization: env:HP_AUTH
        allowed_headers: [X-Request-Tag]
        follow_redirects: false
`)
	}
	return strings.TrimSuffix(rendered.String(), "\n")
}

func templateRuntimeCommand(t *testing.T, id, method string, params json.RawMessage, ready <-chan struct{}) mocktunnelservice.CommandResponse {
	t.Helper()
	if len(params) == 0 {
		params = json.RawMessage(`{}`)
	}
	// Preserve argument bytes, including duplicate keys, rather than letting
	// a map decode erase malformed caller input before it reaches the client.
	meta := `"_meta":{"io.modelcontextprotocol/protocolVersion":"2026-07-28","io.modelcontextprotocol/clientInfo":{"name":"template-e2e","version":"1"},"io.modelcontextprotocol/clientCapabilities":{}}`
	paramsWithMeta := strings.TrimSuffix(string(params), "}")
	if paramsWithMeta != "{" {
		paramsWithMeta += ","
	}
	payload := json.RawMessage(fmt.Sprintf(`{"jsonrpc":"2.0","id":%q,"method":%q,"params":%s%s}}`, id, method, paramsWithMeta, meta))
	require.True(t, json.Valid(payload), "invalid fixture JSON: %s", payload)
	command := runtimeArtifactChannelCommandResponse(t, id, "harpoon", payload, string(wiretypes.ResponsePayloadJSONRPC))
	command.DeliverAfter = ready
	return command
}

func templateRuntimeResult(t *testing.T, response mocktunnelservice.ReceivedResponse) map[string]any {
	t.Helper()
	var envelope struct {
		Result map[string]any  `json:"result"`
		Error  json.RawMessage `json:"error"`
	}
	require.NoError(t, json.Unmarshal(response.JSONResponse, &envelope))
	require.Empty(t, envelope.Error, string(response.JSONResponse))
	require.NotEqual(t, true, envelope.Result["isError"], string(response.JSONResponse))
	return envelope.Result
}
