package session

import (
	"os"
	"path/filepath"
	"runtime"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/openai/tunnel-client/pkg/codexplugin/state"
)

func TestExplicitReadersPreserveSelectedSymlinks(t *testing.T) {
	t.Parallel()
	if runtime.GOOS == "windows" {
		t.Skip("symlink permissions vary on Windows")
	}
	target := filepath.Join(t.TempDir(), "selected")
	require.NoError(t, os.WriteFile(target, []byte("http://127.0.0.1:1234/healthz\n"), 0o600))
	selected := filepath.Join(t.TempDir(), "alias")
	require.NoError(t, os.Symlink(target, selected))
	require.Equal(t, "http://127.0.0.1:1234/healthz", ReadHealthURL(selected))
	require.Equal(t, "http://127.0.0.1:1234/healthz", LogTail(selected, 20))
}

func TestLegacyTmuxRunnerOnlyAcceptsInspectionAndStop(t *testing.T) {
	t.Parallel()
	selectors := [][]string{nil, {"-L", "default"}, {"-S", filepath.Join(t.TempDir(), "socket with spaces")}}
	for _, selector := range selectors {
		for _, operation := range []string{"has-session", "kill-session"} {
			args := append(append([]string{"tmux"}, selector...), operation, "-t", "=owned-session")
			validated, err := validatedTmuxArgs(args)
			require.NoError(t, err)
			require.Equal(t, args[1:], validated)
			for _, suffix := range [][]string{{";", "run-shell", "echo injected"}, {"extra"}} {
				_, err := validatedTmuxArgs(append(args, suffix...))
				require.Error(t, err)
			}
		}
		for _, command := range [][]string{
			{"source-file", "-"}, {"new-session", "-d", "-s", "owned-session"},
			{"respawn-pane", "-k", "-t", "%42"}, {"run-shell", "echo injected"},
			{"list-panes", "-t", "=owned-session", "-F", "#{pane_id}"},
		} {
			args := append(append([]string{"tmux"}, selector...), command...)
			_, err := DefaultRuntime().Run(args, nil)
			require.Error(t, err)
		}
	}
	for _, target := range []string{"owned-session", "=owned;run-shell", "=owned\nkill-server", "=owned:0", "=*"} {
		_, err := validatedTmuxArgs([]string{"tmux", "has-session", "-t", target})
		require.Error(t, err)
	}
	_, err := DefaultRuntime().RunInput([]string{"tmux", "has-session", "-t", "=owned"}, nil, "run-shell echo injected")
	require.EqualError(t, err, "default runtime does not accept tmux script input")
}

func TestManagedStartRejectsEscapingLogDirectory(t *testing.T) {
	t.Parallel()
	if runtime.GOOS == "windows" {
		t.Skip("symlink permissions vary on Windows")
	}
	root := state.Root{Path: t.TempDir()}
	outside := t.TempDir()
	sentinel := filepath.Join(outside, "docs-mcp.log")
	require.NoError(t, os.WriteFile(sentinel, []byte("untouched"), 0o644))
	require.NoError(t, os.Symlink(outside, filepath.Join(root.Path, "logs")))
	_, err := StartOrReuseWithExistingRuntime(DefaultRuntime(), "docs-mcp", "docs-mcp", t.TempDir(), "", root, nil, ExistingRuntime{}, false)
	require.Error(t, err)
	data, err := os.ReadFile(sentinel)
	require.NoError(t, err)
	require.Equal(t, "untouched", string(data))
	info, err := os.Stat(sentinel)
	require.NoError(t, err)
	require.Equal(t, os.FileMode(0o644), info.Mode().Perm())
}

