package main

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"syscall"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/openai/tunnel-client/pkg/codexplugin/session"
)

type runtimesTestProcess struct {
	cmd  *exec.Cmd
	done <-chan struct{}
}

func (p *runtimesTestProcess) PID() int {
	return p.cmd.Process.Pid
}

func (p *runtimesTestProcess) Poll() *int {
	select {
	case <-p.done:
		exitCode := p.cmd.ProcessState.ExitCode()
		return &exitCode
	default:
		return nil
	}
}

func startRuntimesTestProcess(t *testing.T, healthPath string, healthURL string) session.Process {
	t.Helper()
	cmd := exec.Command(os.Args[0], "-test.run=^TestRuntimesManagedProcessHelper$")
	cmd.Env = append(
		os.Environ(),
		"TUNNEL_CLIENT_RUNTIME_TEST_HELPER=1",
		"TUNNEL_CLIENT_RUNTIME_TEST_HEALTH_PATH="+healthPath,
		"TUNNEL_CLIENT_RUNTIME_TEST_HEALTH_URL="+healthURL,
	)
	require.NoError(t, cmd.Start())
	done := make(chan struct{})
	go func() {
		_ = cmd.Wait()
		close(done)
	}()
	t.Cleanup(func() {
		if cmd.Process != nil {
			_ = cmd.Process.Kill()
		}
		select {
		case <-done:
		case <-time.After(time.Second):
			t.Error("timed out waiting for managed process test helper to exit")
		}
	})
	return &runtimesTestProcess{cmd: cmd, done: done}
}

func TestRuntimesManagedProcessHelper(t *testing.T) {
	if os.Getenv("TUNNEL_CLIENT_RUNTIME_TEST_HELPER") != "1" {
		return
	}
	signals := make(chan os.Signal, 1)
	signal.Notify(signals, os.Interrupt, syscall.SIGTERM)
	defer signal.Stop(signals)
	healthPath := os.Getenv("TUNNEL_CLIENT_RUNTIME_TEST_HEALTH_PATH")
	healthURL := os.Getenv("TUNNEL_CLIENT_RUNTIME_TEST_HEALTH_URL")
	require.NotEmpty(t, healthPath)
	require.NotEmpty(t, healthURL)
	require.NoError(t, os.MkdirAll(filepath.Dir(healthPath), 0o755))
	require.NoError(t, os.WriteFile(healthPath, []byte(healthURL), 0o600))
	<-signals
}

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
	runtime := session.Runtime{
		Run: func(args []string, env map[string]string) (session.CompletedProcess, error) {
			t.Fatalf("unexpected tmux command: %v", args)
			return session.CompletedProcess{}, nil
		},
		Start: func(args []string, env map[string]string, logPath string) (session.Process, error) {
			require.Equal(t, "docs-mcp", args[5])
			healthPath := filepath.Join(codexHome, "health", "docs-mcp.url")
			return startRuntimesTestProcess(t, healthPath, healthServer.URL+"/healthz"), nil
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
	require.Equal(t, "process", connectPayload["mode"])
	_, hasPID := connectPayload["pid"]
	require.True(t, hasPID)
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
	require.True(t, hasProcessPID)

	stdout.Reset()
	stderr.Reset()
	cmd = newRuntimesCommandWithRuntime(lookupEnvMap(env), &stdout, &stderr, runtime)
	cmd.SetArgs([]string{"stop", "docs-mcp", "--json"})
	err = cmd.Execute()
	require.NoErrorf(t, err, "stdout=%s stderr=%s", stdout.String(), stderr.String())
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

func TestValidateTunnelClientBinOverrideAcceptsCurrentExecutable(t *testing.T) {
	t.Parallel()

	require.NoError(t, validateTunnelClientBinOverride(currentExecutablePath()))
}

func TestRuntimesConnectRejectsAlternateTunnelClientBin(t *testing.T) {
	t.Parallel()

	alternate := filepath.Join(t.TempDir(), "alternate-tunnel-client")
	require.NoError(t, os.WriteFile(alternate, []byte("alternate"), 0o700))

	var stdout bytes.Buffer
	var stderr bytes.Buffer
	cmd := newRuntimesCommandWithRuntime(lookupEnvMap(map[string]string{}), &stdout, &stderr, session.DefaultRuntime())
	cmd.SetArgs([]string{"connect", "--alias", "docs-mcp", "--tunnel-client-bin", alternate})

	err := cmd.Execute()
	require.EqualError(t, err, "--tunnel-client-bin only accepts the current tunnel-client executable")
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
