package codexplugin

import (
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/openai/tunnel-client/pkg/codexplugin/session"
	pluginstate "github.com/openai/tunnel-client/pkg/codexplugin/state"
	adminapi "github.com/openai/tunnel-client/pkg/controlplane/admin"
)

func TestStatusReconcilesStaleAliasHealthURLWithLiveRuntimeAndPollHealth(t *testing.T) {
	t.Parallel()

	root := pluginstate.Root{Path: t.TempDir()}
	require.NoError(t, pluginstate.EnsureDirs(root))
	profileDir := filepath.Join(t.TempDir(), "profiles")
	profilePath := filepath.Join(profileDir, "docs-mcp.yaml")
	require.NoError(t, os.MkdirAll(profileDir, 0o755))
	require.NoError(t, os.WriteFile(profilePath, []byte("config_version: 1\n"), 0o600))

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/healthz", "/readyz":
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte("ok"))
		case "/api/status":
			w.Header().Set("content-type", "application/json")
			_, _ = w.Write([]byte(`{"control_plane_tunnel_id":"tunnel_123","control_plane_route":{"kind":"control_plane","route_mode":"proxy"}}`))
		case "/api/system":
			w.Header().Set("content-type", "application/json")
			_, _ = w.Write([]byte(`{"proxy_health":[{"health_state":"failed","route":{"kind":"control_plane","route_mode":"proxy","proxy_url":"http://127.0.0.1:9"},"last_check":"2026-05-08T00:00:00Z"}]}`))
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()

	staleHealthURLFile := session.ProfileHealthURLFile("docs-mcp", root)
	require.NoError(t, os.WriteFile(staleHealthURLFile, []byte("http://127.0.0.1:1/healthz\n"), 0o600))
	require.NoError(t, os.WriteFile(filepath.Join(root.Path, "health", "live-docs-mcp.url"), []byte(server.URL+"/healthz\n"), 0o600))
	require.NoError(t, pluginstate.SaveAliases(root, map[string]pluginstate.AliasRecord{
		"docs-mcp": {
			Alias:         "docs-mcp",
			TunnelID:      "tunnel_123",
			ConfigPath:    profilePath,
			ProfileName:   "docs-mcp",
			ProfileDir:    profileDir,
			ProfilePath:   profilePath,
			HealthURLFile: staleHealthURLFile,
		},
	}))

	manager := NewManager(testLookupEnv(map[string]string{
		"TUNNEL_CLIENT_STATE_DIR":   root.Path,
		"TUNNEL_CLIENT_PROFILE_DIR": profileDir,
		"HOME":                      t.TempDir(),
	}), session.Runtime{})
	payload, err := manager.Status(AliasOptions{Alias: "docs-mcp"})
	require.NoError(t, err)

	require.Equal(t, true, payload["healthy"])
	require.Equal(t, true, payload["ready"])
	require.Equal(t, "failed", payload["control_plane_poll_health"].(map[string]any)["state"])
	local := payload["local"].(map[string]any)
	require.Equal(t, true, local["live_admin_ui"].(map[string]any)["found"])
	require.Equal(t, server.URL, local["live_admin_ui"].(map[string]any)["base_url"])
	require.Contains(t, strings.Join(toStringSlice(local["issues"]), "\n"), "recorded health URL looks stale")
	require.Contains(t, strings.Join(toStringSlice(local["issues"]), "\n"), "control-plane poll route health is failed")
	requireRepairAction(t, payload["repair_actions"], "refresh_stale_health_url")
	requireRepairAction(t, payload["repair_actions"], "repair_control_plane_proxy")
}

