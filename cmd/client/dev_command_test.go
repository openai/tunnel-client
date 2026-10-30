package main

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/stretchr/testify/require"
)

func TestDevProxyFlagsExposeQueueBackendAndUnixIngress(t *testing.T) {
	t.Parallel()
	cmd := newDevProxyCommand(&bytes.Buffer{}, &bytes.Buffer{})
	require.Equal(t, "127.0.0.1:0", cmd.Flags().Lookup("listen").DefValue)
	require.Equal(t, "", cmd.Flags().Lookup("listen-unix-socket").DefValue)
	require.Equal(t, "inmem", cmd.Flags().Lookup("engine-queue-backend").DefValue)
	require.True(t, cmd.Flags().Lookup("engine-redis-url").Hidden)
	require.NotEmpty(t, cmd.Flags().Lookup("engine-redis-url").Deprecated)
}

func TestDevProxyRejectsMutuallyExclusiveIngressFlags(t *testing.T) {
	t.Parallel()
	cmd := newDevProxyCommand(&bytes.Buffer{}, &bytes.Buffer{})
	cmd.SetArgs([]string{
		"--listen", "127.0.0.1:0",
		"--listen-unix-socket", t.TempDir() + "/mcp.sock",
		"--mcp-server-url", "http://127.0.0.1:1/mcp",
	})
	err := cmd.Execute()
	require.ErrorContains(t, err, "--listen and --listen-unix-socket are mutually exclusive")
}

func TestDevProxyRejectsUnknownQueueBackend(t *testing.T) {
	t.Parallel()
	cmd := newDevProxyCommand(&bytes.Buffer{}, &bytes.Buffer{})
	cmd.SetArgs([]string{
		"--engine-queue-backend", "disk",
		"--mcp-server-url", "http://127.0.0.1:1/mcp",
	})
	err := cmd.Execute()
	require.ErrorContains(t, err, `unknown engine queue backend "disk"`)
}

func TestDevProxyRejectsRetiredRedisConfigurationBeforeStartup(t *testing.T) {
	const legacyURL = "redis://:private-redis-password@127.0.0.1:1"
	for _, backend := range []string{"auto", "go", "rust"} {
		for _, test := range []struct {
			name string
			args []string
			env  string
		}{
			{name: "Backend", args: []string{"--engine-queue-backend", "redis"}},
			{name: "URLFlag", args: []string{"--engine-redis-url", legacyURL}},
			{name: "EmptyURLFlag", args: []string{"--engine-redis-url="}},
			{name: "URLEnvironment", env: legacyURL},
			{name: "LegacyCombination", args: []string{"--engine-queue-backend", "redis", "--engine-redis-url", legacyURL}, env: legacyURL},
			{name: "InMemoryWithLegacyURL", args: []string{"--engine-queue-backend", "inmem"}, env: legacyURL},
		} {
			t.Run(backend+"/"+test.name, func(t *testing.T) {
				t.Setenv("TUNNEL_ENGINE_REDIS_URL", test.env)
				var stdout, stderr bytes.Buffer
				cmd := newDevProxyCommand(&stdout, &stderr)
				urlFile := filepath.Join(t.TempDir(), "connection.json")
				cmd.SetArgs(append([]string{"--backend", backend, "--url-file", urlFile}, test.args...))
				err := cmd.Execute()
				require.ErrorContains(t, err, "Redis-backed dev proxy queues are no longer supported")
				require.ErrorContains(t, err, "--engine-queue-backend inmem only for ephemeral local work")
				require.ErrorContains(t, err, "keep the prior tunnel-client version")
				require.NotContains(t, err.Error()+stdout.String()+stderr.String(), "private-redis-password")
				require.NotContains(t, stdout.String(), "MCP URL:")
				require.NoFileExists(t, urlFile)
			})
		}
	}
}

func TestDevMCPStubMetadataEndpoints(t *testing.T) {
	t.Parallel()

	for _, mode := range []struct {
		name      string
		stateless bool
	}{
		{name: "Compatible"},
		{name: "Stateless", stateless: true},
	} {
		t.Run(mode.name, func(t *testing.T) {
			t.Parallel()
			server := httptest.NewServer(newDevMCPStubHandler("demo-stub", "0.1.0", mode.stateless))
			t.Cleanup(server.Close)

			resp, err := http.Get(server.URL + "/.well-known/oauth-protected-resource/mcp")
			require.NoError(t, err)
			t.Cleanup(func() { _ = resp.Body.Close() })
			require.Equal(t, http.StatusOK, resp.StatusCode)

			var protectedResource map[string]any
			require.NoError(t, json.NewDecoder(resp.Body).Decode(&protectedResource))
			require.Equal(t, server.URL+"/mcp", protectedResource["resource"])
			require.Equal(t, []any{server.URL}, protectedResource["authorization_servers"])

			authResp, err := http.Get(server.URL + "/.well-known/oauth-authorization-server")
			require.NoError(t, err)
			t.Cleanup(func() { _ = authResp.Body.Close() })
			require.Equal(t, http.StatusOK, authResp.StatusCode)

			var authServer map[string]any
			require.NoError(t, json.NewDecoder(authResp.Body).Decode(&authServer))
			require.Equal(t, server.URL, authServer["issuer"])
			require.Equal(t, server.URL+"/jwks", authServer["jwks_uri"])
		})
	}
}

