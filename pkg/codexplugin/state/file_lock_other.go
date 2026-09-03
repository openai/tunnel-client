//go:build !darwin && !dragonfly && !freebsd && !illumos && !linux && !netbsd && !openbsd && !windows

package state

import "fmt"

func acquireFileLock(path string) (func() error, error) {
	return nil, fmt.Errorf("state file locking is unsupported on this platform: %s", path)
}