func TestStartTmuxAllowsExplicitLogDirectorySymlink(t *testing.T) {
	t.Parallel()
	if runtime.GOOS == "windows" {
		t.Skip("symlink permissions vary on Windows")
	}
	selectedDir := filepath.Join(t.TempDir(), "logs")
	volume := t.TempDir()
	require.NoError(t, os.Symlink(volume, selectedDir))
	called := false
	rt := Runtime{Run: func(args []string, env map[string]string) (CompletedProcess, error) {
		called = true
		return CompletedProcess{}, nil
	}}
	_, err := StartTmux(rt, "owned-session", "", "docs-mcp", t.TempDir(), nil, filepath.Join(selectedDir, "runtime.log"))
	require.NoError(t, err)
	require.True(t, called)
	info, err := os.Stat(filepath.Join(volume, "runtime.log"))
	require.NoError(t, err)
	require.Equal(t, os.FileMode(0o600), info.Mode().Perm())
}

func TestDefaultStarterAllowsExplicitLogDirectorySymlink(t *testing.T) {
	t.Parallel()
	if runtime.GOOS == "windows" {
		t.Skip("uses the Unix re-exec fixture and symlinks")
	}
	selectedDir := filepath.Join(t.TempDir(), "logs")
	volume := t.TempDir()
	require.NoError(t, os.Symlink(volume, selectedDir))
	args, err := currentTunnelClientInvocation("docs-mcp", t.TempDir())
	require.NoError(t, err)
	argsPath := filepath.Join(t.TempDir(), "args")
	// TestMain in session_exec_unix_test.go exits the child after recording argv.
	process, err := DefaultRuntime().Start(args, map[string]string{
		"TUNNEL_CLIENT_SESSION_TEST_REEXEC_ARGS_PATH": argsPath,
	}, filepath.Join(selectedDir, "runtime.log"))
	require.NoError(t, err)
	started := process.(*osProcess)
	t.Cleanup(func() { _ = started.Abort() })
	select {
	case <-started.done:
	case <-time.After(30 * time.Second):
		t.Fatal("re-exec fixture did not finish")
	}
	require.Equal(t, 0, *process.Poll())
	_, err = os.Stat(argsPath)
	require.NoError(t, err)
	info, err := os.Stat(filepath.Join(volume, "runtime.log"))
	require.NoError(t, err)
	require.Equal(t, os.FileMode(0o600), info.Mode().Perm())
}

func TestManagedStartPreservesDefaultRuntimeStartOverride(t *testing.T) {
	t.Parallel()
	root := state.Root{Path: t.TempDir()}
	profileDir := t.TempDir()
	args, err := currentTunnelClientInvocation("docs-mcp", profileDir)
	require.NoError(t, err)
	called := false
	rt := DefaultRuntime()
	rt.Start = func(gotArgs []string, env map[string]string, logPath string) (Process, error) {
		called = true
		require.Equal(t, args, gotArgs)
		require.Equal(t, "custom-value", env["CUSTOM_SETTING"])
		require.Equal(t, LogPath("docs-mcp", root), logPath)
		exitCode := 1
		return &fakeProcess{exitCode: &exitCode}, nil
	}
	result, err := StartOrReuseWithExistingRuntime(rt, "docs-mcp", "docs-mcp", profileDir, "", root, map[string]string{"CUSTOM_SETTING": "custom-value"}, ExistingRuntime{}, false)
	require.NoError(t, err)
	require.True(t, called)
	require.NotNil(t, result.ExitCode)
	require.Equal(t, 1, *result.ExitCode)
}

func TestGeneratedProfileDoesNotOverwriteOutsideSymlink(t *testing.T) {
	t.Parallel()
	if runtime.GOOS == "windows" {
		t.Skip("symlink permissions vary on Windows")
	}
	profiles := t.TempDir()
	outside := filepath.Join(t.TempDir(), "sentinel")
	require.NoError(t, os.WriteFile(outside, []byte("untouched"), 0o644))
	require.NoError(t, os.Symlink(outside, filepath.Join(profiles, "docs-mcp.yaml")))
	_, err := WriteRuntimeProfile("docs-mcp", "docs-mcp", "tunnel", "https://example.com", "", "test-key", Target{Kind: "url", Value: "https://example.com/mcp"}, profiles, state.Root{Path: t.TempDir()}, nil)
	require.Error(t, err)
	data, err := os.ReadFile(outside)
	require.NoError(t, err)
	require.Equal(t, "untouched", string(data))
}
