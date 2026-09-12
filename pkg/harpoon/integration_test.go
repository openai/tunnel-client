package harpoon

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/stretchr/testify/require"

	"github.com/openai/tunnel-client/pkg/config"
	"github.com/openai/tunnel-client/pkg/runtimeconfig"
	"github.com/openai/tunnel-client/pkg/runtimeharpoon"
	"github.com/openai/tunnel-client/pkg/version"
)

// toolCallTestCase defines a test case for calling a harpoon tool via MCP.
// To add more test cases, append to the testCases slice with:
//   - name: descriptive test name
//   - payload: JSON payload matching the format from the user's example
//   - expectedPayload: JSON payload expected from the tool response
//   - setupHTTPServer: function that creates and configures the target HTTP server
//   - harpoonConfig: function that returns a harpoon config for the test case
//   - validateResult: function that validates the tool call result against expected payload
type toolCallTestCase struct {
	name            string
	payload         json.RawMessage
	expectedPayload json.RawMessage
	setupHTTPServer func(*testing.T) *httptest.Server
	harpoonConfig   func(*testing.T, *httptest.Server) config.HarpoonConfig
	validateResult  func(*testing.T, *mcp.CallToolResult, json.RawMessage)
}

func TestHarpoonInMemoryToolCall(t *testing.T) {
	t.Parallel()

	newConnectionNominatedHeadersCase := func() toolCallTestCase {
		receivedHeaders := make(chan http.Header, 1)
		return toolCallTestCase{
			name: "call_target_drops_connection_nominated_headers",
			payload: json.RawMessage(`{
				"method": "tools/call",
				"params": {
					"name": "call_target",
					"arguments": {
						"label": "abc",
						"method": "GET",
						"headers": {
							"Connection": "Authorization, X-API-Key",
							"Authorization": "Bearer nested-secret",
							"X-API-Key": "nested-key-secret",
							"X-Safe-Header": "forwarded"
						}
					}
				}
			}`),
			expectedPayload: json.RawMessage(`{
				"status_code": 200,
				"body_base64": "cG9uZw==",
				"body_size_bytes": 4
			}`),
			setupHTTPServer: func(t *testing.T) *httptest.Server {
				t.Helper()
				return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					receivedHeaders <- r.Header.Clone()
					w.Header().Set("Content-Type", "text/plain")
					_, _ = w.Write([]byte("pong"))
				}))
			},
			harpoonConfig: func(t *testing.T, httpServer *httptest.Server) config.HarpoonConfig {
				t.Helper()
				return config.HarpoonConfig{
					AllowPlaintextHTTP: true,
					MaxResponseBytes:   config.DefaultHarpoonMaxResponseBytes,
					MaxRedirects:       config.DefaultHarpoonMaxRedirects,
					Targets: []config.HarpoonTarget{{
						Label:       "abc",
						Description: "Test target",
						BaseURL:     mustParseURL(t, httpServer.URL),
					}},
				}
			},
			validateResult: func(t *testing.T, result *mcp.CallToolResult, expected json.RawMessage) {
				t.Helper()
				validatePayloadResult(t, result, expected)
				headers := <-receivedHeaders
				require.Empty(t, headers.Get("Authorization"))
				require.Empty(t, headers.Get("X-API-Key"))
				require.Equal(t, "forwarded", headers.Get("X-Safe-Header"))
			},
		}
	}

	newRedirectAllowedCase := func() toolCallTestCase {
		redirectTargetURL := ""
		return toolCallTestCase{
			name: "call_target_redirect_allowed",
			payload: json.RawMessage(`{
				"method": "tools/call",
				"params": {
					"name": "call_target",
					"arguments": {
						"label": "abc",
												"method": "GET",
						"headers": {}
					},
					"_meta": {
						"progressToken": 5
					}
				}
			}`),
			expectedPayload: json.RawMessage(`{
				"status_code": 200,
				"body_base64": "cmVkaXJlY3RlZA==",
				"body_size_bytes": 10
			}`),
			setupHTTPServer: func(t *testing.T) *httptest.Server {
				t.Helper()
				redirectTarget := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					w.Header().Set("Content-Type", "text/plain")
					_, _ = w.Write([]byte("redirected"))
				}))
				t.Cleanup(redirectTarget.Close)
				redirectTargetURL = redirectTarget.URL
				server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					http.Redirect(w, r, redirectTarget.URL+"/", http.StatusMovedPermanently)
				}))
				return server
			},
			harpoonConfig: func(t *testing.T, httpServer *httptest.Server) config.HarpoonConfig {
				t.Helper()
				return config.HarpoonConfig{
					AllowPlaintextHTTP: true,
					MaxResponseBytes:   config.DefaultHarpoonMaxResponseBytes,
					MaxRedirects:       config.DefaultHarpoonMaxRedirects,
					Targets: []config.HarpoonTarget{
						{
							Label:       "abc",
							Description: "Redirecting target",
							BaseURL:     mustParseURL(t, httpServer.URL),
						},
						{
							Label:       "redirected",
							Description: "Redirect destination",
							BaseURL:     mustParseURL(t, redirectTargetURL+"/"),
						},
					},
				}
			},
			validateResult: validatePayloadResult,
		}
	}

	testCases := []toolCallTestCase{
		{
			name: "call_target_with_get",
			payload: json.RawMessage(`{
				"method": "tools/call",
				"params": {
					"name": "call_target",
					"arguments": {
						"label": "abc",
												"method": "GET",
						"headers": {}
					},
					"_meta": {
						"progressToken": 3
					}
				}
			}`),
			expectedPayload: json.RawMessage(`{
				"status_code": 200,
				"body_base64": "cG9uZw==",
				"body_size_bytes": 4
			}`),
			setupHTTPServer: func(t *testing.T) *httptest.Server {
				t.Helper()
				server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					require.Equal(t, http.MethodGet, r.Method)
					require.Equal(t, "/", r.URL.Path)
					w.Header().Set("Content-Type", "text/plain")
					_, _ = w.Write([]byte("pong"))
				}))
				return server
			},
			harpoonConfig: func(t *testing.T, httpServer *httptest.Server) config.HarpoonConfig {
				t.Helper()
				return config.HarpoonConfig{
					AllowPlaintextHTTP: true,
					MaxResponseBytes:   config.DefaultHarpoonMaxResponseBytes,
					MaxRedirects:       config.DefaultHarpoonMaxRedirects,
					Targets: []config.HarpoonTarget{{
						Label:       "abc",
						Description: "Test target",
						BaseURL:     mustParseURL(t, httpServer.URL),
					}},
				}
			},
			validateResult: validatePayloadResult,
		},
		{
			name: "call_target_with_simple_input",
			payload: json.RawMessage(`{
				"method": "tools/call",
				"params": {
					"name": "call_target",
					"arguments": {
						"label": "abc",
						"method": "GET"
					}
				}
			}`),
			expectedPayload: json.RawMessage(`{
				"status_code": 200,
				"body_base64": "cG9uZw==",
				"body_size_bytes": 4
			}`),
			setupHTTPServer: func(t *testing.T) *httptest.Server {
				t.Helper()
				server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					require.Equal(t, http.MethodGet, r.Method)
					require.Equal(t, "/", r.URL.Path)
					w.Header().Set("Content-Type", "text/plain")
					_, _ = w.Write([]byte("pong"))
				}))
				return server
			},
			harpoonConfig: func(t *testing.T, httpServer *httptest.Server) config.HarpoonConfig {
				t.Helper()
				return config.HarpoonConfig{
					AllowPlaintextHTTP: true,
					MaxResponseBytes:   config.DefaultHarpoonMaxResponseBytes,
					MaxRedirects:       config.DefaultHarpoonMaxRedirects,
					Targets: []config.HarpoonTarget{{
						Label:       "abc",
						Description: "Test target",
						BaseURL:     mustParseURL(t, httpServer.URL),
					}},
				}
			},
			validateResult: validatePayloadResult,
		},
		{
			name: "call_target_redirect_blocked",
			payload: json.RawMessage(`{
				"method": "tools/call",
				"params": {
					"name": "call_target",
					"arguments": {
						"label": "abc",
												"method": "GET",
						"headers": {}
					},
					"_meta": {
						"progressToken": 4
					}
				}
			}`),
			setupHTTPServer: func(t *testing.T) *httptest.Server {
				t.Helper()
				redirectTarget := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					w.Header().Set("Content-Type", "text/plain")
					_, _ = w.Write([]byte("redirected"))
				}))
				t.Cleanup(redirectTarget.Close)
				server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					http.Redirect(w, r, redirectTarget.URL+"/", http.StatusMovedPermanently)
				}))
				return server
			},
			harpoonConfig: func(t *testing.T, httpServer *httptest.Server) config.HarpoonConfig {
				t.Helper()
				return config.HarpoonConfig{
					AllowPlaintextHTTP: true,
					MaxResponseBytes:   config.DefaultHarpoonMaxResponseBytes,
					MaxRedirects:       config.DefaultHarpoonMaxRedirects,
					Targets: []config.HarpoonTarget{{
						Label:       "abc",
						Description: "Redirecting target",
						BaseURL:     mustParseURL(t, httpServer.URL),
					}},
				}
			},
			validateResult: func(t *testing.T, result *mcp.CallToolResult, _ json.RawMessage) {
				t.Helper()
				require.True(t, result.IsError, "tool call should return error")
				if len(result.Content) == 0 {
					t.Fatalf("expected error content")
				}
				text, ok := result.Content[0].(*mcp.TextContent)
				require.True(t, ok, "expected text content for error")
				require.Contains(t, text.Text, "redirect blocked")
			},
		},
		newConnectionNominatedHeadersCase(),
		newRedirectAllowedCase(),
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			httpServer := tc.setupHTTPServer(t)
			defer httpServer.Close()

			_, clientTransport := setupHarpoon(t, tc.harpoonConfig(t, httpServer))

			client := mcp.NewClient(&mcp.Implementation{Name: "test-client", Version: "1.0.0"}, nil)

			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()

			session, err := client.Connect(ctx, clientTransport, nil)
			require.NoError(t, err)
			defer func() {
				_ = session.Close()
			}()

			var payload struct {
				Method string          `json:"method"`
				Params json.RawMessage `json:"params"`
			}
			require.NoError(t, json.Unmarshal(tc.payload, &payload))
			require.Equal(t, "tools/call", payload.Method)

			var toolParams struct {
				Name      string          `json:"name"`
				Arguments json.RawMessage `json:"arguments"`
				Meta      struct {
					ProgressToken float64 `json:"progressToken"`
				} `json:"_meta"`
			}
			require.NoError(t, json.Unmarshal(payload.Params, &toolParams))
			require.Equal(t, "call_target", toolParams.Name)

			var arguments map[string]any
			require.NoError(t, json.Unmarshal(toolParams.Arguments, &arguments))

			callParams := &mcp.CallToolParams{
				Name:      toolParams.Name,
				Arguments: arguments,
			}
			if toolParams.Meta.ProgressToken > 0 {
				callParams.SetProgressToken(int(toolParams.Meta.ProgressToken))
			}

			result, err := session.CallTool(ctx, callParams)
			require.NoError(t, err)
			validateResult := tc.validateResult
			if validateResult == nil {
				validateResult = validatePayloadResult
			}
			validateResult(t, result, tc.expectedPayload)
		})
	}
}