func TestDevMCPStubDemoToolsWorkOverStreamableHTTP(t *testing.T) {
	t.Parallel()

	for _, mode := range []struct {
		name      string
		stateless bool
	}{
		{name: "Compatible"},
		{name: "Stateless", stateless: true},
	} {
		t.Run(mode.name, func(t *testing.T) {
			t.Parallel()
			server := httptest.NewServer(newDevMCPStubHandler("demo-stub", "0.1.0", mode.stateless))
			t.Cleanup(server.Close)

			client := mcp.NewClient(&mcp.Implementation{Name: "test-client", Version: "1.0.0"}, nil)
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()

			session, err := client.Connect(ctx, &mcp.StreamableClientTransport{Endpoint: server.URL + "/mcp"}, nil)
			require.NoError(t, err)
			t.Cleanup(func() { _ = session.Close() })
			require.Equal(t, "2026-07-28", session.InitializeResult().ProtocolVersion, "the current SDK must connect without falling back to legacy initialize")

			tools, err := session.ListTools(ctx, nil)
			require.NoError(t, err)
			toolNames := map[string]bool{}
			for _, tool := range tools.Tools {
				toolNames[tool.Name] = true
			}
			require.True(t, toolNames["server_info"])
			require.True(t, toolNames["echo"])
			require.True(t, toolNames["uppercase"])

			echoResult, err := session.CallTool(ctx, &mcp.CallToolParams{
				Name:      "echo",
				Arguments: map[string]any{"input": "hello from tunnel-client"},
			})
			require.NoError(t, err)
			require.False(t, echoResult.IsError)
			require.NotEmpty(t, echoResult.Content)
			echoText, ok := echoResult.Content[0].(*mcp.TextContent)
			require.True(t, ok)
			require.Equal(t, "hello from tunnel-client", echoText.Text)

			uppercaseResult, err := session.CallTool(ctx, &mcp.CallToolParams{
				Name:      "uppercase",
				Arguments: map[string]any{"input": "openai tunnel"},
			})
			require.NoError(t, err)
			require.False(t, uppercaseResult.IsError)
			require.NotEmpty(t, uppercaseResult.Content)
			uppercaseText, ok := uppercaseResult.Content[0].(*mcp.TextContent)
			require.True(t, ok)
			require.Equal(t, "OPENAI TUNNEL", uppercaseText.Text)
		})
	}
}

func TestDevMCPStubAcceptsSelfContainedModernRequests(t *testing.T) {
	t.Parallel()

	for _, mode := range []struct {
		name      string
		stateless bool
	}{
		{name: "Compatible"},
		{name: "Stateless", stateless: true},
	} {
		t.Run(mode.name, func(t *testing.T) {
			t.Parallel()
			server := httptest.NewServer(newDevMCPStubHandler("demo-stub", "0.1.0", mode.stateless))
			t.Cleanup(server.Close)
			endpoint := server.URL + "/mcp"

			// Raw requests cannot hide a failed discovery behind an SDK initialize fallback.
			discoverResponse := postDevMCPStubRequest(t, endpoint, "2026-07-28", "", "server/discover", nil)
			require.Empty(t, discoverResponse.Header.Get("Mcp-Session-Id"))
			discovery := readDevMCPStubResult(t, discoverResponse)
			require.Equal(t, "complete", discovery["resultType"])
			require.Contains(t, discovery["supportedVersions"], "2026-07-28")

			tools := readDevMCPStubResult(t, postDevMCPStubRequest(t, endpoint, "2026-07-28", "", "tools/list", nil))
			require.Equal(t, "complete", tools["resultType"])
			listedTools, ok := tools["tools"].([]any)
			require.True(t, ok)
			names := make([]string, 0, len(listedTools))
			for _, tool := range listedTools {
				entry, ok := tool.(map[string]any)
				require.True(t, ok)
				names = append(names, entry["name"].(string))
			}
			require.ElementsMatch(t, []string{"server_info", "echo", "uppercase"}, names)

			for _, tc := range []struct {
				name      string
				arguments map[string]any
				wantText  string
			}{
				{name: "server_info", arguments: map[string]any{}, wantText: "demo-stub 0.1.0 demo tools: server_info, echo, uppercase"},
				{name: "echo", arguments: map[string]any{"input": "hello modern MCP"}, wantText: "hello modern MCP"},
				{name: "uppercase", arguments: map[string]any{"input": "openai tunnel"}, wantText: "OPENAI TUNNEL"},
			} {
				t.Run(tc.name, func(t *testing.T) {
					t.Parallel()
					response := postDevMCPStubRequest(t, endpoint, "2026-07-28", "", "tools/call", map[string]any{
						"name": tc.name, "arguments": tc.arguments,
					})
					require.Empty(t, response.Header.Get("Mcp-Session-Id"))
					result := readDevMCPStubResult(t, response)
					require.Equal(t, "complete", result["resultType"])
					require.NotEqual(t, true, result["isError"])
					require.Equal(t, []any{map[string]any{"type": "text", "text": tc.wantText}}, result["content"])
				})
			}
		})
	}
}

