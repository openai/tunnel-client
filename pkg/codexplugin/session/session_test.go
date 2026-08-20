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
	require.Contains(t, string(data), `"file": "`+LogPath("docs-mcp", root)+`"`)
}

func TestTmuxSessionNameIsScopedByStateRoot(t *testing.T) {
	t.Parallel()

	first := TmuxSessionName("docs-mcp", state.Root{Path: "/tmp/one"})
	second := TmuxSessionName("docs-mcp", state.Root{Path: "/tmp/two"})
	require.NotEqual(t, first, second)
	require.Contains(t, first, "tunnel-mcp__docs-mcp__")
}

func TestShellQuoteAlwaysSingleQuotesShellTokens(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name  string
		value string
		want  string
	}{
		{name: "plain", value: "tunnel-client", want: "'tunnel-client'"},
		{name: "semicolon", value: "a;b", want: "'a;b'"},
		{name: "ampersand", value: "a&b", want: "'a&b'"},
		{name: "pipe", value: "a|b", want: "'a|b'"},
		{name: "input redirect", value: "a<b", want: "'a<b'"},
		{name: "output redirect", value: "a>b", want: "'a>b'"},
		{name: "dollar", value: "$HOME", want: "'$HOME'"},
		{name: "backtick", value: "`id`", want: "'`id`'"},
		{name: "whitespace", value: "two words\tand tab", want: "'two words\tand tab'"},
		{name: "single quote", value: "O'Reilly", want: "'O'\\''Reilly'"},
		{name: "empty", value: "", want: "''"},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			require.Equal(t, tt.want, shellQuote(tt.value))
		})
	}
}

func TestTunnelClientRunArgsRemainRawArgv(t *testing.T) {
	t.Parallel()

	require.Equal(t, []string{
		"run",
		"--profile-dir",
		"/tmp/profiles & data",
		"--profile",
		"docs|mcp",
	}, tunnelClientRunArgs("docs|mcp", "/tmp/profiles & data"))
}

func TestDefaultRuntimeRejectsNonTmuxCommands(t *testing.T) {
	t.Parallel()

	rt := DefaultRuntime()
	_, err := rt.Run([]string{"sh", "-c", "touch /tmp/injected"}, nil)
	require.EqualError(t, err, "default runtime only supports tmux commands")
	_, err = rt.RunInput([]string{"sh", "-c", "touch /tmp/injected"}, nil, "secret")
	require.EqualError(t, err, "default runtime only supports tmux commands")
}

func TestDefaultRuntimeRejectsUnmanagedTmuxCommands(t *testing.T) {
	t.Parallel()

	rt := DefaultRuntime()
	_, err := rt.Run([]string{"tmux", "new-session", "-d", "-s", "safe", "sh", "-c", "touch /tmp/injected"}, nil)
	require.EqualError(t, err, "default runtime only supports managed tmux commands")
}

func TestDefaultRuntimeRejectsNonFixedProcessArgs(t *testing.T) {
	t.Parallel()

	_, err := startProcess([]string{"sh", "-c", "touch /tmp/injected"}, nil, filepath.Join(t.TempDir(), "runtime.log"))
	require.EqualError(t, err, "default runtime only supports the fixed tunnel-client run command")
}

func TestStartOrReuseRejectsAlternateTunnelClientExecutable(t *testing.T) {
	t.Parallel()

	alternate := filepath.Join(t.TempDir(), "alternate-tunnel-client")
	require.NoError(t, os.WriteFile(alternate, []byte("alternate"), 0o700))

	_, err := StartOrReuse(
		Runtime{},
		"docs-mcp",
		"docs-mcp",
		t.TempDir(),
		alternate,
		state.Root{Path: t.TempDir()},
		nil,
		0,
		false,
	)
	require.EqualError(t, err, "tunnel-client executable override must resolve to the current executable")
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
			require.Equal(t, "docs-mcp", args[5])
			return &fakeProcess{pid: os.Getpid()}, nil
		},
	}

	result, err := StartOrReuse(rt, "docs-mcp", "docs-mcp", t.TempDir(), "", root, nil, 0, false)
	require.NoError(t, err)
	require.Equal(t, "process", result.Mode)
	require.True(t, result.Launched)
	require.Equal(t, os.Getpid(), result.PID)
}

