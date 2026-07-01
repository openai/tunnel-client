package main

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/openai/tunnel-client/pkg/codexplugin/session"
)

func TestAdminProfilesSetAndListJSON(t *testing.T) {
	t.Parallel()

	codexHome := t.TempDir()
	env := map[string]string{
		"TUNNEL_CLIENT_STATE_DIR": codexHome,
	}

	var stdout bytes.Buffer
	cmd := newAdminProfilesCommandWithRuntime(lookupEnvMap(env), &stdout, &bytes.Buffer{}, session.DefaultRuntime())
	cmd.SetArgs([]string{"set", "sandbox", "--admin-key", "env:OPENAI_ADMIN_KEY", "--control-plane-base-url", "https://api.openai.com", "--control-plane-url-path", "/chatgpttunnelgateway/dev/us", "--json"})
	require.NoError(t, cmd.Execute())
	require.Contains(t, stdout.String(), `"name": "sandbox"`)
	require.Contains(t, stdout.String(), `"control_plane_url_path": "/chatgpttunnelgateway/dev/us"`)

	stdout.Reset()
	cmd = newAdminProfilesCommandWithRuntime(lookupEnvMap(env), &stdout, &bytes.Buffer{}, session.DefaultRuntime())
	cmd.SetArgs([]string{"list", "--json"})
	require.NoError(t, cmd.Execute())
	require.Contains(t, stdout.String(), `"active_profile": "sandbox"`)

	stdout.Reset()
	cmd = newAdminProfilesCommandWithRuntime(lookupEnvMap(env), &stdout, &bytes.Buffer{}, session.DefaultRuntime())
	cmd.SetArgs([]string{"delete", "sandbox", "--json"})
	require.NoError(t, cmd.Execute())
	require.Contains(t, stdout.String(), `"deleted_profile": "sandbox"`)
}

