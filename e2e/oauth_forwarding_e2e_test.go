package e2e_test

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"encoding/pem"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/openai/tunnel-client/pkg/controlplane/wiretypes"
	"github.com/openai/tunnel-client/testsupport/mocktunnelservice"
)

// The customer topology has a private MCP origin and a different HTTPS origin
// for protected-resource metadata and OAuth. Exercise the real binaries and
// poll/response protocol: discovery trust alone must not authorize forwarding.
func TestRuntimeOAuthForwardOriginsE2E(t *testing.T) {
	t.Parallel()
	for _, subject := range runtimeSubjectsWithBinaries(t, runtimeFullSubject(), runtimeCustomerSubject(), runtimeCloudflaredSubject()) {
		t.Run(subject.name, func(t *testing.T) {
			t.Parallel()
			for _, forward := range []bool{false, true} {
				t.Run(fmt.Sprintf("forward=%t", forward), func(t *testing.T) {
					t.Parallel()
					runRuntimeOAuthForwardOrigins(t, subject, forward)
				})
			}
		})
	}
}

func runRuntimeOAuthForwardOrigins(t *testing.T, subject runtimeSubject, forward bool) {
	t.Helper()
	const (
		accessToken = "oauth-forward-e2e-access-token"
		mcpSecret   = "oauth-forward-e2e-mcp-secret"
	)
	var metadataHits, tokenHits, authenticatedCalls, thirdOriginHits atomic.Int32
	var leakedMCPHeader atomic.Bool
	thirdOrigin := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		thirdOriginHits.Add(1)
		w.WriteHeader(http.StatusOK)
	}))
	t.Cleanup(thirdOrigin.Close)

	// Construct origins before starting the handlers so every advertised URL is
	// immutable while startup discovery and queued calls run concurrently.
	oauthServer := httptest.NewUnstartedServer(nil)
	oauthOrigin := "https://" + oauthServer.Listener.Addr().String()
	canonicalResource := oauthOrigin + "/resource"
	issuer := oauthOrigin + "/issuer"
	oauthServer.Config.Handler = http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("X-MCP-Secret") != "" {
			leakedMCPHeader.Store(true)
		}
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/metadata":
			metadataHits.Add(1)
			_ = json.NewEncoder(w).Encode(map[string]any{
				"resource": canonicalResource, "authorization_servers": []string{issuer},
			})
		case "/.well-known/oauth-authorization-server/issuer", "/issuer/.well-known/oauth-authorization-server":
			_ = json.NewEncoder(w).Encode(map[string]any{
				"issuer": issuer, "authorization_endpoint": oauthOrigin + "/authorize",
				"token_endpoint": oauthOrigin + "/token", "registration_endpoint": oauthOrigin + "/register",
				"revocation_endpoint": oauthOrigin + "/revoke", "introspection_endpoint": thirdOrigin.URL + "/introspect",
				"response_types_supported": []string{"code"}, "code_challenge_methods_supported": []string{"S256"},
			})
		case "/token":
			tokenHits.Add(1)
			if r.Method != http.MethodPost || r.ParseForm() != nil || r.Form.Get("resource") != canonicalResource || r.Form.Get("code") != "approved-code" {
				http.Error(w, "invalid grant or resource", http.StatusBadRequest)
				return
			}
			_ = json.NewEncoder(w).Encode(map[string]any{"access_token": accessToken, "token_type": "Bearer"})
		case "/revoke":
			http.Redirect(w, r, thirdOrigin.URL+"/redirect-destination", http.StatusTemporaryRedirect)
		default:
			http.NotFound(w, r)
		}
	})
	oauthServer.StartTLS()
	t.Cleanup(oauthServer.Close)

	// This stateless MCP fixture handles startup initialize and a real queued
	// tool call. Only the token returned by the separate OAuth origin permits
	// the tool call, and the grant must retain the canonical resource audience.
	mcpServer := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/mcp" {
			http.NotFound(w, r)
			return
		}
		body, err := io.ReadAll(r.Body)
		if err != nil {
			http.Error(w, "invalid request", http.StatusBadRequest)
			return
		}
		if r.Method == http.MethodPost && len(body) == 0 {
			w.Header().Set("WWW-Authenticate", fmt.Sprintf(`Bearer resource_metadata="%s/metadata"`, oauthOrigin))
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		var request struct {
			ID     json.RawMessage `json:"id"`
			Method string          `json:"method"`
			Params struct {
				ProtocolVersion string `json:"protocolVersion"`
			} `json:"params"`
		}
		if json.Unmarshal(body, &request) != nil {
			http.Error(w, "invalid JSON-RPC", http.StatusBadRequest)
			return
		}
		var result any
		switch request.Method {
		case "initialize":
			result = map[string]any{
				"protocolVersion": request.Params.ProtocolVersion, "capabilities": map[string]any{"tools": map[string]any{}},
				"serverInfo": map[string]string{"name": "separate-origin-mcp", "version": "1"},
			}
		case "notifications/initialized":
			w.WriteHeader(http.StatusAccepted)
			return
		case "tools/call":
			if r.Header.Get("Authorization") != "Bearer "+accessToken || r.Header.Get("X-MCP-Secret") != mcpSecret {
				w.WriteHeader(http.StatusUnauthorized)
				return
			}
			authenticatedCalls.Add(1)
			result = map[string]any{"content": []map[string]string{{"type": "text", "text": "authenticated tool succeeded"}}}
		default:
			http.Error(w, "unsupported method", http.StatusBadRequest)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{"jsonrpc": "2.0", "id": request.ID, "result": result})
	}))
	t.Cleanup(mcpServer.Close)
	caPath := filepath.Join(t.TempDir(), "origins.pem")
	var certificates bytes.Buffer
	for _, server := range []*httptest.Server{mcpServer, oauthServer, thirdOrigin} {
		require.NoError(t, pem.Encode(&certificates, &pem.Block{Type: "CERTIFICATE", Bytes: server.Certificate().Raw}))
	}
	require.NoError(t, os.WriteFile(caPath, certificates.Bytes(), 0o600))

	ids := []string{"list-targets"}
	if forward {
		ids = append(ids, "prmd", "auth-metadata", "token", "authenticated-mcp", "unapproved-target", "unapproved-redirect")
	}
	gates := make([]chan struct{}, len(ids))
	commands := make([]mocktunnelservice.CommandResponse, len(ids))
	payloads := make([]json.RawMessage, len(ids))
	for i, id := range ids {
		gates[i] = make(chan struct{})
		commands[i] = mocktunnelservice.CommandResponse{
			Command: oauthForwardRuntimeCommand(t, id, "harpoon", "tools/call", nil, nil),
			CommandMutator: func(_ json.RawMessage, _ mocktunnelservice.SharedStorage) json.RawMessage {
				// Closing this command's gate publishes its payload after the
				// preceding live response has supplied the endpoint label/token.
				return payloads[i]
			},
			DeliverAfter:      gates[i],
			ExpectedResponses: []mocktunnelservice.ExpectedResponse{{RequestID: id}},
		}
	}
	controlPlane := mocktunnelservice.NewMockTunnelService(
		mocktunnelservice.WithAPIKey(runtimeArtifactAPIKey),
		mocktunnelservice.WithTunnelID(runtimeArtifactTunnelID),
		mocktunnelservice.WithCommandResponses(commands...),
	)
	controlPlane.Start(t)
	healthFile := filepath.Join(t.TempDir(), "health.url")
	args := []string{
		"run", "--control-plane.base-url", controlPlane.BaseURL().String(),
		"--control-plane.tunnel-id", runtimeArtifactTunnelID,
		"--mcp.server-url", mcpServer.URL + "/mcp", "--mcp.oauth-trusted-origin", oauthOrigin,
		"--mcp.extra-headers", "X-MCP-Secret: " + mcpSecret,
		"--ca-bundle", caPath, "--health.listen-addr", "127.0.0.1:0", "--health.url-file", healthFile,
		"--log.format", "json", "--log.level", "info",
	}
	if forward {
		args = append(args, "--harpoon.oauth-forward-origin", oauthOrigin)
	}
	proc := startRuntimeArtifactWithEnv(t, subject.binary, map[string]string{"HARPOON_OAUTH_FORWARD_ORIGINS": ""}, args...)
	waitForRuntimeArtifactHealthURL(t, proc, healthFile)
	waitForRuntimeArtifactOutput(t, proc, "OAuth catalog completion", runtimeArtifactOAuthReadySignal, "harpoon startup catalog digest")

	send := func(i int, channel string, params map[string]any, headers http.Header) mocktunnelservice.ReceivedResponse {
		t.Helper()
		payloads[i] = oauthForwardRuntimeCommand(t, ids[i], channel, "tools/call", params, headers)
		close(gates[i])
		ctx, cancel := context.WithTimeout(context.Background(), runtimeArtifactSignalTimeout)
		defer cancel()
		require.NoErrorf(t, controlPlane.WaitForResponses(ctx, i+1), "waiting for %s; runtime output:\n%s", ids[i], proc.output.String())
		for _, response := range controlPlane.ReceivedResponses(mocktunnelservice.ResponseMatchMatched) {
			if response.RequestID == ids[i] {
				return response
			}
		}
		t.Fatalf("missing response for %s", ids[i])
		return mocktunnelservice.ReceivedResponse{}
	}
	list := send(0, "harpoon", map[string]any{"name": "list_targets", "arguments": map[string]any{"categories": []string{"oauth"}}}, nil)
	if !forward {
		require.Equal(t, http.StatusBadRequest, list.ResponseCode)
		require.Contains(t, string(list.JSONResponse), "unsupported channel")
		require.Contains(t, proc.output.String(), "private-host-registration-disabled")
		require.Positive(t, metadataHits.Load(), "discovery itself must have succeeded")
	} else {
		var catalog struct {
			Targets []struct {
				Label, Category, Source string
				Tags                    []string
			} `json:"targets"`
		}
		require.NoError(t, json.Unmarshal(oauthForwardStructuredResult(t, list), &catalog))
		labels := make(map[string]string)
		groups := make(map[string]string)
		for _, target := range catalog.Targets {
			require.Equal(t, "oauth", target.Category)
			require.Equal(t, "oauth", target.Source)
			require.NotContains(t, target.Tags, "introspection-endpoint", "a different HTTPS port must not inherit forwarding approval")
			for _, role := range []string{"source-url", "authorization-server", "token-endpoint", "registration-endpoint", "revocation-endpoint"} {
				if slices.Contains(target.Tags, role) {
					labels[role] = target.Label
				}
			}
			if slices.Contains(target.Tags, "auth-server-metadata") && len(target.Tags) == 2 {
				labels["auth-server-metadata"] = target.Label
			}
			for _, tag := range target.Tags {
				if strings.HasPrefix(tag, "group=") {
					groups[target.Label] = tag
				}
			}
		}
		for _, role := range []string{"source-url", "authorization-server", "auth-server-metadata", "token-endpoint", "registration-endpoint", "revocation-endpoint"} {
			require.NotEmpty(t, labels[role], "missing discovered role %s", role)
		}
		group := groups[labels["authorization-server"]]
		require.NotEmpty(t, group)
		for _, role := range []string{"auth-server-metadata", "token-endpoint", "registration-endpoint", "revocation-endpoint"} {
			require.Equal(t, group, groups[labels[role]], "OAuth association lost for %s", role)
		}
		call := func(label, method string) map[string]any {
			return map[string]any{"name": "call_target", "arguments": map[string]any{"label": label, "method": method}}
		}
		prmd := oauthForwardHTTPResult(t, send(1, "harpoon", call(labels["source-url"], "GET"), nil))
		require.Equal(t, canonicalResource, prmd["resource"], "preserve the upstream resource audience")
		require.Equal(t, []any{"harpoon://" + labels["authorization-server"]}, prmd["authorization_servers"])
		metadata := oauthForwardHTTPResult(t, send(2, "harpoon", call(labels["auth-server-metadata"], "GET"), nil))
		require.Equal(t, "harpoon://"+labels["token-endpoint"], metadata["token_endpoint"])
		require.Equal(t, thirdOrigin.URL+"/introspect", metadata["introspection_endpoint"], "unapproved endpoint must not become a Harpoon URI")
		tokenCall := call(labels["token-endpoint"], "POST")
		tokenArgs := tokenCall["arguments"].(map[string]any)
		tokenArgs["headers"] = map[string]string{"Content-Type": "application/x-www-form-urlencoded"}
		tokenArgs["body"] = url.Values{"grant_type": {"authorization_code"}, "code": {"approved-code"}, "resource": {prmd["resource"].(string)}}.Encode()
		token := oauthForwardHTTPResult(t, send(3, "harpoon", tokenCall, nil))
		require.Equal(t, accessToken, token["access_token"])
		tool := send(4, "main", map[string]any{"name": "authenticated_tool", "arguments": map[string]any{}}, http.Header{"Authorization": {"Bearer " + token["access_token"].(string)}})
		require.Equal(t, http.StatusOK, tool.ResponseCode)
		require.Contains(t, string(tool.JSONResponse), "authenticated tool succeeded")
		deniedTarget := send(5, "harpoon", call("oauth-introspection-endpoint-0", "POST"), nil)
		deniedRedirect := send(6, "harpoon", call(labels["revocation-endpoint"], "POST"), nil)
		for _, denied := range []mocktunnelservice.ReceivedResponse{deniedTarget, deniedRedirect} {
			var result struct {
				Error  json.RawMessage `json:"error"`
				Result struct {
					IsError bool `json:"isError"`
				} `json:"result"`
			}
			require.NoError(t, json.Unmarshal(denied.JSONResponse, &result))
			require.True(t, len(result.Error) > 0 || result.Result.IsError, "unapproved call succeeded: %s", denied.JSONResponse)
		}
	}
	waitForRuntimeArtifactIdle(t, proc, controlPlane)
	require.NoError(t, proc.stop())
	// Commands and subprocess have completed, so the zero-request assertions
	// need no sleep or polling for something that must never happen.
	require.Zero(t, thirdOriginHits.Load())
	require.False(t, leakedMCPHeader.Load(), "MCP credentials leaked to the separately approved OAuth origin")
	if forward {
		require.EqualValues(t, 1, tokenHits.Load())
		require.EqualValues(t, 1, authenticatedCalls.Load())
	} else {
		require.Zero(t, tokenHits.Load())
		require.Zero(t, authenticatedCalls.Load())
	}
}

