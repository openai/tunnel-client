//go:build !windows

package version

import (
	"os"
	"path/filepath"
	"runtime/debug"
	"testing"
)

// TestVersionDoesNotSpawnGit proves version initialization is static even
// when no linked SHA or Go build revision is available.
func TestVersionDoesNotSpawnGit(t *testing.T) {
	tempDir := t.TempDir()
	markerPath := filepath.Join(tempDir, "injected")
	gitPath := filepath.Join(tempDir, "git")
	script := "#!/bin/sh\n: > \"$TC_GIT_MARKER\"\nprintf 'deadbeef\\n'\n"
	if err := os.WriteFile(gitPath, []byte(script), 0o700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", tempDir+string(os.PathListSeparator)+os.Getenv("PATH"))
	t.Setenv("TC_GIT_MARKER", markerPath)

	restoreVersionGlobals(t)
	GitSHA = ""
	GoVersion = ""
	Flavor = FlavorRuntime
	initVersion(func() (*debug.BuildInfo, bool) { return nil, false })

	if _, err := os.Stat(markerPath); !os.IsNotExist(err) {
		t.Fatalf("version initialization unexpectedly started git: %v", err)
	}
}
