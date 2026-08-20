package mockmcpserver

import (
	_ "embed"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
)

//go:embed stdio_server.sh
var stdioServerScript string

// StdioServerCommand returns the command args to launch the stdio MCP server script.
// It fails the test if the script cannot be prepared.
func StdioServerCommand(t testing.TB) []string {
	t.Helper()
	path, err := writeEmbeddedScript(t)
	if err != nil {
		t.Fatalf("stdio MCP server script unavailable: %v", err)
		return nil
	}
	bashPath, err := exec.LookPath("bash")
	if err != nil {
		t.Fatalf("bash not found in PATH: %v", err)
		return nil
	}
	return []string{bashPath, path}
}

func writeEmbeddedScript(t testing.TB) (string, error) {
	t.Helper()
	dir, err := tempDir(t)
	if err != nil {
		return "", err
	}
	path := filepath.Join(dir, "stdio_server.sh")
	if err := os.WriteFile(path, []byte(stdioServerScript), 0o700); err != nil {
		return "", fmt.Errorf("write stdio server script: %w", err)
	}
	if err := os.Chmod(path, 0o700); err != nil {
		return "", fmt.Errorf("chmod stdio server script: %w", err)
	}
	return path, nil
}

func tempDir(t testing.TB) (string, error) {
	t.Helper()
	if base := os.Getenv("TEST_TMPDIR"); base != "" {
		dir, err := os.MkdirTemp(base, "mcp-stdio-")
		if err != nil {
			return "", fmt.Errorf("create temp dir: %w", err)
		}
		t.Cleanup(func() { _ = os.RemoveAll(dir) })
		return dir, nil
	}
	return t.TempDir(), nil
}
