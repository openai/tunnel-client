package session

import (
	"io"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/openai/tunnel-client/pkg/codexplugin/state"
)

func TestOpenManagedFileKeepsOpenedLogPrivateAcrossDirectoryReplacement(t *testing.T) {
	t.Parallel()
	if runtime.GOOS == "windows" {
		t.Skip("open directory renames vary on Windows")
	}
	root := state.Root{Path: filepath.Join(t.TempDir(), "custom state")}
	logPath := LogPath("docs", root)
	file, err := OpenManagedFile(root, "logs", logPath, os.O_CREATE|os.O_WRONLY|os.O_APPEND)
	require.NoError(t, err)
	defer func() { _ = file.Close() }()
	_, err = file.WriteString("before\n")
	require.NoError(t, err)
	outside := t.TempDir()
	sentinel := filepath.Join(outside, "docs.log")
	require.NoError(t, os.WriteFile(sentinel, []byte("outside"), 0o644))
	require.NoError(t, os.Rename(filepath.Join(root.Path, "logs"), filepath.Join(root.Path, "old-logs")))
	require.NoError(t, os.Symlink(outside, filepath.Join(root.Path, "logs")))
	_, err = file.WriteString("after\n")
	require.NoError(t, err)
	info, err := file.Stat()
	require.NoError(t, err)
	require.Equal(t, os.FileMode(0o600), info.Mode().Perm())
	data, err := os.ReadFile(filepath.Join(root.Path, "old-logs", "docs.log"))
	require.NoError(t, err)
	require.Equal(t, "before\nafter\n", string(data))
	_, err = OpenManagedFile(root, "logs", logPath, os.O_CREATE|os.O_WRONLY|os.O_APPEND)
	require.Error(t, err)
	data, err = os.ReadFile(sentinel)
	require.NoError(t, err)
	require.Equal(t, "outside", string(data))
	info, err = os.Stat(sentinel)
	require.NoError(t, err)
	require.Equal(t, os.FileMode(0o644), info.Mode().Perm())
}

func TestOpenManagedFilePreservesRootSymlinkAndReadPermissions(t *testing.T) {
	t.Parallel()
	if runtime.GOOS == "windows" {
		t.Skip("symlink permissions vary on Windows")
	}
	realRoot := t.TempDir()
	selectedRoot := filepath.Join(t.TempDir(), "selected state")
	require.NoError(t, os.Symlink(realRoot, selectedRoot))
	root := state.Root{Path: selectedRoot}
	require.NoError(t, os.Mkdir(filepath.Join(realRoot, "logs"), 0o755))
	path := LogPath("docs", root)
	require.NoError(t, os.WriteFile(path, []byte("existing\n"), 0o644))
	file, err := OpenManagedFile(root, "logs", path, os.O_RDONLY)
	require.NoError(t, err)
	data, err := io.ReadAll(file)
	require.NoError(t, err)
	require.NoError(t, file.Close())
	require.Equal(t, "existing\n", string(data))
	info, err := os.Stat(path)
	require.NoError(t, err)
	require.Equal(t, os.FileMode(0o644), info.Mode().Perm())
	file, err = OpenManagedFile(root, "logs", path, os.O_WRONLY|os.O_APPEND)
	require.NoError(t, err)
	_, err = file.WriteString("appended\n")
	require.NoError(t, err)
	require.NoError(t, file.Close())
	info, err = os.Stat(path)
	require.NoError(t, err)
	require.Equal(t, os.FileMode(0o600), info.Mode().Perm())
	data, err = os.ReadFile(path)
	require.NoError(t, err)
	require.Equal(t, "existing\nappended\n", string(data))
}

