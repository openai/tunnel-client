//go:build !windows

package session

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

const reexecArgsPathEnv = "TUNNEL_CLIENT_SESSION_TEST_REEXEC_ARGS_PATH"

func TestMain(m *testing.M) {
	if argsPath := os.Getenv(reexecArgsPathEnv); argsPath != "" {
		data := strings.Join(os.Args[1:], "\n") + "\n"
		if err := os.WriteFile(argsPath, []byte(data), 0o600); err != nil {
			os.Exit(2)
		}
		os.Exit(0)
	}
	os.Exit(m.Run())
}

// TestStartProcessReexecsCurrentExecutableWithFixedArgs covers the direct
// Cmd.Start path. The payload must reach the re-executed tunnel-client as one
// literal profile-dir argument, not shell syntax or a selectable executable.
func TestStartProcessReexecsCurrentExecutableWithFixedArgs(t *testing.T) {
	tempDir := t.TempDir()
	markerPath := filepath.Join(tempDir, "injected")
	argsPath := filepath.Join(tempDir, "argv")
	payload := "profile;:>" + markerPath
	logPath := filepath.Join(tempDir, "runtime.log")
	executable, err := os.Executable()
	require.NoError(t, err)

	process, err := startProcess(
		append([]string{executable}, tunnelClientRunArgs("profile", payload)...),
		map[string]string{reexecArgsPathEnv: argsPath},
		logPath,
	)
	require.NoError(t, err)

	osProcess, ok := process.(*osProcess)
	require.True(t, ok)
	require.Equal(t, executable, osProcess.cmd.Path)
	require.Equal(t, append([]string{executable}, tunnelClientRunArgs("profile", payload)...), osProcess.cmd.Args)
	select {
	case <-osProcess.done:
		exitCode := osProcess.Poll()
		require.NotNil(t, exitCode)
		require.Zero(t, *exitCode)
	case <-time.After(30 * time.Second):
		t.Fatal("timed out waiting for process exit")
	}

	require.NoFileExists(t, markerPath)
	require.Equal(t, strings.Join(tunnelClientRunArgs("profile", payload), "\n")+"\n", readFile(t, argsPath))
}

func readFile(t *testing.T, path string) string {
	t.Helper()

	data, err := os.ReadFile(path)
	require.NoError(t, err)
	return string(data)
}
