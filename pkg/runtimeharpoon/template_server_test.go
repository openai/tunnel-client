package runtimeharpoon

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/stretchr/testify/require"
	"go.opentelemetry.io/otel/attribute"
	sdkmetric "go.opentelemetry.io/otel/sdk/metric"
	"go.opentelemetry.io/otel/sdk/metric/metricdata"

	"github.com/openai/tunnel-client/pkg/runtimeconfig"
)

func TestTemplateMCPInstructionsAndDiscovery(t *testing.T) {
	const legacyInstructions = "Harpoon provides a constrained outbound HTTP client. Use list_targets to see allowlisted targets and call_target to make GET/POST/PUT requests with strict size, timeout, and redirect limits. Harpoon cannot reach arbitrary hosts or paths outside the configured allowlist."
	const customInstructions = "Operator instructions: inspect the target schema before making requests."
	const templateRoute = "For entries with template_version and parameters_schema, use call_target_template with the label and all parameters declared by parameters_schema; each value must satisfy that schema."
	// Capture the existing exact-target contract before constructing template
	// servers; their conditional discovery schema must not mutate this shared value.
	legacySchemaJSON, err := json.Marshal(listTargetsOutputSchema)
	require.NoError(t, err)
	var legacySchema map[string]any
	require.NoError(t, json.Unmarshal(legacySchemaJSON, &legacySchema))
	legacySchemaJSON, err = json.Marshal(legacySchema)
	require.NoError(t, err)
	require.Equal(t, "Allowlisted targets available to call_target.", legacySchema["description"])

	for _, tc := range []struct {
		name         string
		exact        bool
		template     bool
		instructions string
	}{
		{name: "exact only", exact: true},
		{name: "template only", template: true},
		{name: "mixed", exact: true, template: true},
		{name: "custom exact", exact: true, instructions: customInstructions},
		{name: "custom template", template: true, instructions: customInstructions},
		{name: "custom mixed", exact: true, template: true, instructions: customInstructions},
		{name: "exact after templates", exact: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var targets []Target
			if tc.exact {
				targets = append(targets, Target{Label: "exact", BaseURL: runtimeRegistryTestURL(t, "https://exact.example/resource")})
			}
			if tc.template {
				targets = append(targets, Target{Label: "template", Template: templateTestConfig()})
			}
			logger := runtimeRegistryTestLogger()
			registry, err := NewRegistry(logger, false, targets)
			require.NoError(t, err)
			server, err := NewServer(&runtimeconfig.HarpoonConfig{MaxResponseBytes: 1024}, registry, logger, WithInstructions(tc.instructions))
			require.NoError(t, err)
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			t.Cleanup(cancel)
			serverTransport, clientTransport := mcp.NewInMemoryTransports()
			serverSession, err := server.MCPServer().Connect(ctx, &legacyProtocolForTestingTransport{base: serverTransport}, nil)
			require.NoError(t, err)
			t.Cleanup(func() { require.NoError(t, serverSession.Close()) })
			client := mcp.NewClient(&mcp.Implementation{Name: "template-guidance-test", Version: "test"}, nil)
			clientSession, err := client.Connect(ctx, clientTransport, nil)
			require.NoError(t, err)
			t.Cleanup(func() { require.NoError(t, clientSession.Close()) })
			initialized := clientSession.InitializeResult()
			require.NotNil(t, initialized)
			require.Equal(t, "2025-11-25", initialized.ProtocolVersion)
			switch {
			case tc.instructions != "":
				require.Equal(t, customInstructions, initialized.Instructions)
			case tc.template:
				require.Contains(t, initialized.Instructions, "For exact targets, use call_target")
				require.Contains(t, initialized.Instructions, templateRoute)
			default:
				require.Equal(t, legacyInstructions, initialized.Instructions)
			}

			result, err := clientSession.ListTools(ctx, nil)
			require.NoError(t, err)
			tools := make(map[string]*mcp.Tool, len(result.Tools))
			for _, tool := range result.Tools {
				tools[tool.Name] = tool
			}
			require.Contains(t, tools, "call_target")
			require.Contains(t, tools, "list_targets")
			_, templateToolPresent := tools[callTargetTemplateTool]
			require.Equal(t, tc.template, templateToolPresent)
			outputJSON, err := json.Marshal(tools["list_targets"].OutputSchema)
			require.NoError(t, err)
			var output map[string]any
			require.NoError(t, json.Unmarshal(outputJSON, &output))
			if tc.template {
				description, ok := output["description"].(string)
				require.True(t, ok)
				require.Contains(t, description, "use call_target for exact targets")
				require.Contains(t, description, templateRoute)
			} else {
				require.Equal(t, legacySchemaJSON, outputJSON, "exact-target schema bytes must remain unchanged")
			}
			targetProperties := output["properties"].(map[string]any)["targets"].(map[string]any)["items"].(map[string]any)["properties"].(map[string]any)
			for _, field := range []string{"template_version", "parameters_schema"} {
				_, present := targetProperties[field]
				require.Equal(t, tc.template, present, field)
			}
		})
	}
}

