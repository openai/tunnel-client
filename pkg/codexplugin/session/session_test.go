package session

import (
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/openai/tunnel-client/pkg/codexplugin/state"
)

type fakeProcess struct {
	pid      int
	exitCode *int
}

func (p *fakeProcess) PID() int   { return p.pid }
func (p *fakeProcess) Poll() *int { return p.exitCode }

func TestWriteRuntimeProfileUsesExistingJSONCompatibleShape(t *testing.T) {
	t.Parallel()

	root := state.Root{Path: t.TempDir()}
	path, err := WriteRuntimeProfile(
		"docs-mcp",
		"",
		"tunnel_123",
		"https://api.openai.com",
		"/chatgpttunnelgateway/dev/us",
		"env:CONTROL_PLANE_API_KEY",
		Target{Kind: "server_url", Value: "http://127.0.0.1:3001/mcp"},
		filepath.Join(t.TempDir(), "profiles"),
		root,
		nil,
	)
	require.NoError(t, err)
	data, err := os.ReadFile(path)
	require.NoError(t, err)
	require.Contains(t, string(data), `"config_version": 1`)
	require.Contains(t, string(data), `"url_path": "/chatgpttunnelgateway/dev/us"`)
	require.Contains(t, string(data), `"server_urls": [`)
}

func TestTmuxSessionNameIsScopedByStateRoot(t *testing.T) {
	t.Parallel()

	first := TmuxSessionName("docs-mcp", state.Root{Path: "/tmp/one"})
	second := TmuxSessionName("docs-mcp", state.Root{Path: "/tmp/two"})
	require.NotEqual(t, first, second)
	require.Contains(t, first, "tunnel-mcp__docs-mcp__")
}

func TestProbeHealthEndpoints(t *testing.T) {
	t.Parallel()

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/healthz":
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte("live"))
		case "/readyz":
			w.WriteHeader(http.StatusServiceUnavailable)
			_, _ = w.Write([]byte("pending"))
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer server.Close()

	probe := ProbeHealthEndpoints(server.URL + "/healthz")
	require.True(t, probe.Healthz.OK)
	require.False(t, probe.Readyz.OK)
	require.Equal(t, http.StatusServiceUnavailable, probe.Readyz.Status)
}

func TestStartOrReuseFallsBackToProcessMode(t *testing.T) {
	t.Parallel()

	root := state.Root{Path: t.TempDir()}
	require.NoError(t, state.EnsureDirs(root))

	healthURL := "http://127.0.0.1:43199/healthz"
	require.NoError(t, os.WriteFile(ProfileHealthURLFile("docs-mcp", root), []byte(healthURL), 0o600))
	rt := Runtime{
		Run: func(args []string, env map[string]string) (CompletedProcess, error) {
			if len(args) >= 2 && args[0] == "tmux" && args[1] == "-V" {
				return CompletedProcess{}, os.ErrNotExist
			}
			return CompletedProcess{}, nil
		},
		Start: func(args []string, env map[string]string, logPath string) (Process, error) {
			return &fakeProcess{pid: os.Getpid()}, nil
		},
	}

	result, err := StartOrReuse(rt, "docs-mcp", "docs-mcp", t.TempDir(), "/bin/tunnel-client", root, nil, 0, false)
	require.NoError(t, err)
	require.Equal(t, "process", result.Mode)
	require.True(t, result.Launched)
	require.Equal(t, os.Getpid(), result.PID)
}

func TestStartTmuxUsesSourceFileForSecretEnv(t *testing.T) {
	t.Parallel()

	var gotRunArgs [][]string
	var gotArgs []string
	var gotStdin string
	rt := Runtime{
		Run: func(args []string, env map[string]string) (CompletedProcess, error) {
			gotRunArgs = append(gotRunArgs, append([]string{}, args...))
			if len(args) >= 2 && args[0] == "tmux" && args[1] == "list-panes" {
				return CompletedProcess{ReturnCode: 0, Stdout: "%42\n"}, nil
			}
			return CompletedProcess{ReturnCode: 0}, nil
		},
		RunInput: func(args []string, env map[string]string, stdin string) (CompletedProcess, error) {
			gotArgs = append([]string{}, args...)
			gotStdin = stdin
			return CompletedProcess{ReturnCode: 0}, nil
		},
	}

	_, err := StartTmux(
		rt,
		"tunnel-mcp__docs-mcp__deadbeef",
		"/tmp/tunnel-client",
		"docs-mcp",
		"/tmp/profiles",
		map[string]string{"OPENAI_TUNNEL_KEY_PROD": "sk-proj-runtime-secret"},
		filepath.Join(t.TempDir(), "runtime.log"),
	)
	require.NoError(t, err)
	require.Equal(t, [][]string{
		{"tmux", "new-session", "-d", "-s", "tunnel-mcp__docs-mcp__deadbeef"},
		{"tmux", "list-panes", "-t", "=tunnel-mcp__docs-mcp__deadbeef", "-F", "#{pane_id}"},
	}, gotRunArgs)
	require.Equal(t, []string{"tmux", "source-file", "-"}, gotArgs)
	require.Contains(t, gotStdin, "set-environment -t =tunnel-mcp__docs-mcp__deadbeef OPENAI_TUNNEL_KEY_PROD sk-proj-runtime-secret")
	require.Contains(t, gotStdin, "respawn-pane -k -t %42")
	require.Contains(t, gotStdin, "tunnel-client run --profile-dir /tmp/profiles --profile docs-mcp")
	require.Contains(t, gotStdin, " 2>&1")
	require.NotContains(t, strings.Join(gotRunArgs[0], " "), "OPENAI_TUNNEL_KEY_PROD=sk-proj-runtime-secret")
	require.NotContains(t, strings.Join(gotRunArgs[1], " "), "OPENAI_TUNNEL_KEY_PROD=sk-proj-runtime-secret")
	require.NotContains(t, strings.Join(gotArgs, " "), "OPENAI_TUNNEL_KEY_PROD=sk-proj-runtime-secret")
}

