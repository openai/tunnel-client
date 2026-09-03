package session

import (
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"time"
)

// ErrProcessIdentityUnsupported is returned on platforms where tunnel-client
// cannot safely prove that a PID still names the process it launched.
var ErrProcessIdentityUnsupported = errors.New("stable process identity is unsupported on this platform")

// ProcessIdentityMatches reports whether pid still names the exact process
// described by expected. An incomplete identity never matches: falling back to
// a bare PID would make PID reuse unsafe.
func ProcessIdentityMatches(pid int, expected ProcessIdentity) (bool, error) {
	if pid <= 0 || !PIDIsRunning(pid) || !processIdentityComplete(expected) {
		return false, nil
	}
	actual, err := CaptureProcessIdentity(pid)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) || !PIDIsRunning(pid) {
			return false, nil
		}
		return false, err
	}
	return actual.StartTime == expected.StartTime &&
		processExecutableEqual(actual.Executable, expected.Executable), nil
}

// WaitForProcessIdentityExit waits until pid no longer names expected. A PID
// reused by a different process counts as exited; an inspection error does not.
func WaitForProcessIdentityExit(pid int, expected ProcessIdentity) bool {
	if pid <= 0 || !processIdentityComplete(expected) {
		return false
	}
	deadline := time.Now().Add(terminateWaitDuration)
	for time.Now().Before(deadline) {
		matches, err := ProcessIdentityMatches(pid, expected)
		if err != nil {
			time.Sleep(50 * time.Millisecond)
			continue
		}
		if !matches {
			return true
		}
		time.Sleep(50 * time.Millisecond)
	}
	matches, err := ProcessIdentityMatches(pid, expected)
	return err == nil && !matches
}

func processIdentityComplete(identity ProcessIdentity) bool {
	return strings.TrimSpace(identity.StartTime) != "" &&
		strings.TrimSpace(identity.Executable) != ""
}

func normalizeProcessExecutable(pathValue string) string {
	return filepath.Clean(strings.TrimSpace(pathValue))
}

func processExecutableEqual(first string, second string) bool {
	first = normalizeProcessExecutable(first)
	second = normalizeProcessExecutable(second)
	if runtime.GOOS == "windows" {
		return strings.EqualFold(first, second)
	}
	return first == second
}
