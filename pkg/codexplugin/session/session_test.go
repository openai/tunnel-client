package session

import (
	"errors"
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
	abort    func() error
}

func (p *fakeProcess) PID() int   { return p.pid }
func (p *fakeProcess) Poll() *int { return p.exitCode }
func (p *fakeProcess) Abort() error {
	if p.abort != nil {
		return p.abort()
	}
	return nil
}

func newHealthyRuntimeServer(t *testing.T) *httptest.Server {
	t.Helper()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/healthz", "/readyz":
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte("ok"))
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(server.Close)
	return server
}

func writeRuntimeHealthURL(t *testing.T, alias string, root state.Root, healthURL string) {
	t.Helper()
	require.NoError(t, state.EnsureDirs(root))
	require.NoError(t, os.WriteFile(ProfileHealthURLFile(alias, root), []byte(healthURL), 0o600))
}

func mustCurrentProcessIdentity(t *testing.T) ProcessIdentity {
	t.Helper()
	identity, err := CaptureProcessIdentity(os.Getpid())
	require.NoError(t, err)
	return identity
}

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

func TestStartOrReuseUsesProcessModeWithoutTmux(t *testing.T) {
	t.Parallel()

	root := state.Root{Path: t.TempDir()}
	healthServer := newHealthyRuntimeServer(t)
	started := false
	rt := Runtime{
		Run: func(args []string, env map[string]string) (CompletedProcess, error) {
			t.Fatalf("unexpected tmux command: %v", args)
			return CompletedProcess{}, nil
		},
		Start: func(args []string, env map[string]string, logPath string) (Process, error) {
			started = true
			require.Equal(t, "docs-mcp", args[5])
			require.Equal(t, "/tmp/example.AppImage", env["APPIMAGE"])
			require.Equal(t, "tunnel-client", env["ARGV0"])
			writeRuntimeHealthURL(t, "docs-mcp", root, healthServer.URL+"/healthz")
			return &fakeProcess{pid: os.Getpid()}, nil
		},
	}

	result, err := StartOrReuseWithExistingRuntime(
		rt,
		"docs-mcp",
		"docs-mcp",
		t.TempDir(),
		"",
		root,
		map[string]string{"APPIMAGE": "/tmp/example.AppImage", "ARGV0": "tunnel-client"},
		ExistingRuntime{},
		false,
	)
	require.NoError(t, err)
	require.True(t, started)
	require.Equal(t, "process", result.Mode)
	require.True(t, result.Launched)
	require.True(t, result.Healthy)
	require.True(t, result.Ready)
	require.Equal(t, os.Getpid(), result.PID)
	require.Empty(t, result.SessionName)
}

func TestStartOrReuseReusesExistingProcess(t *testing.T) {
	t.Parallel()

	root := state.Root{Path: t.TempDir()}
	healthServer := newHealthyRuntimeServer(t)
	writeRuntimeHealthURL(t, "docs-mcp", root, healthServer.URL+"/healthz")
	rt := Runtime{
		Run: func(args []string, env map[string]string) (CompletedProcess, error) {
			t.Fatalf("unexpected tmux command: %v", args)
			return CompletedProcess{}, nil
		},
		Start: func(args []string, env map[string]string, logPath string) (Process, error) {
			t.Fatalf("unexpected process launch: %v", args)
			return nil, nil
		},
	}

	result, err := StartOrReuseWithExistingRuntime(
		rt,
		"docs-mcp",
		"docs-mcp",
		t.TempDir(),
		"",
		root,
		nil,
		ExistingRuntime{
			Mode:          "process",
			PID:           os.Getpid(),
			PIDStartTime:  mustCurrentProcessIdentity(t).StartTime,
			PIDExecutable: mustCurrentProcessIdentity(t).Executable,
		},
		false,
	)
	require.NoError(t, err)
	require.Equal(t, "process", result.Mode)
	require.True(t, result.AlreadyRunning)
	require.True(t, result.Healthy)
	require.Equal(t, os.Getpid(), result.PID)
}