func TestOpenManagedFileRejectsLogSymlinksAndOutsidePaths(t *testing.T) {
	t.Parallel()
	if runtime.GOOS == "windows" {
		t.Skip("symlink permissions vary on Windows")
	}
	root := state.Root{Path: t.TempDir()}
	require.NoError(t, state.EnsureDirs(root))
	inside := filepath.Join(root.Path, "logs", "target.log")
	outside := filepath.Join(t.TempDir(), "target.log")
	for _, path := range []string{inside, outside} {
		require.NoError(t, os.WriteFile(path, []byte("unchanged"), 0o644))
	}
	for _, target := range []string{"target.log", outside} {
		path := LogPath("docs", root)
		require.NoError(t, os.Symlink(target, path))
		_, err := OpenManagedFile(root, "logs", path, os.O_CREATE|os.O_WRONLY|os.O_TRUNC)
		require.ErrorContains(t, err, "must not be a symlink")
		require.NoError(t, os.Remove(path))
	}
	for _, path := range []string{outside, filepath.Join(root.Path, "target.log"), filepath.Join(root.Path, "logs", "nested", "target.log")} {
		_, err := OpenManagedFile(root, "logs", path, os.O_CREATE|os.O_WRONLY)
		require.Error(t, err)
	}
	for _, path := range []string{inside, outside} {
		data, err := os.ReadFile(path)
		require.NoError(t, err)
		require.Equal(t, "unchanged", string(data))
		info, err := os.Stat(path)
		require.NoError(t, err)
		require.Equal(t, os.FileMode(0o644), info.Mode().Perm())
	}
}

func TestReadManagedHealthURLLimitsRegularFiles(t *testing.T) {
	t.Parallel()
	root := state.Root{Path: t.TempDir()}
	require.NoError(t, state.EnsureDirs(root))
	path := ProfileHealthURLFile("docs", root)
	require.Empty(t, ReadManagedHealthURL(root, path))
	for _, value := range []string{"http://127.0.0.1:1234/healthz", "http+unix://%2Ftmp%2Fhealth.sock/healthz"} {
		require.NoError(t, os.WriteFile(path, []byte(value+"\n"), 0o600))
		require.Equal(t, value, ReadManagedHealthURL(root, path))
	}
	require.NoError(t, os.WriteFile(path, []byte(strings.Repeat("x", maxManagedHealthURLBytes)), 0o600))
	require.Len(t, ReadManagedHealthURL(root, path), maxManagedHealthURLBytes)
	require.NoError(t, os.WriteFile(path, []byte(strings.Repeat("x", maxManagedHealthURLBytes+1)), 0o600))
	require.Empty(t, ReadManagedHealthURL(root, path))
	require.NoError(t, os.Remove(path))
	require.NoError(t, os.Mkdir(path, 0o700))
	require.Empty(t, ReadManagedHealthURL(root, path))
}

func TestRemoveManagedFileConfinesParentAndRemovesOnlyLeafLink(t *testing.T) {
	t.Parallel()
	if runtime.GOOS == "windows" {
		t.Skip("symlink permissions vary on Windows")
	}
	root := state.Root{Path: t.TempDir()}
	require.NoError(t, state.EnsureDirs(root))
	outside := t.TempDir()
	sentinel := filepath.Join(outside, "docs.url")
	require.NoError(t, os.WriteFile(sentinel, []byte("outside"), 0o600))
	path := ProfileHealthURLFile("docs", root)
	require.NoError(t, os.Symlink(sentinel, path))
	require.NoError(t, RemoveManagedFile(root, "health", path))
	require.Error(t, RemoveManagedFile(root, "health", sentinel))
	require.NoError(t, os.Remove(filepath.Join(root.Path, "health")))
	require.NoError(t, os.Symlink(outside, filepath.Join(root.Path, "health")))
	require.Error(t, RemoveManagedFile(root, "health", path))
	data, err := os.ReadFile(sentinel)
	require.NoError(t, err)
	require.Equal(t, "outside", string(data))
}