func TestHarpoonToolSchemas(t *testing.T) {
	t.Parallel()

	httpServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/plain")
		_, _ = w.Write([]byte("pong"))
	}))
	t.Cleanup(httpServer.Close)

	cfg := config.HarpoonConfig{
		AllowPlaintextHTTP: true,
		MaxResponseBytes:   config.DefaultHarpoonMaxResponseBytes,
		MaxRedirects:       config.DefaultHarpoonMaxRedirects,
		Targets: []config.HarpoonTarget{{
			Label:       "abc",
			Description: "Test target",
			BaseURL:     mustParseURL(t, httpServer.URL),
		}},
	}

	_, clientTransport := setupHarpoon(t, cfg)

	client := mcp.NewClient(&mcp.Implementation{Name: "test-client", Version: "1.0.0"}, nil)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	session, err := client.Connect(ctx, clientTransport, nil)
	require.NoError(t, err)
	defer func() {
		_ = session.Close()
	}()

	init := session.InitializeResult()
	require.NotNil(t, init)
	require.NotNil(t, init.ServerInfo)
	require.Equal(t, "harpoon", init.ServerInfo.Name)
	require.Equal(t, "Harpoon (Constrained HTTP Client)", init.ServerInfo.Title)
	require.Equal(t, version.Version, init.ServerInfo.Version)
	require.Contains(t, init.Instructions, "constrained outbound HTTP client")
	require.Contains(t, init.Instructions, "allowlisted targets")
	require.Contains(t, init.Instructions, "cannot reach arbitrary hosts")

	result, err := session.ListTools(ctx, nil)
	require.NoError(t, err)

	tools := map[string]*mcp.Tool{}
	for _, tool := range result.Tools {
		tools[tool.Name] = tool
	}

	callTarget := tools["call_target"]
	require.NotNil(t, callTarget, "call_target tool should be present")
	require.NotNil(t, callTarget.InputSchema, "call_target input schema should be present")
	require.NotNil(t, callTarget.OutputSchema, "call_target output schema should be present")

	listTargets := tools["list_targets"]
	require.NotNil(t, listTargets, "list_targets tool should be present")
	require.NotNil(t, listTargets.InputSchema, "list_targets input schema should be present")
	require.NotNil(t, listTargets.OutputSchema, "list_targets output schema should be present")

	oauthAudience := tools["get_oauth_target_audience"]
	require.NotNil(t, oauthAudience, "get_oauth_target_audience tool should be present")
	require.NotNil(t, oauthAudience.InputSchema, "audience input schema should be present")
	require.NotNil(t, oauthAudience.OutputSchema, "audience output schema should be present")
	require.NotNil(t, oauthAudience.Annotations)
	require.True(t, oauthAudience.Annotations.ReadOnlyHint)
	require.True(t, oauthAudience.Annotations.IdempotentHint)

	expectedCallTargetInput := json.RawMessage(`{
		"type": "object",
		"required": ["label", "method"],
		"properties": {
			"label": {
				"type": "string",
				"description": "Allowlisted target label",
				"pattern": "^[a-z0-9][a-z0-9_-]{0,63}$",
				"minLength": 1,
				"maxLength": 64
			},
			"method": {
				"type": "string",
				"description": "HTTP method for the outbound request",
				"enum": ["GET", "POST", "PUT"]
			},
				"headers": {
					"type": "object",
					"description": "HTTP headers to include in the request; transport proxy forwarding headers plus client-managed identity headers plus caller-supplied fields nominated by Connection are blocked",
					"default": {},
					"propertyNames": {
					"type": "string",
					"pattern": "^[!#$%&'*+.^_\u0060|~0-9A-Za-z-]+$"
				}
			},
			"body": {
				"type": "string",
				"description": "Request body as a raw string"
			},
			"timeout_ms": {
				"type": "integer",
				"description": "Request timeout in milliseconds",
				"minimum": 100,
				"maximum": 120000,
				"default": 30000
			},
			"max_response_bytes": {
				"type": "integer",
				"description": "Maximum response bytes to read",
				"minimum": 1,
				"maximum": 102400,
				"default": 102400
			},
			"follow_redirects": {
				"type": "boolean",
				"description": "Whether to follow HTTP redirects",
				"default": true
			},
			"max_redirects": {
				"type": "integer",
				"description": "Maximum redirects to follow when follow_redirects is true",
				"minimum": 0,
				"maximum": 5,
				"default": 5
			}
		}
	}`)
	requireToolSchemaSubset(t, callTarget.InputSchema, expectedCallTargetInput)

	expectedCallTargetOutput := json.RawMessage(`{
		"type": "object",
		"properties": {
			"status_code": {
				"type": "integer",
				"description": "HTTP status code returned by the target.",
				"minimum": 100,
				"maximum": 599
			},
			"headers": {
				"type": "object",
				"description": "Response headers returned by the target.",
				"propertyNames": {
					"type": "string",
					"pattern": "^[!#$%&'*+.^_\u0060|~0-9A-Za-z-]+$"
				}
			},
			"body_base64": {
				"type": "string",
				"description": "Base64-encoded response body bytes.",
				"contentEncoding": "base64"
			},
			"body_size_bytes": {
				"type": "integer",
				"description": "Number of bytes in body_base64.",
				"minimum": 0,
				"maximum": 102400
			},
			"truncated": {
				"type": "boolean",
				"description": "Whether the response body was truncated."
			}
		}
	}`)
	requireToolSchemaSubset(t, callTarget.OutputSchema, expectedCallTargetOutput)

	expectedListTargetsInput := json.RawMessage(`{
		"type": "object",
		"title": "List Harpoon targets",
		"description": "List available allowlisted targets.",
		"properties": {
			"categories": {
				"type": "array",
				"description": "Target categories to include.",
				"items": {
					"type": "string"
				}
			},
			"sources": {
				"type": "array",
				"description": "Target sources to include.",
				"items": {
					"type": "string"
				}
			},
			"tags": {
				"type": "array",
				"description": "Target tags to include (all tags must match).",
				"items": {
					"type": "string"
				}
			}
		}
	}`)
	requireToolSchemaSubset(t, listTargets.InputSchema, expectedListTargetsInput)

	expectedListTargetsOutput := json.RawMessage(`{
		"type": "object",
		"properties": {
			"targets": {
				"type": "array",
				"items": {
					"type": "object",
					"properties": {
						"label": {
							"type": "string",
							"description": "Target label.",
							"pattern": "^[a-z0-9][a-z0-9_-]{0,63}$",
							"minLength": 1,
							"maxLength": 64
						},
						"description": {
							"type": "string",
							"description": "Target description."
						},
						"category": {
							"type": "string",
							"description": "Target category."
						},
						"source": {
							"type": "string",
							"description": "Target source."
						},
						"tags": {
							"type": "array",
							"description": "Target tags.",
							"items": {
								"type": "string"
							}
						},
						"allowed_methods": {
							"type": "array",
							"description": "HTTP methods permitted for this target",
							"items": {
								"type": "string",
								"enum": ["GET", "POST", "PUT"]
							}
						}
					}
				}
			}
		}
	}`)
	requireToolSchemaSubset(t, listTargets.OutputSchema, expectedListTargetsOutput)

	expectedOAuthAudienceInput := json.RawMessage(`{
		"type": "object",
		"title": "Get OAuth target audience",
		"description": "Resolve the exact upstream URL for an allowlisted OAuth token endpoint.",
		"properties": {
			"label": {
				"type": "string",
				"description": "OAuth token-endpoint target label.",
				"pattern": "^[a-z0-9][a-z0-9_-]{0,63}$",
				"minLength": 1,
				"maxLength": 64
			}
		}
	}`)
	requireToolSchemaSubset(t, oauthAudience.InputSchema, expectedOAuthAudienceInput)

	expectedOAuthAudienceOutput := json.RawMessage(`{
		"type": "object",
		"title": "OAuth target audience",
		"description": "Exact private_key_jwt audience for an allowlisted OAuth token endpoint.",
		"properties": {
			"audience": {
				"type": "string",
				"description": "Exact upstream OAuth token endpoint URL to use as a private_key_jwt audience.",
				"format": "uri"
			}
		}
	}`)
	requireToolSchemaSubset(t, oauthAudience.OutputSchema, expectedOAuthAudienceOutput)
}