func TestStartOrReuseDoesNotReuseOrTerminateMismatchedProcessIdentity(t *testing.T) {
	t.Parallel()

	root := state.Root{Path: t.TempDir()}
	healthServer := newHealthyRuntimeServer(t)
	actual := ProcessIdentity{StartTime: "actual", Executable: "/tmp/tunnel-client"}
	terminated := false
	startCalls := 0
	rt := Runtime{
		Run: func(args []string, env map[string]string) (CompletedProcess, error) {
			t.Fatalf("unexpected tmux command: %v", args)
			return CompletedProcess{}, nil
		},
		Start: func(args []string, env map[string]string, logPath string) (Process, error) {
			startCalls++
			writeRuntimeHealthURL(t, "docs-mcp", root, healthServer.URL+"/healthz")
			return &fakeProcess{pid: os.Getpid()}, nil
		},
		InspectProcess: func(pid int) (ProcessIdentity, error) {
			return actual, nil
		},
		Terminate: func(pid int) error {
			terminated = true
			return nil
		},
	}

	result, err := StartOrReuseWithExistingRuntime(
		rt,
		"docs-mcp",
		"docs-mcp",
		t.TempDir(),
		"",
		root,
		nil,
		ExistingRuntime{
			Mode:          "process",
			PID:           os.Getpid(),
			PIDStartTime:  "stale",
			PIDExecutable: actual.Executable,
		},
		true,
	)
	require.NoError(t, err)
	require.False(t, terminated)
	require.Equal(t, 1, startCalls)
	require.True(t, result.Launched)
}

func TestStartOrReuseWaitsForOwnedProcessBeforeReplacement(t *testing.T) {
	t.Parallel()

	root := state.Root{Path: t.TempDir()}
	healthServer := newHealthyRuntimeServer(t)
	identity := ProcessIdentity{StartTime: "owned", Executable: "/tmp/tunnel-client"}
	terminated := false
	waited := false
	rt := Runtime{
		Run: func(args []string, env map[string]string) (CompletedProcess, error) {
			t.Fatalf("unexpected tmux command: %v", args)
			return CompletedProcess{}, nil
		},
		Start: func(args []string, env map[string]string, logPath string) (Process, error) {
			require.True(t, waited, "replacement started before old process exit was observed")
			writeRuntimeHealthURL(t, "docs-mcp", root, healthServer.URL+"/healthz")
			return &fakeProcess{pid: os.Getpid()}, nil
		},
		InspectProcess: func(pid int) (ProcessIdentity, error) {
			return identity, nil
		},
		Terminate: func(pid int) error {
			terminated = true
			return nil
		},
		WaitForExit: func(pid int, expected ProcessIdentity) bool {
			require.True(t, terminated)
			require.Equal(t, identity, expected)
			waited = true
			return true
		},
	}

	_, err := StartOrReuseWithExistingRuntime(
		rt,
		"docs-mcp",
		"docs-mcp",
		t.TempDir(),
		"",
		root,
		nil,
		ExistingRuntime{
			Mode:          "process",
			PID:           os.Getpid(),
			PIDStartTime:  identity.StartTime,
			PIDExecutable: identity.Executable,
		},
		true,
	)
	require.NoError(t, err)
	require.True(t, terminated)
	require.True(t, waited)
}