func TestStartTmuxUsesSourceFileForSecretEnv(t *testing.T) {
	t.Parallel()

	executable, err := os.Executable()
	require.NoError(t, err)
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

	_, err = StartTmux(
		rt,
		"tunnel-mcp__docs-mcp__deadbeef",
		"",
		"docs-mcp",
		"/tmp/profiles",
		map[string]string{"OPENAI_TUNNEL_KEY_PROD": "sk-proj-runtime-secret"},
		filepath.Join(t.TempDir(), "runtime.log"),
	)
	require.NoError(t, err)
	require.Equal(t, [][]string{
		{"tmux", "new-session", "-d", "-s", "tunnel-mcp__docs-mcp__deadbeef"},
		{"tmux", "list-panes", "-t", "=tunnel-mcp__docs-mcp__deadbeef", "-F", "#{pane_id}"},
		{"tmux", "respawn-pane", "-k", "-t", "%42", executable, "run", "--profile-dir", "/tmp/profiles", "--profile", "docs-mcp"},
	}, gotRunArgs)
	require.Equal(t, []string{"tmux", "source-file", "-"}, gotArgs)
	require.Contains(t, gotStdin, "set-environment -t ='tunnel-mcp__docs-mcp__deadbeef' 'OPENAI_TUNNEL_KEY_PROD' 'sk-proj-runtime-secret'")
	require.NotContains(t, gotStdin, "respawn-pane")
	require.NotContains(t, gotStdin, ">>")
	require.NotContains(t, gotStdin, "2>&1")
	require.NotContains(t, strings.Join(gotRunArgs[0], " "), "OPENAI_TUNNEL_KEY_PROD=sk-proj-runtime-secret")
	require.NotContains(t, strings.Join(gotRunArgs[1], " "), "OPENAI_TUNNEL_KEY_PROD=sk-proj-runtime-secret")
	require.NotContains(t, strings.Join(gotRunArgs[2], " "), "OPENAI_TUNNEL_KEY_PROD=sk-proj-runtime-secret")
	require.NotContains(t, strings.Join(gotArgs, " "), "OPENAI_TUNNEL_KEY_PROD=sk-proj-runtime-secret")
}

func TestStartTmuxRejectsSecretEnvWithoutSourceFileRunner(t *testing.T) {
	t.Parallel()

	_, err := StartTmux(
		Runtime{},
		"tunnel-mcp__docs-mcp__deadbeef",
		"",
		"docs-mcp",
		"/tmp/profiles",
		map[string]string{"OPENAI_TUNNEL_KEY_PROD": "sk-proj-runtime-secret"},
		filepath.Join(t.TempDir(), "runtime.log"),
	)
	require.EqualError(t, err, "tmux source-file runner is required when launch environment is set")
}

func TestStartTmuxPassesDirectCommandArgv(t *testing.T) {
	t.Parallel()

	executable, err := os.Executable()
	require.NoError(t, err)
	var gotArgs []string
	rt := Runtime{
		Run: func(args []string, env map[string]string) (CompletedProcess, error) {
			gotArgs = append([]string{}, args...)
			return CompletedProcess{ReturnCode: 0}, nil
		},
	}
	logPath := filepath.Join(t.TempDir(), "runtime.log")

	_, err = StartTmux(
		rt,
		"tunnel-mcp__docs-mcp__deadbeef",
		"",
		"docs|mcp",
		"/tmp/profiles & data",
		nil,
		logPath,
	)
	require.NoError(t, err)
	require.Equal(t, []string{
		"tmux",
		"new-session",
		"-d",
		"-s",
		"tunnel-mcp__docs-mcp__deadbeef",
		executable,
		"run",
		"--profile-dir",
		"/tmp/profiles & data",
		"--profile",
		"docs|mcp",
	}, gotArgs)
}

func TestTmuxEnvironmentScriptQuotesValues(t *testing.T) {
	t.Parallel()

	const (
		sessionName = "session-name"
		envValue    = "secret; &|<>$ with'quote"
	)
	wantScript := "set-environment -t ='session-name' 'OPENAI_TUNNEL_KEY_PROD' 'secret; &|<>$ with'\\''quote'\n"

	gotScript, err := tmuxEnvironmentScript(
		sessionName,
		map[string]string{"OPENAI_TUNNEL_KEY_PROD": envValue},
	)
	require.NoError(t, err)
	require.Equal(t, wantScript, gotScript)
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
		"",
		"docs-mcp",
		"/tmp/profiles",
		nil,
		logPath,
	)
	require.NoError(t, err)
	require.NotContains(t, strings.Join(gotArgs, " "), ">>")
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
		"",
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
		"",
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