func TestHarpoonTemplateInstructionsAndDiscovery(t *testing.T) {
	const legacyInstructions = "Harpoon provides a constrained outbound HTTP client. Use list_targets to see allowlisted targets and call_target to make GET/POST/PUT requests with strict size, timeout, and redirect limits. get_oauth_target_audience is a narrow opt-in lookup for OAuth token-endpoint private_key_jwt audiences. Harpoon cannot reach arbitrary hosts or paths outside the configured allowlist."
	const templateRoute = "For entries with template_version and parameters_schema, use call_target_template with the label and all parameters declared by parameters_schema; each value must satisfy that schema."
	for _, tc := range []struct {
		name     string
		exact    bool
		template bool
		opts     []ServerOption
	}{
		{name: "exact only", exact: true},
		{name: "template only", template: true},
		{name: "mixed", exact: true, template: true},
		// The full adapter owns its instructions and has always applied them
		// after caller options. Keep that precedence, including empty options.
		{name: "exact adapter precedence", exact: true, opts: []ServerOption{runtimeharpoon.WithInstructions("Caller instructions")}},
		{name: "template adapter precedence", template: true, opts: []ServerOption{runtimeharpoon.WithInstructions("Caller instructions")}},
		{name: "mixed empty instructions", exact: true, template: true, opts: []ServerOption{runtimeharpoon.WithInstructions("")}},
		{name: "exact after templates", exact: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var targets []Target
			if tc.exact {
				targets = append(targets, Target{Label: "exact", BaseURL: mustParseURL(t, "https://exact.example/resource")})
			}
			if tc.template {
				targets = append(targets, Target{Label: "template", Template: &runtimeconfig.HarpoonTargetTemplate{
					Version: 1, Origin: "https://template.example", Method: http.MethodGet, PathTemplate: "/resources/{resourceId}",
					Parameters: map[string]runtimeconfig.HarpoonTemplateParameter{
						"resourceId": {Type: "string", Required: true, Pattern: "[A-Za-z0-9_-]+", MaxLength: 64},
					},
				}})
			}
			logger := slog.New(slog.NewTextHandler(io.Discard, nil))
			registry, err := NewRegistry(logger, false, targets)
			require.NoError(t, err)
			server, err := NewServer(&config.HarpoonConfig{MaxResponseBytes: 1024}, registry, nil, logger, tc.opts...)
			require.NoError(t, err)
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			t.Cleanup(cancel)
			serverTransport, clientTransport := mcp.NewInMemoryTransports()
			serverSession, err := server.MCPServer().Connect(ctx, serverTransport, nil)
			require.NoError(t, err)
			t.Cleanup(func() { require.NoError(t, serverSession.Close()) })
			client := mcp.NewClient(&mcp.Implementation{Name: "full-template-guidance-test", Version: "test"}, nil)
			session, err := client.Connect(ctx, clientTransport, nil)
			require.NoError(t, err)
			t.Cleanup(func() { require.NoError(t, session.Close()) })
			initialized := session.InitializeResult()
			require.NotNil(t, initialized)
			if tc.template {
				require.Contains(t, initialized.Instructions, "For exact targets, use call_target")
				require.Contains(t, initialized.Instructions, templateRoute)
				require.Contains(t, initialized.Instructions, "get_oauth_target_audience is a narrow opt-in lookup")
			} else {
				require.Equal(t, legacyInstructions, initialized.Instructions)
			}
			require.NotContains(t, initialized.Instructions, "Caller instructions")
			result, err := session.ListTools(ctx, nil)
			require.NoError(t, err)
			tools := make(map[string]*mcp.Tool, len(result.Tools))
			for _, tool := range result.Tools {
				tools[tool.Name] = tool
			}
			for _, name := range []string{"list_targets", "call_target", "get_oauth_target_audience"} {
				require.Contains(t, tools, name)
			}
			_, templateToolPresent := tools["call_target_template"]
			require.Equal(t, tc.template, templateToolPresent)
			outputJSON, err := json.Marshal(tools["list_targets"].OutputSchema)
			require.NoError(t, err)
			var output map[string]any
			require.NoError(t, json.Unmarshal(outputJSON, &output))
			if tc.template {
				require.Contains(t, output["description"], "use call_target for exact targets")
				require.Contains(t, output["description"], templateRoute)
			} else {
				require.Equal(t, "Allowlisted targets available to call_target.", output["description"])
			}
			targetProperties := output["properties"].(map[string]any)["targets"].(map[string]any)["items"].(map[string]any)["properties"].(map[string]any)
			for _, field := range []string{"template_version", "parameters_schema"} {
				_, present := targetProperties[field]
				require.Equal(t, tc.template, present, field)
			}
			catalogResult, err := session.CallTool(ctx, &mcp.CallToolParams{Name: "list_targets", Arguments: map[string]any{}})
			require.NoError(t, err)
			require.False(t, catalogResult.IsError)
			var catalog struct {
				Targets []map[string]any `json:"targets"`
			}
			require.NoError(t, json.Unmarshal(extractResultPayload(t, catalogResult), &catalog))
			require.Len(t, catalog.Targets, len(targets))
			for _, target := range catalog.Targets {
				if target["label"] == "template" {
					require.Equal(t, float64(1), target["template_version"])
					parameters := target["parameters_schema"].(map[string]any)
					require.Equal(t, []any{"resourceId"}, parameters["required"])
					require.Contains(t, parameters["properties"], "resourceId")
				} else {
					require.NotContains(t, target, "template_version")
					require.NotContains(t, target, "parameters_schema")
				}
			}
		})
	}
}