func oauthForwardRuntimeCommand(t *testing.T, id, channel, method string, params map[string]any, headers http.Header) json.RawMessage {
	t.Helper()
	if params == nil {
		params = make(map[string]any)
	}
	params["_meta"] = map[string]any{
		"io.modelcontextprotocol/protocolVersion":    "2026-07-28",
		"io.modelcontextprotocol/clientInfo":         map[string]string{"name": "oauth-forward-e2e", "version": "1"},
		"io.modelcontextprotocol/clientCapabilities": map[string]any{},
	}
	payload, err := json.Marshal(map[string]any{"jsonrpc": "2.0", "id": id, "method": method, "params": params})
	require.NoError(t, err)
	var command map[string]any
	require.NoError(t, json.Unmarshal(runtimeArtifactChannelCommand(id, channel, payload), &command))
	command["headers"] = headers
	encoded, err := json.Marshal(command)
	require.NoError(t, err)
	return encoded
}

func oauthForwardStructuredResult(t *testing.T, response mocktunnelservice.ReceivedResponse) json.RawMessage {
	t.Helper()
	require.Equal(t, http.StatusOK, response.ResponseCode)
	require.Equal(t, string(wiretypes.ResponsePayloadJSONRPC), response.ResponseType)
	var envelope struct {
		Error  json.RawMessage `json:"error"`
		Result struct {
			IsError           bool            `json:"isError"`
			StructuredContent json.RawMessage `json:"structuredContent"`
		} `json:"result"`
	}
	require.NoError(t, json.Unmarshal(response.JSONResponse, &envelope))
	require.Empty(t, envelope.Error, string(response.JSONResponse))
	require.False(t, envelope.Result.IsError, string(response.JSONResponse))
	require.NotEmpty(t, envelope.Result.StructuredContent)
	return envelope.Result.StructuredContent
}

func oauthForwardHTTPResult(t *testing.T, response mocktunnelservice.ReceivedResponse) map[string]any {
	t.Helper()
	var upstream struct {
		StatusCode int    `json:"status_code"`
		BodyBase64 string `json:"body_base64"`
	}
	require.NoError(t, json.Unmarshal(oauthForwardStructuredResult(t, response), &upstream))
	require.Equal(t, http.StatusOK, upstream.StatusCode)
	body, err := base64.StdEncoding.DecodeString(upstream.BodyBase64)
	require.NoError(t, err)
	var result map[string]any
	require.NoError(t, json.Unmarshal(body, &result))
	return result
}