func TestStartOrReuseDoesNotLaunchWhenOwnedProcessDoesNotExit(t *testing.T) {
	t.Parallel()

	root := state.Root{Path: t.TempDir()}
	identity := ProcessIdentity{StartTime: "owned", Executable: "/tmp/tunnel-client"}
	started := false
	rt := Runtime{
		Run: func(args []string, env map[string]string) (CompletedProcess, error) {
			t.Fatalf("unexpected tmux command: %v", args)
			return CompletedProcess{}, nil
		},
		Start: func(args []string, env map[string]string, logPath string) (Process, error) {
			started = true
			return nil, nil
		},
		InspectProcess: func(pid int) (ProcessIdentity, error) {
			return identity, nil
		},
		Terminate: func(pid int) error { return nil },
		WaitForExit: func(pid int, expected ProcessIdentity) bool {
			return false
		},
	}

	_, err := StartOrReuseWithExistingRuntime(
		rt,
		"docs-mcp",
		"docs-mcp",
		t.TempDir(),
		"",
		root,
		nil,
		ExistingRuntime{
			Mode:          "process",
			PID:           os.Getpid(),
			PIDStartTime:  identity.StartTime,
			PIDExecutable: identity.Executable,
		},
		true,
	)
	require.Error(t, err)
	require.Contains(t, err.Error(), "did not exit after SIGTERM")
	require.False(t, started)
}

func TestStartOrReuseSafelyAbortsWhenIdentityCaptureFails(t *testing.T) {
	t.Parallel()

	root := state.Root{Path: t.TempDir()}
	aborted := false
	rt := Runtime{
		Run: func(args []string, env map[string]string) (CompletedProcess, error) {
			t.Fatalf("unexpected tmux command: %v", args)
			return CompletedProcess{}, nil
		},
		Start: func(args []string, env map[string]string, logPath string) (Process, error) {
			return &fakeProcess{pid: os.Getpid(), abort: func() error {
				aborted = true
				return nil
			}}, nil
		},
		InspectProcess: func(pid int) (ProcessIdentity, error) {
			return ProcessIdentity{}, errors.New("identity unavailable")
		},
	}

	result, err := StartOrReuseWithExistingRuntime(
		rt,
		"docs-mcp",
		"docs-mcp",
		t.TempDir(),
		"",
		root,
		nil,
		ExistingRuntime{},
		false,
	)
	require.Error(t, err)
	require.Contains(t, err.Error(), "safely aborted launched process before state persistence")
	require.True(t, result.Launched)
	require.Zero(t, result.PID)
	require.True(t, aborted)
}

func TestStartOrReuseReportsFastExitBeforeIdentityFailure(t *testing.T) {
	t.Parallel()

	exitCode := 7
	aborted := false
	rt := Runtime{
		Start: func(args []string, env map[string]string, logPath string) (Process, error) {
			return &fakeProcess{pid: os.Getpid(), exitCode: &exitCode, abort: func() error {
				aborted = true
				return nil
			}}, nil
		},
		InspectProcess: func(pid int) (ProcessIdentity, error) {
			return ProcessIdentity{}, errors.New("identity unavailable")
		},
	}

	result, err := StartOrReuseWithExistingRuntime(
		rt,
		"docs-mcp",
		"docs-mcp",
		t.TempDir(),
		"",
		state.Root{Path: t.TempDir()},
		nil,
		ExistingRuntime{},
		false,
	)
	require.NoError(t, err)
	require.NotNil(t, result.ExitCode)
	require.Equal(t, exitCode, *result.ExitCode)
	require.Zero(t, result.PID)
	require.False(t, aborted)
}

func TestStartOrReuseRejectsLivePIDWithoutStableIdentity(t *testing.T) {
	t.Parallel()

	_, err := StartOrReuse(
		Runtime{},
		"docs-mcp",
		"docs-mcp",
		t.TempDir(),
		"",
		state.Root{Path: t.TempDir()},
		nil,
		os.Getpid(),
		false,
	)
	require.Error(t, err)
	require.Contains(t, err.Error(), "without a stable process identity")
}