func setupHarpoon(t *testing.T, cfg config.HarpoonConfig) (*Server, *mcp.InMemoryTransport) {
	t.Helper()

	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	registry, err := NewRegistry(logger, cfg.AllowPlaintextHTTP, convertTargets(cfg.Targets))
	require.NoError(t, err)
	buffer := NewCallBuffer()
	server, err := NewServer(&cfg, registry, buffer, logger)
	require.NoError(t, err)

	mcpServer := server.MCPServer()
	serverTransport, clientTransport := mcp.NewInMemoryTransports()

	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)

	serverDone := make(chan error, 1)
	go func() {
		serverDone <- mcpServer.Run(ctx, serverTransport)
	}()

	t.Cleanup(func() {
		cancel()
		select {
		case err := <-serverDone:
			if err != nil && err != context.Canceled {
				t.Errorf("harpoon MCP server stopped with error: %v", err)
			}
		case <-time.After(time.Second):
			t.Errorf("harpoon MCP server did not stop before timeout")
		}
	})

	return server, clientTransport
}

func validatePayloadResult(t *testing.T, result *mcp.CallToolResult, expectedPayload json.RawMessage) {
	t.Helper()
	require.False(t, result.IsError, "tool call should not return error")

	payload := extractResultPayload(t, result)
	requireJSONSubset(t, expectedPayload, payload)
}

