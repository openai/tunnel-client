//go:build unix

package localfiles

import (
	"os"
	"path/filepath"
	"syscall"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestRegularFileOperationsRejectFIFOWithoutBlocking(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	root := openTestRoot(t, dir)
	require.NoError(t, syscall.Mkfifo(filepath.Join(dir, "pipe"), 0o600))
	done := make(chan error, 1)
	go func() {
		_, err := ReadFile(root, "pipe")
		done <- err
	}()
	select {
	case err := <-done:
		require.Error(t, err)
	case <-time.After(5 * time.Second):
		t.Fatal("reading a FIFO blocked")
	}
	file, err := OpenRegular(root, "pipe", os.O_WRONLY|os.O_TRUNC, 0o600)
	require.Error(t, err)
	require.Nil(t, file)
	_, err = ReadDir(root, "pipe")
	require.Error(t, err)
	require.Error(t, WriteFile(root, "pipe", []byte("rejected"), true))
	info, err := root.Lstat("pipe")
	require.NoError(t, err)
	require.NotZero(t, info.Mode()&os.ModeNamedPipe)
}