func TestStartOrReuseMigratesOwnedTmuxSessionToProcess(t *testing.T) {
	t.Parallel()

	root := state.Root{Path: t.TempDir()}
	healthServer := newHealthyRuntimeServer(t)
	sessionName := TmuxSessionName("docs-mcp", root)
	var gotRunArgs [][]string
	started := false
	rt := Runtime{
		Run: func(args []string, env map[string]string) (CompletedProcess, error) {
			gotRunArgs = append(gotRunArgs, append([]string{}, args...))
			switch args[3] {
			case "has-session", "kill-session":
				return CompletedProcess{ReturnCode: 0}, nil
			default:
				t.Fatalf("unexpected tmux command: %v", args)
				return CompletedProcess{}, nil
			}
		},
		Start: func(args []string, env map[string]string, logPath string) (Process, error) {
			started = true
			require.Equal(t, [][]string{
				{"tmux", "-L", "default", "has-session", "-t", "=" + sessionName},
				{"tmux", "-L", "default", "kill-session", "-t", "=" + sessionName},
			}, gotRunArgs)
			writeRuntimeHealthURL(t, "docs-mcp", root, healthServer.URL+"/healthz")
			return &fakeProcess{pid: os.Getpid()}, nil
		},
	}

	result, err := StartOrReuseWithExistingRuntime(
		rt,
		"docs-mcp",
		"docs-mcp",
		t.TempDir(),
		"",
		root,
		nil,
		ExistingRuntime{Mode: "tmux", SessionName: sessionName},
		false,
	)
	require.NoError(t, err)
	require.True(t, started)
	require.Equal(t, "process", result.Mode)
	require.True(t, result.Healthy)
	require.Equal(t, os.Getpid(), result.PID)
	require.Empty(t, result.SessionName)
}

func TestStartOrReuseRecoversAmbientLegacyTmuxSocket(t *testing.T) {
	root := state.Root{Path: t.TempDir()}
	healthServer := newHealthyRuntimeServer(t)
	sessionName := TmuxSessionName("docs-mcp", root)
	const socketPath = "/tmp/tmux-501/custom"
	t.Setenv("TMUX", socketPath+",123,0")
	var gotRunArgs [][]string
	rt := Runtime{
		Run: func(args []string, env map[string]string) (CompletedProcess, error) {
			gotRunArgs = append(gotRunArgs, append([]string{}, args...))
			return CompletedProcess{ReturnCode: 0}, nil
		},
		Start: func(args []string, env map[string]string, logPath string) (Process, error) {
			writeRuntimeHealthURL(t, "docs-mcp", root, healthServer.URL+"/healthz")
			return &fakeProcess{pid: os.Getpid()}, nil
		},
	}

	_, err := StartOrReuseWithExistingRuntime(
		rt,
		"docs-mcp",
		"docs-mcp",
		t.TempDir(),
		"",
		root,
		nil,
		ExistingRuntime{Mode: "tmux", SessionName: sessionName},
		false,
	)
	require.NoError(t, err)
	require.Equal(t, [][]string{
		{"tmux", "-S", socketPath, "has-session", "-t", "=" + sessionName},
		{"tmux", "-S", socketPath, "kill-session", "-t", "=" + sessionName},
	}, gotRunArgs)
}

func TestStartOrReuseStopsDefaultLegacyTmuxAfterAmbientMiss(t *testing.T) {
	root := state.Root{Path: t.TempDir()}
	healthServer := newHealthyRuntimeServer(t)
	sessionName := TmuxSessionName("docs-mcp", root)
	const ambientSocket = "/tmp/tmux-501/custom"
	t.Setenv("TMUX", ambientSocket+",123,0")
	var gotRunArgs [][]string
	rt := Runtime{
		Run: func(args []string, env map[string]string) (CompletedProcess, error) {
			gotRunArgs = append(gotRunArgs, append([]string{}, args...))
			if args[1] == "-S" {
				return CompletedProcess{ReturnCode: 1, Stderr: "error connecting to " + ambientSocket + " (No such file or directory)"}, nil
			}
			return CompletedProcess{ReturnCode: 0}, nil
		},
		Start: func(args []string, env map[string]string, logPath string) (Process, error) {
			writeRuntimeHealthURL(t, "docs-mcp", root, healthServer.URL+"/healthz")
			return &fakeProcess{pid: os.Getpid()}, nil
		},
	}

	_, err := StartOrReuseWithExistingRuntime(
		rt,
		"docs-mcp",
		"docs-mcp",
		t.TempDir(),
		"",
		root,
		nil,
		ExistingRuntime{Mode: "tmux", SessionName: sessionName},
		false,
	)
	require.NoError(t, err)
	require.Equal(t, [][]string{
		{"tmux", "-S", ambientSocket, "has-session", "-t", "=" + sessionName},
		{"tmux", "-L", "default", "has-session", "-t", "=" + sessionName},
		{"tmux", "-L", "default", "kill-session", "-t", "=" + sessionName},
	}, gotRunArgs)
}

