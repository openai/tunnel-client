package main

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestRootCommandWithNoArgsPrintsHelp(t *testing.T) {
	t.Parallel()

	var stdout bytes.Buffer
	root := newRootCommand(func(string) (string, bool) { return "", false }, &stdout, io.Discard)

	root.SetArgs([]string{})

	require.NoError(t, root.Execute())
	output := stdout.String()
	require.Contains(t, output, "Commands:")
	require.Contains(t, output, "run")
	require.Contains(t, output, "connect a local or private MCP server")
	require.Contains(t, output, "codex")
	require.Contains(t, output, "sessions")
	require.Contains(t, output, "admin-profiles")
}

func TestRootHelpListsSubcommands(t *testing.T) {
	t.Parallel()

	var stdout bytes.Buffer
	root := newRootCommand(func(string) (string, bool) { return "", false }, &stdout, io.Discard)

	root.SetArgs([]string{"--help"})

	require.NoError(t, root.Execute())
	output := stdout.String()
	require.Contains(t, output, "Commands:")
	require.Contains(t, output, "run")
	require.Contains(t, output, "dev")
	require.Contains(t, output, "codex")
	require.Contains(t, output, "sessions")
	require.Contains(t, output, "admin-profiles")
	require.NotContains(t, output, "control-plane.base-url")
}

func TestAdminJSONErrorEnvelopeIncludesRequestID(t *testing.T) {
	t.Parallel()

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		require.Equal(t, "/v1/tunnels/tunnel_missing", r.URL.Path)
		w.Header().Set("X-Request-Id", "req_test_json_123")
		w.WriteHeader(http.StatusNotFound)
		_, _ = w.Write([]byte(`{"error":{"message":"missing"}}`))
	}))
	t.Cleanup(server.Close)

	stdout, stderr, err := executeCommand(t, map[string]string{
		"OPENAI_ADMIN_KEY": "admin-key",
	}, "admin", "tunnels", "get", "tunnel_missing", "--control-plane.base-url", server.URL, "--json")

	require.Error(t, err)
	require.Empty(t, stderr)
	require.Equal(t, 1, exitCode(err))

	var payload struct {
		Error struct {
			Message    string `json:"message"`
			RequestID  string `json:"request_id"`
			StatusCode int    `json:"status_code"`
			Method     string `json:"method"`
			Path       string `json:"path"`
		} `json:"error"`
	}
	require.NoError(t, json.Unmarshal([]byte(stdout), &payload))
	require.Contains(t, payload.Error.Message, "req_test_json_123")
	require.Equal(t, "req_test_json_123", payload.Error.RequestID)
	require.Equal(t, http.StatusNotFound, payload.Error.StatusCode)
	require.Equal(t, http.MethodGet, payload.Error.Method)
	require.Equal(t, "/v1/tunnels/tunnel_missing", payload.Error.Path)
}