func TestManagedFilesAcceptEquivalentStateRootSpellings(t *testing.T) {
	t.Parallel()
	if runtime.GOOS == "windows" {
		t.Skip("symlink permissions vary on Windows")
	}
	for _, selectedSpelling := range []string{"canonical", "symlink"} {
		for _, directory := range []string{"health", "logs"} {
			for _, operation := range []string{"read", "info", "remove"} {
				t.Run(selectedSpelling+"/"+directory+"/"+operation, func(t *testing.T) {
					t.Parallel()
					realRoot := t.TempDir()
					aliasRoot := filepath.Join(t.TempDir(), "state alias")
					require.NoError(t, os.Symlink(realRoot, aliasRoot))
					selectedRoot, persistedRoot := realRoot, aliasRoot
					if selectedSpelling == "symlink" {
						selectedRoot, persistedRoot = aliasRoot, realRoot
					}
					root := state.Root{Path: selectedRoot}
					require.NoError(t, state.EnsureDirs(root))
					path := filepath.Join(persistedRoot, directory, "docs.url")
					contents := "http://127.0.0.1:1234/healthz"
					require.NoError(t, os.WriteFile(path, []byte(contents), 0o600))
					switch operation {
					case "read":
						file, err := OpenManagedFile(root, directory, path, os.O_RDONLY)
						require.NoError(t, err)
						defer func() { _ = file.Close() }()
						data, err := io.ReadAll(file)
						require.NoError(t, err)
						require.Equal(t, contents, string(data))
						if directory == "health" {
							require.Equal(t, contents, ReadManagedHealthURL(root, path))
						}
					case "info":
						info, err := ManagedFileInfo(root, directory, path)
						require.NoError(t, err)
						require.Equal(t, int64(len(contents)), info.Size())
					case "remove":
						require.NoError(t, RemoveManagedFile(root, directory, path))
						_, err := os.Stat(path)
						require.True(t, os.IsNotExist(err))
					}
				})
			}
		}
	}
}

func TestManagedHealthFileRejectsSiblingEscape(t *testing.T) {
	t.Parallel()
	if runtime.GOOS == "windows" {
		t.Skip("symlink permissions vary on Windows")
	}
	root := state.Root{Path: t.TempDir()}
	require.NoError(t, state.EnsureDirs(root))
	contents := "private admin profile material"
	sibling := filepath.Join(root.Path, "admin_profiles.yaml")
	require.NoError(t, os.WriteFile(sibling, []byte(contents), 0o600))
	path := ProfileHealthURLFile("docs", root)
	require.NoError(t, os.Symlink("../admin_profiles.yaml", path))
	t.Run("read", func(t *testing.T) {
		require.Empty(t, ReadManagedHealthURL(root, path))
	})
	t.Run("info", func(t *testing.T) {
		_, err := ManagedFileInfo(root, "health", path)
		require.Error(t, err)
	})
	require.NoError(t, RemoveManagedFile(root, "health", path))
	data, err := os.ReadFile(sibling)
	require.NoError(t, err)
	require.Equal(t, contents, string(data))
}

func TestManagedHealthFilePreservesAliasWithinHealthDirectory(t *testing.T) {
	t.Parallel()
	if runtime.GOOS == "windows" {
		t.Skip("symlink permissions vary on Windows")
	}
	root := state.Root{Path: t.TempDir()}
	require.NoError(t, state.EnsureDirs(root))
	contents := "http://127.0.0.1:1234/healthz"
	target := ProfileHealthURLFile("target", root)
	alias := ProfileHealthURLFile("docs", root)
	require.NoError(t, os.WriteFile(target, []byte(contents), 0o600))
	require.NoError(t, os.Symlink("target.url", alias))
	require.Equal(t, contents, ReadManagedHealthURL(root, alias))
	info, err := ManagedFileInfo(root, "health", alias)
	require.NoError(t, err)
	require.Equal(t, int64(len(contents)), info.Size())
	require.NoError(t, RemoveManagedFile(root, "health", alias))
	require.Equal(t, contents, ReadManagedHealthURL(root, target))
}

func TestManagedHealthDirectoryRejectsStateRootAlias(t *testing.T) {
	t.Parallel()
	if runtime.GOOS == "windows" {
		t.Skip("symlink permissions vary on Windows")
	}
	for _, operation := range []string{"read", "info", "remove", "list"} {
		t.Run(operation, func(t *testing.T) {
			t.Parallel()
			root := state.Root{Path: t.TempDir()}
			sibling := filepath.Join(root.Path, "admin_profiles.yaml")
			require.NoError(t, os.WriteFile(sibling, []byte("private admin profile material"), 0o600))
			require.NoError(t, os.Symlink(".", filepath.Join(root.Path, "health")))
			path := filepath.Join(root.Path, "health", "admin_profiles.yaml")
			switch operation {
			case "read":
				require.Empty(t, ReadManagedHealthURL(root, path))
			case "info":
				_, err := ManagedFileInfo(root, "health", path)
				require.Error(t, err)
			case "remove":
				require.Error(t, RemoveManagedFile(root, "health", path))
			case "list":
				_, err := ManagedFileNames(root, "health")
				require.Error(t, err)
			}
		})
	}
}