func TestDevMCPStubPreservesLegacyHTTPSession(t *testing.T) {
	t.Parallel()

	server := httptest.NewServer(newDevMCPStubHandler("demo-stub", "0.1.0", false))
	t.Cleanup(server.Close)
	endpoint := server.URL + "/mcp"
	const protocolVersion = "2025-11-25"
	initializeResponse := postDevMCPStubRequest(t, endpoint, "", "", "initialize", map[string]any{
		"protocolVersion": protocolVersion,
		"capabilities":    map[string]any{},
		"clientInfo":      map[string]any{"name": "legacy-stub-test", "version": "1.0.0"},
	})
	sessionID := initializeResponse.Header.Get("Mcp-Session-Id")
	require.NotEmpty(t, sessionID)
	initialized := readDevMCPStubResult(t, initializeResponse)
	require.Equal(t, protocolVersion, initialized["protocolVersion"])
	require.Equal(t, map[string]any{"name": "demo-stub", "version": "0.1.0"}, initialized["serverInfo"])

	notification := postDevMCPStubRequest(t, endpoint, protocolVersion, sessionID, "notifications/initialized", nil)
	require.Equal(t, http.StatusAccepted, notification.StatusCode)
	require.NoError(t, notification.Body.Close())
	tools := readDevMCPStubResult(t, postDevMCPStubRequest(t, endpoint, protocolVersion, sessionID, "tools/list", nil))
	require.Len(t, tools["tools"], 3)
	call := readDevMCPStubResult(t, postDevMCPStubRequest(t, endpoint, protocolVersion, sessionID, "tools/call", map[string]any{
		"name": "echo", "arguments": map[string]any{"input": "legacy session"},
	}))
	require.Equal(t, []any{map[string]any{"type": "text", "text": "legacy session"}}, call["content"])

	client := &http.Client{Timeout: 5 * time.Second}
	for _, tc := range []struct {
		method string
		status int
	}{
		{method: http.MethodGet, status: http.StatusOK},
		{method: http.MethodDelete, status: http.StatusNoContent},
		{method: http.MethodGet, status: http.StatusNotFound},
	} {
		req, err := http.NewRequestWithContext(t.Context(), tc.method, endpoint, nil)
		require.NoError(t, err)
		req.Header.Set("Accept", "text/event-stream")
		req.Header.Set("Mcp-Session-Id", sessionID)
		req.Header.Set("Mcp-Protocol-Version", protocolVersion)
		resp, err := client.Do(req)
		require.NoError(t, err)
		require.NoError(t, resp.Body.Close())
		require.Equal(t, tc.status, resp.StatusCode, tc.method)
		if tc.method == http.MethodGet && tc.status == http.StatusOK {
			require.Contains(t, resp.Header.Get("Content-Type"), "text/event-stream")
		}
	}
}