func TestStartTmuxPrecreatesPrivateLogFile(t *testing.T) {
	t.Parallel()

	var gotArgs []string
	rt := Runtime{
		Run: func(args []string, env map[string]string) (CompletedProcess, error) {
			gotArgs = append([]string{}, args...)
			return CompletedProcess{ReturnCode: 0}, nil
		},
	}
	logPath := filepath.Join(t.TempDir(), "runtime.log")

	_, err := StartTmux(
		rt,
		"tunnel-mcp__docs-mcp__deadbeef",
		"/tmp/tunnel-client",
		"docs-mcp",
		"/tmp/profiles",
		nil,
		logPath,
	)
	require.NoError(t, err)
	require.Contains(t, strings.Join(gotArgs, " "), " >> "+shellQuote(logPath)+" 2>&1")
	info, err := os.Stat(logPath)
	require.NoError(t, err)
	require.Equal(t, os.FileMode(0o600), info.Mode().Perm())
}

func TestStartTmuxRejectsSymlinkLogFile(t *testing.T) {
	t.Parallel()
	if runtime.GOOS == "windows" {
		t.Skip("symlink permissions vary on Windows")
	}

	dir := t.TempDir()
	targetPath := filepath.Join(dir, "target.log")
	logPath := filepath.Join(dir, "runtime.log")
	require.NoError(t, os.WriteFile(targetPath, []byte("existing"), 0o600))
	require.NoError(t, os.Symlink(targetPath, logPath))

	_, err := StartTmux(
		Runtime{},
		"tunnel-mcp__docs-mcp__deadbeef",
		"/tmp/tunnel-client",
		"docs-mcp",
		"/tmp/profiles",
		nil,
		logPath,
	)
	require.Error(t, err)
	require.Contains(t, err.Error(), "must not be a symlink")
}

func TestStartTmuxCleansUpSessionWhenSourceFileFails(t *testing.T) {
	t.Parallel()

	var gotRunArgs [][]string
	rt := Runtime{
		Run: func(args []string, env map[string]string) (CompletedProcess, error) {
			gotRunArgs = append(gotRunArgs, append([]string{}, args...))
			if len(args) >= 2 && args[0] == "tmux" && args[1] == "list-panes" {
				return CompletedProcess{ReturnCode: 0, Stdout: "%42\n"}, nil
			}
			return CompletedProcess{ReturnCode: 0}, nil
		},
		RunInput: func(args []string, env map[string]string, stdin string) (CompletedProcess, error) {
			return CompletedProcess{ReturnCode: 1, Stderr: "boom"}, nil
		},
	}

	result, err := StartTmux(
		rt,
		"tunnel-mcp__docs-mcp__deadbeef",
		"/tmp/tunnel-client",
		"docs-mcp",
		"/tmp/profiles",
		map[string]string{"OPENAI_TUNNEL_KEY_PROD": "sk-proj-runtime-secret"},
		filepath.Join(t.TempDir(), "runtime.log"),
	)
	require.NoError(t, err)
	require.Equal(t, 1, result.ReturnCode)
	require.Equal(t, [][]string{
		{"tmux", "new-session", "-d", "-s", "tunnel-mcp__docs-mcp__deadbeef"},
		{"tmux", "list-panes", "-t", "=tunnel-mcp__docs-mcp__deadbeef", "-F", "#{pane_id}"},
		{"tmux", "kill-session", "-t", "=tunnel-mcp__docs-mcp__deadbeef"},
	}, gotRunArgs)
}
