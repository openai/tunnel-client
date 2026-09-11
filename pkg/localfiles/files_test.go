package localfiles

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

func openTestRoot(t *testing.T, dir string) *os.Root {
	t.Helper()
	root, err := os.OpenRoot(dir)
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, root.Close()) })
	return root
}

func TestReadFileConfinesPathsAndAllowsRelativeAliases(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	rootDir := filepath.Join(dir, "profiles")
	require.NoError(t, os.Mkdir(rootDir, 0o700))
	root := openTestRoot(t, rootDir)
	require.NoError(t, root.WriteFile("valid.yaml", []byte("valid"), 0o640))
	before, err := root.Stat("valid.yaml")
	require.NoError(t, err)
	outside := filepath.Join(dir, "outside.yaml")
	require.NoError(t, os.WriteFile(outside, []byte("outside"), 0o600))
	for _, name := range []string{"../outside.yaml", outside, "."} {
		_, err := ReadFile(root, name)
		require.Error(t, err, name)
	}
	data, err := ReadFile(root, "valid.yaml")
	require.NoError(t, err)
	require.Equal(t, "valid", string(data))
	info, err := root.Stat("valid.yaml")
	require.NoError(t, err)
	if runtime.GOOS != "windows" {
		require.Equal(t, before.Mode().Perm(), info.Mode().Perm(), "reading must not chmod")
	}
	if runtime.GOOS == "windows" {
		return
	}
	require.NoError(t, root.Symlink("valid.yaml", "alias.yaml"))
	data, err = ReadFile(root, "alias.yaml")
	require.NoError(t, err)
	require.Equal(t, "valid", string(data))
	require.NoError(t, root.Symlink("../outside.yaml", "escape.yaml"))
	require.NoError(t, root.Symlink(filepath.Join(rootDir, "valid.yaml"), "absolute.yaml"))
	for _, name := range []string{"escape.yaml", "absolute.yaml"} {
		_, err := ReadFile(root, name)
		require.Error(t, err, name)
	}
}

func TestOpenRegularRejectsEscapesBeforeTruncation(t *testing.T) {
	t.Parallel()
	if runtime.GOOS == "windows" {
		t.Skip("symlink permissions vary on Windows")
	}
	dir := t.TempDir()
	rootDir := filepath.Join(dir, "profiles")
	require.NoError(t, os.Mkdir(rootDir, 0o700))
	root := openTestRoot(t, rootDir)
	outside := filepath.Join(dir, "outside")
	require.NoError(t, os.WriteFile(outside, []byte("unchanged"), 0o600))
	require.NoError(t, root.Symlink("../outside", "escape"))
	file, err := OpenRegular(root, "escape", os.O_WRONLY|os.O_TRUNC, 0o600)
	require.Error(t, err)
	require.Nil(t, file)
	data, err := os.ReadFile(outside)
	require.NoError(t, err)
	require.Equal(t, "unchanged", string(data))
}

func TestWriteFileExclusiveAndOverwrite(t *testing.T) {
	t.Parallel()
	root := openTestRoot(t, t.TempDir())
	require.NoError(t, WriteFile(root, "profile.yaml", []byte("original"), false))
	err := WriteFile(root, "profile.yaml", []byte("rejected"), false)
	require.ErrorIs(t, err, os.ErrExist)
	data, err := ReadFile(root, "profile.yaml")
	require.NoError(t, err)
	require.Equal(t, "original", string(data))
	require.NoError(t, root.Chmod("profile.yaml", 0o644))
	require.NoError(t, WriteFile(root, "profile.yaml", []byte("updated"), true))
	data, err = ReadFile(root, "profile.yaml")
	require.NoError(t, err)
	require.Equal(t, "updated", string(data))
	info, err := root.Stat("profile.yaml")
	require.NoError(t, err)
	if runtime.GOOS != "windows" {
		require.Equal(t, os.FileMode(0o600), info.Mode().Perm())
	}
	entries, err := os.ReadDir(root.Name())
	require.NoError(t, err)
	require.Len(t, entries, 1, "staging files must be removed")
}

func TestReplaceFilePublishesNewFile(t *testing.T) {
	t.Parallel()
	root := openTestRoot(t, t.TempDir())
	require.NoError(t, root.WriteFile("profile.yaml", []byte("original contents"), 0o644))
	before, err := root.Stat("profile.yaml")
	require.NoError(t, err)
	require.NoError(t, ReplaceFile(root, "profile.yaml", []byte("updated")))
	after, err := root.Stat("profile.yaml")
	require.NoError(t, err)
	require.False(t, os.SameFile(before, after), "replacement must publish the staged file")
	data, err := ReadFile(root, "profile.yaml")
	require.NoError(t, err)
	require.Equal(t, "updated", string(data))
	if runtime.GOOS != "windows" {
		require.Equal(t, os.FileMode(0o600), after.Mode().Perm())
	}
	entries, err := ReadDir(root, ".")
	require.NoError(t, err)
	require.Len(t, entries, 1, "staging files must be removed")
}