func TestStartOrReuseFailsClosedWhenLegacyTmuxSocketProvenanceIsUnknown(t *testing.T) {
	t.Parallel()

	root := state.Root{Path: t.TempDir()}
	sessionName := TmuxSessionName("docs-mcp", root)
	started := false
	rt := Runtime{
		Run: func(args []string, env map[string]string) (CompletedProcess, error) {
			return CompletedProcess{ReturnCode: 1, Stderr: "can't find session: " + sessionName}, nil
		},
		Start: func(args []string, env map[string]string, logPath string) (Process, error) {
			started = true
			return nil, nil
		},
	}

	_, err := StartOrReuseWithExistingRuntime(
		rt,
		"docs-mcp",
		"docs-mcp",
		t.TempDir(),
		"",
		root,
		nil,
		ExistingRuntime{Mode: "tmux", SessionName: sessionName},
		false,
	)
	require.Error(t, err)
	require.Contains(t, err.Error(), "no socket provenance")
	require.False(t, started)
}

func TestStartOrReuseFailsClosedOnUnexpectedTmuxInspectionFailure(t *testing.T) {
	t.Parallel()

	root := state.Root{Path: t.TempDir()}
	sessionName := TmuxSessionName("docs-mcp", root)
	started := false
	rt := Runtime{
		Run: func(args []string, env map[string]string) (CompletedProcess, error) {
			return CompletedProcess{ReturnCode: 2, Stderr: "permission denied"}, nil
		},
		Start: func(args []string, env map[string]string, logPath string) (Process, error) {
			started = true
			return nil, nil
		},
	}

	_, err := StartOrReuseWithExistingRuntime(
		rt,
		"docs-mcp",
		"docs-mcp",
		t.TempDir(),
		"",
		root,
		nil,
		ExistingRuntime{Mode: "tmux", SessionName: sessionName},
		false,
	)
	require.Error(t, err)
	require.Contains(t, err.Error(), "tmux has-session failed: permission denied")
	require.False(t, started)
}

func TestTmuxSessionIsKnownAbsentDistinguishesMissingSocketFromFailures(t *testing.T) {
	t.Parallel()

	require.True(t, tmuxSessionIsKnownAbsent(CompletedProcess{
		ReturnCode: 1,
		Stderr:     "error connecting to /tmp/tmux-501/custom (No such file or directory)",
	}))
	require.True(t, tmuxSessionIsKnownAbsent(CompletedProcess{
		ReturnCode: 1,
		Stderr:     "can't find session: docs-mcp",
	}))
	require.False(t, tmuxSessionIsKnownAbsent(CompletedProcess{
		ReturnCode: 1,
		Stderr:     "permission denied",
	}))
	require.False(t, tmuxSessionIsKnownAbsent(CompletedProcess{
		ReturnCode: 2,
		Stderr:     "can't find session: docs-mcp",
	}))
}