func TestRuntimesCreateConnectStatusStopJSON(t *testing.T) {
	t.Parallel()

	codexHome := t.TempDir()
	xdgHome := t.TempDir()
	tunnels := map[string]map[string]any{}

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch {
		case r.Method == http.MethodPost && r.URL.Path == "/chatgpttunnelgateway/dev/us/v1/tunnels":
			payload := map[string]any{}
			require.NoError(t, json.NewDecoder(r.Body).Decode(&payload))
			tunnel := map[string]any{
				"id":               "tunnel_0123456789abcdef0123456789abcd",
				"name":             payload["name"],
				"description":      payload["description"],
				"organization_ids": payload["organization_ids"],
				"workspace_ids":    payload["workspace_ids"],
				"tenant_ids":       []string{},
			}
			tunnels[tunnel["id"].(string)] = tunnel
			require.NoError(t, json.NewEncoder(w).Encode(tunnel))
		case r.Method == http.MethodGet && r.URL.Path == "/chatgpttunnelgateway/dev/us/v1/tunnels":
			require.NoError(t, json.NewEncoder(w).Encode(map[string]any{"tunnels": []map[string]any{}}))
		case r.Method == http.MethodGet && r.URL.Path == "/chatgpttunnelgateway/dev/us/v1/tunnels/tunnel_0123456789abcdef0123456789abcd":
			require.NoError(t, json.NewEncoder(w).Encode(tunnels["tunnel_0123456789abcdef0123456789abcd"]))
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()

	healthServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/healthz", "/readyz":
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte("ok"))
		default:
			http.NotFound(w, r)
		}
	}))
	defer healthServer.Close()

	env := map[string]string{
		"TUNNEL_CLIENT_STATE_DIR": codexHome,
		"XDG_CONFIG_HOME":         xdgHome,
		"OPENAI_ADMIN_KEY":        "admin-key",
		"CONTROL_PLANE_API_KEY":   "runtime-key",
		"CONTROL_PLANE_BASE_URL":  server.URL,
		"CONTROL_PLANE_URL_PATH":  "/chatgpttunnelgateway/dev/us",
	}
	activeTmuxSessions := map[string]bool{}
	runtime := session.Runtime{
		Run: func(args []string, env map[string]string) (session.CompletedProcess, error) {
			if len(args) >= 2 && args[0] == "tmux" && args[1] == "-V" {
				return session.CompletedProcess{ReturnCode: 0, Stdout: "tmux 3.4\n"}, nil
			}
			if len(args) >= 4 && args[0] == "tmux" && args[1] == "has-session" {
				name := args[3][1:]
				if activeTmuxSessions[name] {
					return session.CompletedProcess{ReturnCode: 0}, nil
				}
				return session.CompletedProcess{ReturnCode: 1}, nil
			}
			if len(args) >= 6 && args[0] == "tmux" && args[1] == "new-session" {
				name := args[len(args)-2]
				activeTmuxSessions[name] = true
				healthPath := filepath.Join(codexHome, "health", "docs-mcp.url")
				require.NoError(t, os.MkdirAll(filepath.Dir(healthPath), 0o755))
				require.NoError(t, os.WriteFile(healthPath, []byte(healthServer.URL+"/healthz"), 0o600))
				return session.CompletedProcess{ReturnCode: 0}, nil
			}
			if len(args) >= 4 && args[0] == "tmux" && args[1] == "kill-session" {
				name := args[3][1:]
				delete(activeTmuxSessions, name)
				return session.CompletedProcess{ReturnCode: 0}, nil
			}
			return session.CompletedProcess{}, nil
		},
		Start: func(args []string, env map[string]string, logPath string) (session.Process, error) {
			t.Fatalf("unexpected process start fallback: %v", args)
			return nil, nil
		},
	}

	var stdout bytes.Buffer
	var stderr bytes.Buffer
	cmd := newRuntimesCommandWithRuntime(lookupEnvMap(env), &stdout, &stderr, runtime)
	cmd.SetArgs([]string{"create", "--alias", "docs-mcp", "--organization-id", "org_123", "--json"})
	require.NoError(t, cmd.Execute())
	require.Contains(t, stdout.String(), `"alias": "docs-mcp"`)

	stdout.Reset()
	stderr.Reset()
	cmd = newRuntimesCommandWithRuntime(lookupEnvMap(env), &stdout, &stderr, runtime)
	cmd.SetArgs([]string{"connect", "--alias", "docs-mcp", "--organization-id", "org_123", "--mcp-server-url", "http://127.0.0.1:3001/mcp", "--json"})
	require.NoError(t, cmd.Execute())
	var connectPayload map[string]any
	require.NoError(t, json.Unmarshal(stdout.Bytes(), &connectPayload))
	require.Equal(t, true, connectPayload["healthy"])
	require.Equal(t, "ready", connectPayload["runtime_state"])
	require.Equal(t, true, connectPayload["process_running"])
	_, hasPID := connectPayload["pid"]
	require.False(t, hasPID)
	profilePath := connectPayload["profile_path"].(string)
	profileContents, err := os.ReadFile(profilePath)
	require.NoError(t, err)
	require.Contains(t, string(profileContents), `"url_path": "/chatgpttunnelgateway/dev/us"`)

	stdout.Reset()
	stderr.Reset()
	cmd = newRuntimesCommandWithRuntime(lookupEnvMap(env), &stdout, &stderr, runtime)
	cmd.SetArgs([]string{"status", "docs-mcp", "--json"})
	require.NoError(t, cmd.Execute())
	var statusPayload map[string]any
	require.NoError(t, json.Unmarshal(stdout.Bytes(), &statusPayload))
	require.Equal(t, "docs-mcp", statusPayload["alias"])
	require.Equal(t, true, statusPayload["remote_lookup_attempted"])
	require.Equal(t, true, statusPayload["process_running"])
	processPayload, ok := statusPayload["process"].(map[string]any)
	require.True(t, ok)
	_, hasProcessPID := processPayload["pid"]
	require.False(t, hasProcessPID)

	stdout.Reset()
	stderr.Reset()
	cmd = newRuntimesCommandWithRuntime(lookupEnvMap(env), &stdout, &stderr, runtime)
	cmd.SetArgs([]string{"stop", "docs-mcp", "--json"})
	require.NoError(t, cmd.Execute())
	require.Contains(t, stdout.String(), `"stopped": true`)

	stdout.Reset()
	stderr.Reset()
	cmd = newRuntimesCommandWithRuntime(lookupEnvMap(env), &stdout, &stderr, runtime)
	cmd.SetArgs([]string{"rm", "docs-mcp", "--json"})
	require.NoError(t, cmd.Execute())
	require.Contains(t, stdout.String(), `"removed": true`)
}