func TestWriteFileWindowsPreservesExistingFile(t *testing.T) {
	t.Parallel()
	if runtime.GOOS != "windows" {
		t.Skip("Windows overwrites preserve the existing file and its access controls")
	}
	root := openTestRoot(t, t.TempDir())
	require.NoError(t, WriteFile(root, "profile.yaml", []byte("original contents"), true))
	before, err := root.Stat("profile.yaml")
	require.NoError(t, err)
	require.NoError(t, WriteFile(root, "profile.yaml", []byte("updated"), true))
	after, err := root.Stat("profile.yaml")
	require.NoError(t, err)
	require.True(t, os.SameFile(before, after), "overwrite must preserve the file that owns the ACL")
	require.Equal(t, before.Mode().Perm(), after.Mode().Perm())
	data, err := ReadFile(root, "profile.yaml")
	require.NoError(t, err)
	require.Equal(t, "updated", string(data))
}

func TestWriteFileWindowsRejectsReadOnlyFile(t *testing.T) {
	t.Parallel()
	if runtime.GOOS != "windows" {
		t.Skip("Windows read-only file attributes")
	}
	root := openTestRoot(t, t.TempDir())
	require.NoError(t, root.WriteFile("profile.yaml", []byte("original"), 0o600))
	require.NoError(t, root.Chmod("profile.yaml", 0o400))
	t.Cleanup(func() { require.NoError(t, root.Chmod("profile.yaml", 0o600)) })
	before, err := root.Stat("profile.yaml")
	require.NoError(t, err)
	err = WriteFile(root, "profile.yaml", []byte("rejected"), true)
	require.True(t, os.IsPermission(err), "expected a permission error, got %v", err)
	after, err := root.Stat("profile.yaml")
	require.NoError(t, err)
	require.True(t, os.SameFile(before, after))
	require.Equal(t, before.Mode().Perm(), after.Mode().Perm())
	data, err := ReadFile(root, "profile.yaml")
	require.NoError(t, err)
	require.Equal(t, "original", string(data))
}

func TestWriteFileConfinesSymlinksAndReplacesHardlinks(t *testing.T) {
	t.Parallel()
	if runtime.GOOS == "windows" {
		t.Skip("symlink permissions vary on Windows")
	}
	dir := t.TempDir()
	rootDir := filepath.Join(dir, "profiles")
	require.NoError(t, os.Mkdir(rootDir, 0o700))
	root := openTestRoot(t, rootDir)
	outside := filepath.Join(dir, "outside.yaml")
	require.NoError(t, os.WriteFile(outside, []byte("outside"), 0o600))
	require.NoError(t, root.Symlink("../outside.yaml", "escape.yaml"))
	require.Error(t, WriteFile(root, "escape.yaml", []byte("rejected"), true))
	require.Error(t, WriteFile(root, "../outside.yaml", []byte("rejected"), true))
	require.NoError(t, os.Link(outside, filepath.Join(rootDir, "hardlink.yaml")))
	require.NoError(t, WriteFile(root, "hardlink.yaml", []byte("replacement"), true))
	data, err := os.ReadFile(outside)
	require.NoError(t, err)
	require.Equal(t, "outside", string(data))
	require.NoError(t, root.WriteFile("target.yaml", []byte("target"), 0o600))
	require.NoError(t, root.Symlink("target.yaml", "alias.yaml"))
	require.NoError(t, WriteFile(root, "alias.yaml", []byte("replacement"), true))
	info, err := root.Lstat("alias.yaml")
	require.NoError(t, err)
	require.True(t, info.Mode().IsRegular())
	data, err = ReadFile(root, "target.yaml")
	require.NoError(t, err)
	require.Equal(t, "target", string(data))
}

func TestCreateTempAndSelectedRootSymlink(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	selectedDir := filepath.Join(dir, "profile directory")
	require.NoError(t, os.Mkdir(selectedDir, 0o700))
	if runtime.GOOS != "windows" {
		link := filepath.Join(dir, "selected")
		require.NoError(t, os.Symlink(selectedDir, link))
		selectedDir = link
	}
	root := openTestRoot(t, selectedDir)
	file, name, err := CreateTemp(root, ".sample.*.yaml")
	require.NoError(t, err)
	require.True(t, strings.HasPrefix(name, ".sample."))
	require.True(t, strings.HasSuffix(name, ".yaml"))
	require.Equal(t, name, filepath.Base(name))
	require.NoError(t, file.Close())
	info, err := root.Stat(name)
	require.NoError(t, err)
	if runtime.GOOS != "windows" {
		require.Equal(t, os.FileMode(0o600), info.Mode().Perm())
	}
	for _, pattern := range []string{"../escape", `..\escape`} {
		file, _, err := CreateTemp(root, pattern)
		require.Error(t, err)
		require.Nil(t, file)
	}
}