func TestCleanupInventoryClassifiesAndAppliesOnlyStaleAliases(t *testing.T) {
	t.Parallel()

	root := pluginstate.Root{Path: t.TempDir()}
	require.NoError(t, pluginstate.EnsureDirs(root))
	profileDir := filepath.Join(t.TempDir(), "profiles")
	profilePath := filepath.Join(profileDir, "valid.yaml")
	require.NoError(t, os.MkdirAll(profileDir, 0o755))
	require.NoError(t, os.WriteFile(profilePath, []byte("config_version: 1\n"), 0o600))
	require.NoError(t, pluginstate.SaveAliases(root, map[string]pluginstate.AliasRecord{
		"valid": {
			Alias:       "valid",
			TunnelID:    "tunnel_valid",
			ProfileName: "valid",
			ProfileDir:  profileDir,
			ProfilePath: profilePath,
		},
		"stale": {
			Alias:    "stale",
			TunnelID: "tunnel_stale",
		},
		"missing": {
			Alias:       "missing",
			TunnelID:    "tunnel_missing",
			ProfileName: "missing",
			ProfileDir:  profileDir,
			ProfilePath: filepath.Join(profileDir, "missing.yaml"),
		},
	}))

	manager := NewManager(testLookupEnv(map[string]string{
		"TUNNEL_CLIENT_STATE_DIR":   root.Path,
		"TUNNEL_CLIENT_PROFILE_DIR": profileDir,
		"HOME":                      t.TempDir(),
	}), session.Runtime{})
	payload, err := manager.CleanupInventory(CleanupOptions{})
	require.NoError(t, err)
	require.Equal(t, "valid_profile", inventoryClassForAlias(payload, "valid"))
	require.Equal(t, "missing_profile", inventoryClassForAlias(payload, "missing"))
	require.Equal(t, "stale_alias", inventoryClassForAlias(payload, "stale"))

	payload, err = manager.CleanupInventory(CleanupOptions{Apply: true})
	require.NoError(t, err)
	require.Contains(t, payload["removed"], "stale")
	aliases, err := pluginstate.LoadAliases(root)
	require.NoError(t, err)
	require.Contains(t, aliases, "valid")
	require.Contains(t, aliases, "missing")
	require.NotContains(t, aliases, "stale")
}

func TestConnectRejectsLiteralRuntimeSecretBeforePersistence(t *testing.T) {
	t.Parallel()

	root := pluginstate.Root{Path: t.TempDir()}
	profileDir := filepath.Join(t.TempDir(), "profiles")
	secret := "sk-proj-runtime-secret"
	manager := NewManager(testLookupEnv(map[string]string{
		"TUNNEL_CLIENT_STATE_DIR":   root.Path,
		"TUNNEL_CLIENT_PROFILE_DIR": profileDir,
		"HOME":                      t.TempDir(),
	}), session.Runtime{})

	_, err := manager.Connect(ConnectOptions{
		CreateOptions: CreateOptions{
			Alias: "docs-mcp",
		},
		MCPCommand:    "python server.py",
		RuntimeAPIKey: secret,
	})
	require.Error(t, err)
	require.Contains(t, err.Error(), "runtime api_key must be an env:NAME or file:/path reference")
	requireNoFileContains(t, root.Path, secret)
	requireNoFileContains(t, profileDir, secret)
}

func TestConcurrentConnectSameAliasStartsOnlyOneProcess(t *testing.T) {
	t.Parallel()

	root := pluginstate.Root{Path: t.TempDir()}
	profileDir := filepath.Join(t.TempDir(), "profiles")
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

	identity := session.ProcessIdentity{
		StartTime:  "test-process-start",
		Executable: "/test/tunnel-client",
	}
	var starts atomic.Int32
	firstStarted := make(chan struct{})
	releaseStarts := make(chan struct{})
	var firstStartedOnce sync.Once
	var releaseOnce sync.Once
	release := func() {
		releaseOnce.Do(func() { close(releaseStarts) })
	}
	defer release()

	runtime := session.Runtime{
		Run: func(args []string, env map[string]string) (session.CompletedProcess, error) {
			return session.CompletedProcess{}, nil
		},
		Start: func(args []string, env map[string]string, logPath string) (session.Process, error) {
			starts.Add(1)
			if err := os.WriteFile(
				session.ProfileHealthURLFile("docs-mcp", root),
				[]byte(healthServer.URL+"/healthz\n"),
				0o600,
			); err != nil {
				return nil, err
			}
			firstStartedOnce.Do(func() { close(firstStarted) })
			<-releaseStarts
			return managerTestProcess{pid: os.Getpid()}, nil
		},
		InspectProcess: func(pid int) (session.ProcessIdentity, error) {
			return identity, nil
		},
	}
	lookupEnv := testLookupEnv(map[string]string{
		"TUNNEL_CLIENT_STATE_DIR":   root.Path,
		"TUNNEL_CLIENT_PROFILE_DIR": profileDir,
		"OPENAI_ADMIN_KEY":          "admin-key",
		"CONTROL_PLANE_API_KEY":     "runtime-key",
		"HOME":                      t.TempDir(),
	})
	opts := ConnectOptions{
		CreateOptions: CreateOptions{
			Alias:               "docs-mcp",
			ControlPlaneBaseURL: healthServer.URL,
		},
		TunnelID:   "tunnel_0123456789abcdefghijklmnopqrstuv",
		MCPCommand: "python server.py",
	}
	type connectResult struct {
		payload map[string]any
		err     error
	}
	results := make(chan connectResult, 2)
	connect := func() {
		payload, err := NewManager(lookupEnv, runtime).Connect(opts)
		results <- connectResult{payload: payload, err: err}
	}

	go connect()
	select {
	case <-firstStarted:
	case <-time.After(2 * time.Second):
		t.Fatal("first connect did not reach process start")
	}
	go connect()

	require.Never(t, func() bool { return starts.Load() > 1 }, 250*time.Millisecond, 10*time.Millisecond)
	release()

	launched := 0
	reused := 0
	for i := 0; i < 2; i++ {
		select {
		case result := <-results:
			require.NoError(t, result.err)
			if result.payload["launched"] == true {
				launched++
			}
			if result.payload["already_running"] == true {
				reused++
			}
		case <-time.After(3 * time.Second):
			t.Fatal("concurrent connect did not finish")
		}
	}
	require.EqualValues(t, 1, starts.Load())
	require.Equal(t, 1, launched)
	require.Equal(t, 1, reused)

	processes, err := pluginstate.LoadProcesses(root)
	require.NoError(t, err)
	require.Equal(t, os.Getpid(), processes["docs-mcp"].PID)
	require.Equal(t, identity.StartTime, processes["docs-mcp"].PIDStartTime)
	require.Equal(t, identity.Executable, processes["docs-mcp"].PIDExecutable)
}

