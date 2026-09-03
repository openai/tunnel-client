package state

import (
	"fmt"
	"os"
	"path/filepath"
)

// StateLockPath returns the persistent lock-file path used to serialize
// whole-map state-file mutations. aliases.yaml and processes.yaml are each
// rewritten as complete maps, so different aliases must share this lock.
func StateLockPath(root Root) string {
	return filepath.Join(root.Path, "locks", "state.lock")
}

// AcquireStateLock blocks until the state root can be mutated exclusively.
// Hold it across read/launch/persist sequences so another connect cannot
// clobber a whole-map state file or start a duplicate runtime. The lock file
// is intentionally persistent: removing it while another process holds the
// lock could allow a later caller to lock a different inode.
func AcquireStateLock(root Root) (func() error, error) {
	path := StateLockPath(root)
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return nil, fmt.Errorf("create state lock directory for %s: %w", path, err)
	}
	return acquirePersistentLock(path)
}

func acquirePersistentLock(path string) (func() error, error) {
	release, err := acquireFileLock(path)
	if err != nil {
		return nil, fmt.Errorf("acquire state lock %s: %w", path, err)
	}
	return func() error {
		if err := release(); err != nil {
			return fmt.Errorf("release state lock %s: %w", path, err)
		}
		return nil
	}, nil
}