func TestRuntimesListRejectsMixedRemoteScopeFamilies(t *testing.T) {
	t.Parallel()

	var stdout bytes.Buffer
	var stderr bytes.Buffer
	cmd := newRuntimesCommandWithRuntime(lookupEnvMap(map[string]string{}), &stdout, &stderr, session.DefaultRuntime())
	cmd.SetArgs([]string{"list", "--organization-id", "org_123", "--workspace-id", "ws_123"})

	err := cmd.Execute()
	require.EqualError(t, err, "runtimes list accepts exactly one remote scope family: --organization-id, --workspace-id, or --tenant-id")
}

func TestRuntimesCreateRejectsMixedRemoteScopeFamilies(t *testing.T) {
	t.Parallel()

	var stdout bytes.Buffer
	var stderr bytes.Buffer
	cmd := newRuntimesCommandWithRuntime(lookupEnvMap(map[string]string{}), &stdout, &stderr, session.DefaultRuntime())
	cmd.SetArgs([]string{"create", "--alias", "docs-mcp", "--organization-id", "org_123", "--workspace-id", "ws_123"})

	err := cmd.Execute()
	require.EqualError(t, err, "runtimes create accepts exactly one remote scope family: --organization-id or --workspace-id")
}

func TestRuntimesConnectRejectsMixedRemoteScopeFamilies(t *testing.T) {
	t.Parallel()

	var stdout bytes.Buffer
	var stderr bytes.Buffer
	cmd := newRuntimesCommandWithRuntime(lookupEnvMap(map[string]string{}), &stdout, &stderr, session.DefaultRuntime())
	cmd.SetArgs([]string{"connect", "--alias", "docs-mcp", "--organization-id", "org_123", "--workspace-id", "ws_123", "--mcp-server-url", "http://127.0.0.1:3001/mcp"})

	err := cmd.Execute()
	require.EqualError(t, err, "runtimes connect accepts exactly one remote scope family: --organization-id or --workspace-id")
}

func TestRuntimesConnectHelpExplainsManagedSupervision(t *testing.T) {
	t.Parallel()

	var stdout bytes.Buffer
	var stderr bytes.Buffer
	cmd := newRuntimesCommandWithRuntime(lookupEnvMap(map[string]string{}), &stdout, &stderr, session.DefaultRuntime())
	cmd.SetArgs([]string{"connect", "--help"})

	require.NoError(t, cmd.Execute())
	output := stdout.String()
	require.Contains(t, output, "managed local runtime supervision")
	require.Contains(t, output, "instead of nohup or disown")
	require.Contains(t, output, "tunnel-client runtimes status <alias>")
}

func TestRuntimesListRejectsMultipleOrganizationIDs(t *testing.T) {
	t.Parallel()

	var stdout bytes.Buffer
	var stderr bytes.Buffer
	cmd := newRuntimesCommandWithRuntime(lookupEnvMap(map[string]string{}), &stdout, &stderr, session.DefaultRuntime())
	cmd.SetArgs([]string{"list", "--organization-id", "org_123", "--organization-id", "org_456"})

	err := cmd.Execute()
	require.EqualError(t, err, "runtimes list accepts at most one --organization-id for remote listing")
}

func lookupEnvMap(env map[string]string) func(string) (string, bool) {
	return func(key string) (string, bool) {
		value, ok := env[key]
		return value, ok
	}
}