func TestConnectCleansUpNewProcessWhenProcessStateSaveFails(t *testing.T) {
	t.Parallel()

	root := pluginstate.Root{Path: t.TempDir()}
	profileDir := filepath.Join(t.TempDir(), "profiles")
	require.NoError(t, pluginstate.EnsureDirs(root))
	// SaveProcesses writes through this deterministic temp path. A directory
	// makes the final persistence step fail after the detached child starts.
	require.NoError(t, os.Mkdir(pluginstate.ProcessesPath(root)+".tmp", 0o700))
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

	identity := session.ProcessIdentity{StartTime: "new-process", Executable: "/test/tunnel-client"}
	terminated := false
	waited := false
	runtime := session.Runtime{
		Run: func(args []string, env map[string]string) (session.CompletedProcess, error) {
			return session.CompletedProcess{}, nil
		},
		Start: func(args []string, env map[string]string, logPath string) (session.Process, error) {
			require.NoError(t, os.WriteFile(session.ProfileHealthURLFile("docs-mcp", root), []byte(healthServer.URL+"/healthz\n"), 0o600))
			return managerTestProcess{pid: os.Getpid()}, nil
		},
		InspectProcess: func(pid int) (session.ProcessIdentity, error) {
			return identity, nil
		},
		Terminate: func(pid int) error {
			terminated = true
			return nil
		},
		WaitForExit: func(pid int, expected session.ProcessIdentity) bool {
			require.Equal(t, identity, expected)
			waited = true
			return true
		},
	}
	lookupEnv := testLookupEnv(map[string]string{
		"TUNNEL_CLIENT_STATE_DIR":   root.Path,
		"TUNNEL_CLIENT_PROFILE_DIR": profileDir,
		"OPENAI_ADMIN_KEY":          "admin-key",
		"CONTROL_PLANE_API_KEY":     "runtime-key",
		"HOME":                      t.TempDir(),
	})

	_, err := NewManager(lookupEnv, runtime).Connect(ConnectOptions{
		CreateOptions: CreateOptions{
			Alias:               "docs-mcp",
			ControlPlaneBaseURL: healthServer.URL,
		},
		TunnelID:   "tunnel_0123456789abcdefghijklmnopqrstuv",
		MCPCommand: "python server.py",
	})
	require.Error(t, err)
	require.Contains(t, err.Error(), "processes.yaml.tmp")
	require.True(t, terminated)
	require.True(t, waited)
}