func TestTemplateArgumentDecoderStrictContract(t *testing.T) {
	for _, raw := range []string{
		`{"label":"resource","parameters":{"resourceId":"one"},"method":"GET"}`,
		`{"Label":"resource","parameters":{"resourceId":"one"}}`,
		`{"label":"resource","Label":"other","parameters":{"resourceId":"one"}}`,
		`{"label":"resource","label":"other","parameters":{"resourceId":"one"}}`,
		`{"label":"resource","parameters":{"resourceId":"one","resourceId":"two"}}`,
		`{"label":"resource","parameters":{"resourceId":"one"},"headers":{"Accept":"a","Accept":"b"}}`,
		`{"label":"resource","parameters":{"resourceId":"one"},"headers":{"Accept":null}}`,
		`{"label":"resource","parameters":{"resourceId":"one"},"headers":null}`,
		`{"label":"resource","parameters":{"resourceId":"one"},"timeout_ms":null}`,
		`{"label":"resource","parameters":{"resourceId":"one"}} {}`,
		`{"label":"resource","parameters":null}`,
		`{"label":"resource","parameters":[]}`,
		`{"label":"resource"}`, `null`, `[]`,
		`{"label":"resource","parameters":{"resourceId":{"deep":{"nested":{"object":true}}}}}`,
		strings.Repeat(" ", 32769),
	} {
		var params callTargetTemplateRequest
		require.Error(t, decodeTemplateArguments(json.RawMessage(raw), &params), raw)
	}
	var params callTargetTemplateRequest
	require.NoError(t, decodeTemplateArguments(json.RawMessage(" \n"+`{"label":"resource","parameters":{"resourceId":"one"}}`+"\n"), &params))
	require.Equal(t, "one", params.Parameters["resourceId"])
}

type templateServerTransport func(*http.Request) (*http.Response, error)

func (f templateServerTransport) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

func TestTemplateHandlerPrivacyAndBounds(t *testing.T) {
	const identifier = "private-id-123"
	const credential = "Bearer private-credential"
	for _, mode := range []string{"success", "redirect", "transport error", "too large"} {
		t.Run(mode, func(t *testing.T) {
			var logs bytes.Buffer
			logger := slog.New(slog.NewTextHandler(&logs, nil))
			cfg := templateTestConfig()
			cfg.Headers = map[string]string{"Authorization": credential}
			registry, err := NewRegistry(logger, false, []Target{{Label: "resource", Template: cfg}})
			require.NoError(t, err)
			calls, observed := 0, 0
			transport := templateServerTransport(func(req *http.Request) (*http.Response, error) {
				calls++
				require.Equal(t, "/resource/"+identifier, req.URL.Path)
				require.Equal(t, credential, req.Header.Get("Authorization"))
				if mode == "transport error" {
					return nil, errors.New(req.URL.String() + credential)
				}
				status, body := http.StatusOK, "private-response"
				if mode == "too large" {
					body = strings.Repeat("X", 1025)
				}
				if mode == "redirect" {
					status = http.StatusFound
				}
				return &http.Response{StatusCode: status, Body: io.NopCloser(strings.NewReader(body)), Header: http.Header{"Location": {"https://other.example/private"}}, Request: req}, nil
			})
			server, err := NewServer(&runtimeconfig.HarpoonConfig{MaxResponseBytes: 1024}, registry, logger,
				WithHTTPTransport(transport), WithCallObserver(func(CallEvent) { observed++ }))
			require.NoError(t, err)
			result, err := server.callTemplateHandler(context.Background(), &mcp.CallToolRequest{Params: &mcp.CallToolParamsRaw{
				Name: callTargetTemplateTool, Arguments: json.RawMessage(`{"label":"resource","parameters":{"resourceId":"` + identifier + `"}}`),
			}})
			require.NoError(t, err)
			require.Equal(t, mode == "transport error" || mode == "too large", result.IsError)
			require.Equal(t, 1, calls, "redirect must not be followed")
			require.Zero(t, observed, "payload observers must not receive template data")
			for _, private := range []string{identifier, credential, "inventory.example", "private-response"} {
				require.NotContains(t, logs.String(), private)
				if result.IsError {
					require.NotContains(t, result.Content[0].(*mcp.TextContent).Text, private)
				}
			}
			require.Contains(t, logs.String(), "label=resource")
		})
	}
}

func TestTemplateTransportRechecksBoundPolicyBeforeNetwork(t *testing.T) {
	policy, err := CompileTargetTemplate(templateTestConfig())
	require.NoError(t, err)
	parameters := map[string]any{"resourceId": "one"}
	resolved, err := policy.Render(parameters)
	require.NoError(t, err)
	calls := 0
	transport := templateRoundTripper{policy: policy, parameters: parameters, base: templateServerTransport(func(*http.Request) (*http.Response, error) {
		calls++
		return nil, errors.New("unexpected network call")
	})}
	for _, mutate := range []func(*http.Request){
		func(r *http.Request) { r.URL.Path = "/resource/two" },
		func(r *http.Request) { r.URL.RawQuery = "admin=true" },
		func(r *http.Request) { r.Method = http.MethodPost },
		func(r *http.Request) { r.Header.Set("X-HTTP-Method-Override", "DELETE") },
		func(r *http.Request) { r.Host = "other.example" },
	} {
		req, err := http.NewRequest(http.MethodGet, resolved.String(), nil)
		require.NoError(t, err)
		mutate(req)
		_, err = transport.RoundTrip(req)
		require.Error(t, err)
	}
	require.Zero(t, calls)
}