func TestDevMCPStubStatelessInitializationAndTools(t *testing.T) {
	t.Parallel()

	for _, protocolVersion := range []string{"2025-03-26", "2025-11-25"} {
		t.Run(protocolVersion, func(t *testing.T) {
			t.Parallel()
			server := httptest.NewServer(newDevMCPStubHandler("stateless-stub", "1.2.3", true))
			t.Cleanup(server.Close)
			endpoint := server.URL + "/mcp"
			initializeResponse := postDevMCPStubRequest(t, endpoint, "", "", "initialize", map[string]any{
				"protocolVersion": protocolVersion,
				"capabilities":    map[string]any{},
				"clientInfo":      map[string]any{"name": "stateless-stub-test", "version": "1.0.0"},
			})
			require.Empty(t, initializeResponse.Header.Get("Mcp-Session-Id"))
			initialized := readDevMCPStubResult(t, initializeResponse)
			require.Equal(t, protocolVersion, initialized["protocolVersion"])
			require.Equal(t, map[string]any{"name": "stateless-stub", "version": "1.2.3"}, initialized["serverInfo"])

			notification := postDevMCPStubRequest(t, endpoint, protocolVersion, "", "notifications/initialized", nil)
			require.Equal(t, http.StatusAccepted, notification.StatusCode)
			require.Empty(t, notification.Header.Get("Mcp-Session-Id"))
			require.NoError(t, notification.Body.Close())

			toolsResponse := postDevMCPStubRequest(t, endpoint, protocolVersion, "", "tools/list", nil)
			require.Empty(t, toolsResponse.Header.Get("Mcp-Session-Id"))
			tools := readDevMCPStubResult(t, toolsResponse)
			require.Len(t, tools["tools"], 3)
			for _, tool := range []struct {
				name string
				want string
			}{
				{name: "echo", want: "stateless tools"},
				{name: "uppercase", want: "STATELESS TOOLS"},
			} {
				response := postDevMCPStubRequest(t, endpoint, protocolVersion, "", "tools/call", map[string]any{
					"name": tool.name, "arguments": map[string]any{"input": "stateless tools"},
				})
				require.Empty(t, response.Header.Get("Mcp-Session-Id"))
				result := readDevMCPStubResult(t, response)
				require.NotEqual(t, true, result["isError"])
				require.Equal(t, []any{map[string]any{"type": "text", "text": tool.want}}, result["content"])
			}

			// The SDK ignores supplied session IDs in stateless mode. Requests
			// cannot accidentally select the default stub's stateful handler.
			response := postDevMCPStubRequest(t, endpoint, protocolVersion, "unowned-session", "tools/list", nil)
			require.Empty(t, response.Header.Get("Mcp-Session-Id"))
			require.Len(t, readDevMCPStubResult(t, response)["tools"], 3)
		})
	}
}

func TestDevMCPStubStatelessRejectsSessionOperations(t *testing.T) {
	t.Parallel()

	server := httptest.NewServer(newDevMCPStubHandler("stateless-stub", "1.2.3", true))
	t.Cleanup(server.Close)
	for _, protocolVersion := range []string{"2025-11-25", "2026-07-28"} {
		for _, sessionID := range []string{"", "unowned-session"} {
			for _, method := range []string{http.MethodGet, http.MethodDelete} {
				t.Run(protocolVersion+"/"+sessionID+"/"+method, func(t *testing.T) {
					t.Parallel()
					req, err := http.NewRequestWithContext(t.Context(), method, server.URL+"/mcp", nil)
					require.NoError(t, err)
					req.Header.Set("Accept", "text/event-stream")
					req.Header.Set("Mcp-Protocol-Version", protocolVersion)
					if sessionID != "" {
						req.Header.Set("Mcp-Session-Id", sessionID)
					}
					resp, err := (&http.Client{Timeout: 5 * time.Second}).Do(req)
					require.NoError(t, err)
					require.NoError(t, resp.Body.Close())
					require.Equal(t, http.StatusMethodNotAllowed, resp.StatusCode)
					require.Equal(t, "POST", resp.Header.Get("Allow"))
					require.Empty(t, resp.Header.Get("Mcp-Session-Id"))
				})
			}
		}
	}
}

