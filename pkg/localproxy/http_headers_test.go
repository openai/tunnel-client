package localproxy

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/openai/tunnel-client/pkg/controlplane/wiretypes"
	"github.com/openai/tunnel-client/pkg/types"
)

func TestHeaderForwardingRejectsMalformedFields(t *testing.T) {
	for _, kind := range []string{"request", "mcp", "oauth"} {
		t.Run(kind, func(t *testing.T) {
			input := http.Header{
				"Bad Header":    {"bad"},
				"Bad:Header":    {"bad"},
				"Bad\r\nHeader": {"bad"},
				"":              {"bad"},
				"X-Mixed":       {"first", "bad\r\nInjected: yes", "bad\r", "bad\n", "bad\x00", "bad\x1f", "bad\x7f", "last"},
				"X-Bytes":       {"value\t\x80\xff"},
				"X-Empty":       {""},
				"X-Case":        {"first"},
				"x-case":        {"second"},
				"Authorization": {"Bearer connector-token"},
				"Connection":    {"X-Hop"},
				"X-Hop":         {"blocked"},
			}
			var got http.Header
			switch kind {
			case "request":
				got = sanitizeForwardableRequestHeaders(input)
			case "mcp":
				got = make(http.Header)
				appendMCPResponseHeaders(got, input, "http://proxy.test/v1/mcp/tunnel_test")
			case "oauth":
				recorder := httptest.NewRecorder()
				renderOAuthDiscoveryResponse(recorder, wiretypes.TunnelResponsePayload{ResponseHeaders: input}, "http://proxy.test/v1/mcp/tunnel_test")
				got = recorder.Header()
			}
			for _, name := range []string{"Bad Header", "Bad:Header", "Bad\r\nHeader", "", "Connection", "X-Hop"} {
				require.NotContains(t, got, name)
			}
			require.Equal(t, []string{"first", "last"}, got.Values("X-Mixed"))
			require.Equal(t, "value\t\x80\xff", got.Get("X-Bytes"))
			require.ElementsMatch(t, []string{"first", "second"}, got.Values("X-Case"))
			require.Equal(t, "Bearer connector-token", got.Get("Authorization"))
			if kind == "request" {
				require.NotContains(t, got, "X-Empty")
			} else {
				require.Equal(t, []string{""}, got.Values("X-Empty"))
			}
		})
	}
}

func TestResponseHeaderValidationOnWire(t *testing.T) {
	const publicURL = "http://proxy.test/v1/mcp/tunnel_test"
	const metadataURL = "http://proxy.test/.well-known/oauth-protected-resource/v1/mcp/tunnel_test"
	validChallenges := []string{
		`Bearer realm="connector", resource_metadata="https://upstream.example/metadata"`,
		`Bearer resource_metadata=https://upstream.example/metadata, scope="mcp:tools"`,
	}
	payload := wiretypes.TunnelResponsePayload{
		JSONResponse: json.RawMessage(`{"ok":true}`),
		ResponseHeaders: http.Header{
			"X-Bad":            {"value\r\nX-Injected: yes"},
			"Bad Header":       {"bad"},
			"X-Repeated":       {"first", "second"},
			"Set-Cookie":       {"one=1", "two=2"},
			"Connection":       {"X-Hop"},
			"X-Hop":            {"blocked"},
			"WWW-Authenticate": append(append([]string{}, validChallenges...), "Bearer resource_metadata=\"https://upstream.example/\r\nX-Injected: yes\""),
		},
	}
	encoded, err := json.Marshal(payload)
	require.NoError(t, err)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var response wiretypes.TunnelResponsePayload
		if err := json.NewDecoder(r.Body).Decode(&response); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		switch r.URL.Path {
		case "/mcp":
			renderMCPResponse(w, response, publicURL)
		case "/sse":
			(&localServer{tunnelID: types.TunnelID("tunnel_test")}).renderMCPEventStream(w, r, nil, response, nil)
		case "/oauth":
			renderOAuthDiscoveryResponse(w, response, publicURL)
		}
	}))
	t.Cleanup(server.Close)
	for _, kind := range []string{"mcp", "sse", "oauth"} {
		t.Run(kind, func(t *testing.T) {
			request, err := http.NewRequest(http.MethodPost, server.URL+"/"+kind, bytes.NewReader(encoded))
			require.NoError(t, err)
			request.Host = "proxy.test"
			request.Header.Set("Content-Type", "application/json")
			response, err := server.Client().Do(request)
			require.NoError(t, err)
			defer func() { _ = response.Body.Close() }()
			body, err := io.ReadAll(response.Body)
			require.NoError(t, err)
			require.Equal(t, http.StatusOK, response.StatusCode)
			for _, name := range []string{"X-Bad", "X-Injected", "Bad Header", "X-Hop", "Connection"} {
				require.Empty(t, response.Header.Values(name))
			}
			require.Equal(t, []string{"first", "second"}, response.Header.Values("X-Repeated"))
			require.Equal(t, []string{"one=1", "two=2"}, response.Header.Values("Set-Cookie"))
			if kind == "oauth" {
				require.Equal(t, validChallenges, response.Header.Values("WWW-Authenticate"))
				require.JSONEq(t, `{"ok":true,"resource":"`+publicURL+`"}`, string(body))
			} else {
				require.Equal(t, []string{
					`Bearer realm="connector", resource_metadata="` + metadataURL + `"`,
					`Bearer resource_metadata=` + metadataURL + `, scope="mcp:tools"`,
				}, response.Header.Values("WWW-Authenticate"))
				if kind == "sse" {
					require.Equal(t, "text/event-stream", response.Header.Get("Content-Type"))
					require.Equal(t, "data: {\"ok\":true}\n\n", string(body))
				} else {
					require.JSONEq(t, `{"ok":true}`, string(body))
				}
			}
		})
	}
}