func TestConnectDoesNotPersistProcessWhenIdentityCaptureSafelyAborts(t *testing.T) {
	t.Parallel()

	root := pluginstate.Root{Path: t.TempDir()}
	profileDir := filepath.Join(t.TempDir(), "profiles")
	healthServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.NotFound(w, r)
	}))
	defer healthServer.Close()
	aborted := false
	runtime := session.Runtime{
		Run: func(args []string, env map[string]string) (session.CompletedProcess, error) {
			return session.CompletedProcess{}, nil
		},
		Start: func(args []string, env map[string]string, logPath string) (session.Process, error) {
			return managerTestProcess{pid: os.Getpid(), abort: func() error {
				aborted = true
				return nil
			}}, nil
		},
		InspectProcess: func(pid int) (session.ProcessIdentity, error) {
			return session.ProcessIdentity{}, errors.New("identity unavailable")
		},
	}
	lookupEnv := testLookupEnv(map[string]string{
		"TUNNEL_CLIENT_STATE_DIR":   root.Path,
		"TUNNEL_CLIENT_PROFILE_DIR": profileDir,
		"OPENAI_ADMIN_KEY":          "admin-key",
		"CONTROL_PLANE_API_KEY":     "runtime-key",
		"HOME":                      t.TempDir(),
	})

	_, err := NewManager(lookupEnv, runtime).Connect(ConnectOptions{
		CreateOptions: CreateOptions{
			Alias:               "docs-mcp",
			ControlPlaneBaseURL: healthServer.URL,
		},
		TunnelID:   "tunnel_0123456789abcdefghijklmnopqrstuv",
		MCPCommand: "python server.py",
	})
	require.Error(t, err)
	require.Contains(t, err.Error(), "safely aborted launched process before state persistence")
	require.True(t, aborted)
	processes, loadErr := pluginstate.LoadProcesses(root)
	require.NoError(t, loadErr)
	_, found := processes["docs-mcp"]
	require.False(t, found)
}

func TestRepairCommandQuotesMCPCommandWithArguments(t *testing.T) {
	t.Parallel()

	command := repairCommand(
		"docs-mcp",
		pluginstate.AliasRecord{
			AdminProfile:    "default",
			ProfileName:     "docs-mcp",
			ProfileDir:      "/tmp/profile dir",
			OrganizationIDs: []string{"org_123"},
		},
		pluginstate.ProcessRecord{
			TargetKind:  "command",
			TargetValue: "/bin/bash /tmp/free space mcp.sh",
		},
	)

	require.Contains(t, command, "--profile-dir '/tmp/profile dir'")
	require.Contains(t, command, "--mcp-command '/bin/bash /tmp/free space mcp.sh'")
}

func TestConnectPayloadIncludesLaunchDiagnosticsLogTail(t *testing.T) {
	t.Parallel()

	root := pluginstate.Root{Path: t.TempDir()}
	require.NoError(t, pluginstate.EnsureDirs(root))
	logPath := filepath.Join(root.Path, "logs", "docs-mcp.log")
	require.NoError(t, os.MkdirAll(filepath.Dir(logPath), 0o755))
	require.NoError(t, os.WriteFile(logPath, []byte("line one\nlaunch failed\n"), 0o600))

	manager := NewManager(testLookupEnv(map[string]string{
		"TUNNEL_CLIENT_STATE_DIR": root.Path,
		"HOME":                    t.TempDir(),
	}), session.Runtime{})
	payload := manager.connectPayload(
		root,
		"docs-mcp",
		adminapi.Tunnel{ID: "tunnel_123"},
		effectiveAdminProfile{Name: "default"},
		pluginstate.AliasRecord{Alias: "docs-mcp", TunnelID: "tunnel_123"},
		pluginstate.ProcessRecord{Alias: "docs-mcp", LogPath: logPath},
		session.LaunchResult{Launched: true, LogPath: logPath, LogTail: "launch failed"},
		"",
	)

	diagnostics := payload["launch_diagnostics"].(map[string]any)
	require.Equal(t, logPath, diagnostics["log_path"])
	require.Equal(t, "launch failed", diagnostics["log_tail"])
}

func TestStopRejectsUnownedLegacyTmuxSessionWithoutChangingRecord(t *testing.T) {
	t.Parallel()

	root := pluginstate.Root{Path: t.TempDir()}
	require.NoError(t, pluginstate.EnsureDirs(root))
	require.NoError(t, pluginstate.SaveAliases(root, map[string]pluginstate.AliasRecord{
		"docs-mcp": {
			Alias:    "docs-mcp",
			TunnelID: "tunnel_123",
		},
	}))
	require.NoError(t, pluginstate.SaveProcesses(root, map[string]pluginstate.ProcessRecord{
		"docs-mcp": {
			Alias:       "docs-mcp",
			TunnelID:    "tunnel_123",
			Mode:        "tmux",
			SessionName: "user-session",
		},
	}))

	manager := NewManager(testLookupEnv(map[string]string{
		"TUNNEL_CLIENT_STATE_DIR": root.Path,
		"HOME":                    t.TempDir(),
	}), session.Runtime{
		Run: func(args []string, env map[string]string) (session.CompletedProcess, error) {
			t.Fatalf("unexpected tmux command: %v", args)
			return session.CompletedProcess{}, nil
		},
	})

	payload, err := manager.Stop(AliasOptions{Alias: "docs-mcp"})
	require.Error(t, err)
	require.Equal(t, false, payload["stopped"])
	require.Contains(t, payload["stop_error"], "does not match tunnel-client-owned session")

	processes, loadErr := pluginstate.LoadProcesses(root)
	require.NoError(t, loadErr)
	require.Equal(t, "tmux", processes["docs-mcp"].Mode)
	require.Equal(t, "user-session", processes["docs-mcp"].SessionName)
}