func TestTemplateMetricsIncludeRejectedTargetsWithoutUnboundedLabels(t *testing.T) {
	reader := sdkmetric.NewManualReader()
	provider := sdkmetric.NewMeterProvider(sdkmetric.WithReader(reader))
	t.Cleanup(func() { require.NoError(t, provider.Shutdown(context.Background())) })
	var logs bytes.Buffer
	logger := slog.New(slog.NewTextHandler(&logs, nil))
	registry, err := NewRegistry(logger, false, []Target{
		{Label: "resource", Template: templateTestConfig()},
		{Label: "legacy", BaseURL: runtimeRegistryTestURL(t, "https://legacy.example/target")},
	})
	require.NoError(t, err)
	calls := 0
	server, err := NewServer(&runtimeconfig.HarpoonConfig{MaxResponseBytes: 1024}, registry, logger,
		WithMeter(provider.Meter("template-metrics-test")),
		WithHTTPTransport(templateServerTransport(func(req *http.Request) (*http.Response, error) {
			calls++
			return &http.Response{StatusCode: http.StatusOK, Body: io.NopCloser(strings.NewReader("ok")), Header: http.Header{}, Request: req}, nil
		})))
	require.NoError(t, err)
	for _, test := range []struct {
		label, identifier string
		wantError         bool
	}{
		{label: "private-unknown-a", identifier: "one", wantError: true},
		{label: "private-unknown-b", identifier: "one", wantError: true},
		{label: "legacy", identifier: "one", wantError: true},
		{label: "resource", identifier: "../private-identifier", wantError: true},
		{label: "resource", identifier: "one"},
	} {
		raw, err := json.Marshal(callTargetTemplateRequest{Label: test.label, Parameters: map[string]any{"resourceId": test.identifier}})
		require.NoError(t, err)
		result, err := server.callTemplateHandler(context.Background(), &mcp.CallToolRequest{Params: &mcp.CallToolParamsRaw{
			Name: callTargetTemplateTool, Arguments: raw,
		}})
		require.NoError(t, err)
		require.Equal(t, test.wantError, result.IsError)
	}
	require.Equal(t, 1, calls, "rejected target labels and inputs cannot make outbound requests")
	for _, private := range []string{"private-unknown-a", "private-unknown-b", "private-identifier", "legacy.example"} {
		require.NotContains(t, logs.String(), private)
	}
	var collected metricdata.ResourceMetrics
	require.NoError(t, reader.Collect(context.Background(), &collected))
	key := func(attrs attribute.Set) string {
		require.Equal(t, 3, attrs.Len())
		label, ok := attrs.Value("label")
		require.True(t, ok)
		outcome, ok := attrs.Value("outcome")
		require.True(t, ok)
		status, ok := attrs.Value("status_class")
		require.True(t, ok)
		return label.AsString() + "/" + outcome.AsString() + "/" + status.AsString()
	}
	counts := make(map[string]map[string]int64)
	responseSizes := make(map[string]int64)
	for _, scope := range collected.ScopeMetrics {
		for _, measurement := range scope.Metrics {
			values := make(map[string]int64)
			switch data := measurement.Data.(type) {
			case metricdata.Sum[int64]:
				for _, point := range data.DataPoints {
					values[key(point.Attributes)] = point.Value
				}
			case metricdata.Histogram[float64]:
				for _, point := range data.DataPoints {
					values[key(point.Attributes)] = int64(point.Count)
					require.GreaterOrEqual(t, point.Sum, float64(0))
				}
			case metricdata.Histogram[int64]:
				for _, point := range data.DataPoints {
					values[key(point.Attributes)] = int64(point.Count)
					responseSizes[key(point.Attributes)] = point.Sum
				}
			default:
				t.Fatalf("unexpected Harpoon metric data: %T", data)
			}
			counts[measurement.Name] = values
		}
	}
	expected := map[string]int64{
		"__unknown__/invalid_input/none": 3,
		"resource/invalid_input/none":    1,
		"resource/success/2xx":           1,
	}
	require.Equal(t, map[string]map[string]int64{
		metricNameHarpoonCallTotal:     expected,
		metricNameHarpoonCallLatencyMS: expected,
		metricNameHarpoonResponseSizeB: expected,
	}, counts)
	require.Equal(t, map[string]int64{
		"__unknown__/invalid_input/none": 0,
		"resource/invalid_input/none":    0,
		"resource/success/2xx":           2,
	}, responseSizes)
}