func extractResultPayload(t *testing.T, result *mcp.CallToolResult) json.RawMessage {
	t.Helper()
	require.NotEmpty(t, result.Content, "expected response payload")
	text, ok := result.Content[0].(*mcp.TextContent)
	require.True(t, ok, "expected text content payload")
	return json.RawMessage(text.Text)
}

func requireJSONSubset(t *testing.T, expected json.RawMessage, actual json.RawMessage) {
	t.Helper()
	if len(expected) == 0 {
		return
	}
	var expectedValue any
	var actualValue any
	require.NoError(t, json.Unmarshal(expected, &expectedValue))
	require.NoError(t, json.Unmarshal(actual, &actualValue))
	requireJSONContains(t, actualValue, expectedValue)
}

func requireJSONContains(t *testing.T, actual any, expected any) {
	t.Helper()
	if expected == nil {
		require.Nil(t, actual)
		return
	}
	switch expectedValue := expected.(type) {
	case map[string]any:
		actualMap, ok := actual.(map[string]any)
		require.True(t, ok, "expected JSON object, got %T", actual)
		for key, value := range expectedValue {
			actualValue, ok := actualMap[key]
			require.True(t, ok, "missing key %q in payload", key)
			requireJSONContains(t, actualValue, value)
		}
	case []any:
		actualSlice, ok := actual.([]any)
		require.True(t, ok, "expected JSON array, got %T", actual)
		require.Len(t, actualSlice, len(expectedValue))
		for i := range expectedValue {
			requireJSONContains(t, actualSlice[i], expectedValue[i])
		}
	default:
		require.Equal(t, expected, actual)
	}
}

func requireToolSchemaSubset(t *testing.T, schema any, expected json.RawMessage) {
	t.Helper()
	raw, err := json.Marshal(schema)
	require.NoError(t, err)
	requireJSONSubset(t, expected, raw)
}