func TestStopDoesNotSignalReusedPID(t *testing.T) {
	t.Parallel()

	root := pluginstate.Root{Path: t.TempDir()}
	require.NoError(t, pluginstate.EnsureDirs(root))
	require.NoError(t, pluginstate.SaveAliases(root, map[string]pluginstate.AliasRecord{
		"docs-mcp": {
			Alias:    "docs-mcp",
			TunnelID: "tunnel_123",
		},
	}))
	require.NoError(t, pluginstate.SaveProcesses(root, map[string]pluginstate.ProcessRecord{
		"docs-mcp": {
			Alias:         "docs-mcp",
			TunnelID:      "tunnel_123",
			Mode:          "process",
			PID:           os.Getpid(),
			PIDStartTime:  "stale-start",
			PIDExecutable: "/test/tunnel-client",
		},
	}))
	terminated := false
	manager := NewManager(testLookupEnv(map[string]string{
		"TUNNEL_CLIENT_STATE_DIR": root.Path,
		"HOME":                    t.TempDir(),
	}), session.Runtime{
		Run: func(args []string, env map[string]string) (session.CompletedProcess, error) {
			return session.CompletedProcess{}, nil
		},
		Start: func(args []string, env map[string]string, logPath string) (session.Process, error) {
			return managerTestProcess{pid: os.Getpid()}, nil
		},
		InspectProcess: func(pid int) (session.ProcessIdentity, error) {
			return session.ProcessIdentity{StartTime: "different-start", Executable: "/test/tunnel-client"}, nil
		},
		Terminate: func(pid int) error {
			terminated = true
			return nil
		},
	})

	payload, err := manager.Stop(AliasOptions{Alias: "docs-mcp"})
	require.NoError(t, err)
	require.Equal(t, true, payload["already_stopped"])
	require.False(t, terminated)
	processes, loadErr := pluginstate.LoadProcesses(root)
	require.NoError(t, loadErr)
	require.Equal(t, "stopped", processes["docs-mcp"].Mode)
	require.Zero(t, processes["docs-mcp"].PID)
}

func testLookupEnv(values map[string]string) func(string) (string, bool) {
	return func(key string) (string, bool) {
		value, ok := values[key]
		return value, ok
	}
}

type managerTestProcess struct {
	pid   int
	abort func() error
}

func (p managerTestProcess) PID() int { return p.pid }

func (managerTestProcess) Poll() *int { return nil }

func (p managerTestProcess) Abort() error {
	if p.abort != nil {
		return p.abort()
	}
	return nil
}

func requireNoFileContains(t *testing.T, root string, needle string) {
	t.Helper()
	if _, err := os.Stat(root); os.IsNotExist(err) {
		return
	}
	require.NoError(t, filepath.WalkDir(root, func(path string, entry os.DirEntry, err error) error {
		require.NoError(t, err)
		if entry.IsDir() {
			return nil
		}
		data, readErr := os.ReadFile(path)
		require.NoError(t, readErr)
		require.NotContains(t, string(data), needle, path)
		return nil
	}))
}

func toStringSlice(value any) []string {
	raw, _ := value.([]string)
	return raw
}

func requireRepairAction(t *testing.T, value any, id string) {
	t.Helper()
	actions, _ := value.([]RepairAction)
	for _, action := range actions {
		if action.ID == id {
			require.NotEmpty(t, action.Command)
			require.NotEmpty(t, action.Reason)
			return
		}
	}
	t.Fatalf("expected repair action %q in %#v", id, value)
}

func inventoryClassForAlias(payload map[string]any, alias string) string {
	entries, _ := payload["entries"].([]map[string]any)
	for _, entry := range entries {
		if entry["alias"] == alias {
			classification, _ := entry["classification"].(string)
			return classification
		}
	}
	return ""
}