func TestDevMCPStubRejectsInvalidPOSTBodies(t *testing.T) {
	t.Parallel()

	for _, mode := range []struct {
		name      string
		stateless bool
	}{
		{name: "Compatible"},
		{name: "Stateless", stateless: true},
	} {
		t.Run(mode.name, func(t *testing.T) {
			t.Parallel()
			server := httptest.NewServer(newDevMCPStubHandler("demo-stub", "0.1.0", mode.stateless))
			t.Cleanup(server.Close)
			for _, tc := range []struct {
				name      string
				body      string
				status    int
				wantError string
			}{
				{name: "MalformedJSON", body: `{"method":`, status: http.StatusBadRequest},
				{name: "OversizedChunkedBody", body: strings.Repeat("x", mcp.DefaultMaxRequestBodyBytes+1), status: http.StatusRequestEntityTooLarge},
				{
					name:      "MissingVersionMetadata",
					body:      `{"jsonrpc":"2.0","id":1,"method":"tools/list","params":{}}`,
					status:    http.StatusBadRequest,
					wantError: "missing or invalid _meta field",
				},
				{
					name:      "MismatchedVersionMetadata",
					body:      `{"jsonrpc":"2.0","id":1,"method":"tools/list","params":{"_meta":{"io.modelcontextprotocol/protocolVersion":"2025-11-25"}}}`,
					status:    http.StatusBadRequest,
					wantError: "does not match request",
				},
			} {
				t.Run(tc.name, func(t *testing.T) {
					t.Parallel()
					req, err := http.NewRequestWithContext(t.Context(), http.MethodPost, server.URL+"/mcp", strings.NewReader(tc.body))
					require.NoError(t, err)
					req.ContentLength = -1
					req.Header.Set("Accept", "application/json, text/event-stream")
					req.Header.Set("Content-Type", "application/json")
					req.Header.Set("Mcp-Protocol-Version", "2026-07-28")
					req.Header.Set("Mcp-Method", "tools/list")
					resp, err := (&http.Client{Timeout: 5 * time.Second}).Do(req)
					require.NoError(t, err)
					body, err := io.ReadAll(resp.Body)
					require.NoError(t, err)
					require.NoError(t, resp.Body.Close())
					require.Equal(t, tc.status, resp.StatusCode, string(body))
					if tc.wantError != "" {
						require.Contains(t, string(body), tc.wantError)
					}
				})
			}
		})
	}
}

func postDevMCPStubRequest(t *testing.T, endpoint, protocolVersion, sessionID, method string, params map[string]any) *http.Response {
	t.Helper()
	return postDevMCPStubRequestWithClient(t, &http.Client{Timeout: 5 * time.Second}, endpoint, protocolVersion, sessionID, method, params)
}

func postDevMCPStubRequestWithClient(t *testing.T, client *http.Client, endpoint, protocolVersion, sessionID, method string, params map[string]any) *http.Response {
	t.Helper()
	if params == nil {
		params = map[string]any{}
	}
	if protocolVersion == "2026-07-28" {
		params["_meta"] = map[string]any{
			"io.modelcontextprotocol/protocolVersion":    protocolVersion,
			"io.modelcontextprotocol/clientInfo":         map[string]any{"name": "modern-stub-test", "version": "1.0.0"},
			"io.modelcontextprotocol/clientCapabilities": map[string]any{},
		}
	}
	payload := map[string]any{"jsonrpc": "2.0", "method": method, "params": params}
	if !strings.HasPrefix(method, "notifications/") {
		payload["id"] = method
	}
	body, err := json.Marshal(payload)
	require.NoError(t, err)
	req, err := http.NewRequestWithContext(t.Context(), http.MethodPost, endpoint, bytes.NewReader(body))
	require.NoError(t, err)
	req.Header.Set("Accept", "application/json, text/event-stream")
	req.Header.Set("Content-Type", "application/json")
	if protocolVersion != "" {
		req.Header.Set("Mcp-Protocol-Version", protocolVersion)
	}
	if sessionID != "" {
		req.Header.Set("Mcp-Session-Id", sessionID)
	}
	if protocolVersion == "2026-07-28" {
		req.Header.Set("Mcp-Method", method)
		if name, ok := params["name"].(string); ok {
			req.Header.Set("Mcp-Name", name)
		}
	}
	resp, err := client.Do(req)
	require.NoError(t, err)
	t.Cleanup(func() { _ = resp.Body.Close() })
	return resp
}

func readDevMCPStubResult(t *testing.T, resp *http.Response) map[string]any {
	t.Helper()
	body, err := io.ReadAll(resp.Body)
	require.NoError(t, err)
	require.NoError(t, resp.Body.Close())
	require.Equal(t, http.StatusOK, resp.StatusCode, string(body))
	if strings.HasPrefix(resp.Header.Get("Content-Type"), "text/event-stream") {
		scanner := bufio.NewScanner(bytes.NewReader(body))
		var data []string
		for scanner.Scan() {
			if payload, ok := strings.CutPrefix(scanner.Text(), "data:"); ok && strings.TrimSpace(payload) != "" {
				data = append(data, strings.TrimSpace(payload))
			}
		}
		require.NoError(t, scanner.Err())
		body = []byte(strings.Join(data, "\n"))
	} else {
		require.Contains(t, resp.Header.Get("Content-Type"), "application/json")
	}
	var envelope struct {
		Result map[string]any `json:"result"`
		Error  map[string]any `json:"error"`
	}
	require.NoError(t, json.Unmarshal(body, &envelope))
	require.Empty(t, envelope.Error, string(body))
	require.NotNil(t, envelope.Result, string(body))
	return envelope.Result
}