func TestStartOrReuseDoesNotLaunchWhenLegacyTmuxStopFails(t *testing.T) {
	t.Parallel()

	root := state.Root{Path: t.TempDir()}
	sessionName := TmuxSessionName("docs-mcp", root)
	started := false
	rt := Runtime{
		Run: func(args []string, env map[string]string) (CompletedProcess, error) {
			switch args[3] {
			case "has-session":
				return CompletedProcess{ReturnCode: 0}, nil
			case "kill-session":
				return CompletedProcess{ReturnCode: 1, Stderr: "boom"}, nil
			default:
				t.Fatalf("unexpected tmux command: %v", args)
				return CompletedProcess{}, nil
			}
		},
		Start: func(args []string, env map[string]string, logPath string) (Process, error) {
			started = true
			return nil, nil
		},
	}

	_, err := StartOrReuseWithExistingRuntime(
		rt,
		"docs-mcp",
		"docs-mcp",
		t.TempDir(),
		"",
		root,
		nil,
		ExistingRuntime{Mode: "tmux", SessionName: sessionName},
		false,
	)
	require.Error(t, err)
	require.Contains(t, err.Error(), "tmux kill-session failed: boom")
	require.False(t, started)
}

func TestStartOrReuseRejectsUnownedLegacyTmuxSession(t *testing.T) {
	t.Parallel()

	root := state.Root{Path: t.TempDir()}
	rt := Runtime{
		Run: func(args []string, env map[string]string) (CompletedProcess, error) {
			t.Fatalf("unexpected tmux command: %v", args)
			return CompletedProcess{}, nil
		},
		Start: func(args []string, env map[string]string, logPath string) (Process, error) {
			t.Fatalf("unexpected process launch: %v", args)
			return nil, nil
		},
	}

	_, err := StartOrReuseWithExistingRuntime(
		rt,
		"docs-mcp",
		"docs-mcp",
		t.TempDir(),
		"",
		root,
		nil,
		ExistingRuntime{Mode: "tmux", SessionName: "user-session"},
		false,
	)
	require.Error(t, err)
	require.Contains(t, err.Error(), "does not match tunnel-client-owned session")
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
			if len(args) >= 4 && args[0] == "tmux" && args[3] == "list-panes" {
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
		{"tmux", "-L", "default", "new-session", "-d", "-s", "tunnel-mcp__docs-mcp__deadbeef"},
		{"tmux", "-L", "default", "list-panes", "-t", "=tunnel-mcp__docs-mcp__deadbeef", "-F", "#{pane_id}"},
		{"tmux", "-L", "default", "respawn-pane", "-k", "-t", "%42", executable, "run", "--profile-dir", "/tmp/profiles", "--profile", "docs-mcp"},
	}, gotRunArgs)
	require.Equal(t, []string{"tmux", "-L", "default", "source-file", "-"}, gotArgs)
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
		"-L",
		"default",
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

func TestStartProcessRejectsSymlinkLogFile(t *testing.T) {
	t.Parallel()
	if runtime.GOOS == "windows" {
		t.Skip("symlink permissions vary on Windows")
	}

	dir := t.TempDir()
	targetPath := filepath.Join(dir, "target.log")
	logPath := filepath.Join(dir, "runtime.log")
	require.NoError(t, os.WriteFile(targetPath, []byte("existing"), 0o600))
	require.NoError(t, os.Symlink(targetPath, logPath))
	args, err := currentTunnelClientInvocation("docs-mcp", t.TempDir())
	require.NoError(t, err)

	_, err = startProcess(args, nil, logPath)
	require.Error(t, err)
	require.Contains(t, err.Error(), "must not be a symlink")
}

func TestStartTmuxCleansUpSessionWhenSourceFileFails(t *testing.T) {
	t.Parallel()

	var gotRunArgs [][]string
	rt := Runtime{
		Run: func(args []string, env map[string]string) (CompletedProcess, error) {
			gotRunArgs = append(gotRunArgs, append([]string{}, args...))
			if len(args) >= 4 && args[0] == "tmux" && args[3] == "list-panes" {
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
		{"tmux", "-L", "default", "new-session", "-d", "-s", "tunnel-mcp__docs-mcp__deadbeef"},
		{"tmux", "-L", "default", "list-panes", "-t", "=tunnel-mcp__docs-mcp__deadbeef", "-F", "#{pane_id}"},
		{"tmux", "-L", "default", "kill-session", "-t", "=tunnel-mcp__docs-mcp__deadbeef"},
	}, gotRunArgs)
}
