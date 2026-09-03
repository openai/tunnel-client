//go:build darwin

package session

import (
	"bytes"
	"fmt"
	"os"

	"golang.org/x/sys/unix"
)

// CaptureProcessIdentity reads a Darwin process's kernel start timeval and
// executable path from kern.procargs2. The start timeval is checked before and
// after the path lookup so a reused PID cannot produce a mixed identity.
func CaptureProcessIdentity(pid int) (ProcessIdentity, error) {
	if pid <= 0 {
		return ProcessIdentity{}, fmt.Errorf("process pid must be positive")
	}
	firstStart, err := darwinProcessStartTime(pid)
	if err != nil {
		return ProcessIdentity{}, err
	}
	executable, err := darwinProcessExecutable(pid)
	if err != nil {
		return ProcessIdentity{}, err
	}
	secondStart, err := darwinProcessStartTime(pid)
	if err != nil {
		return ProcessIdentity{}, err
	}
	if firstStart != secondStart {
		return ProcessIdentity{}, fmt.Errorf("process %d changed while capturing identity", pid)
	}
	return ProcessIdentity{
		StartTime:  firstStart,
		Executable: normalizeProcessExecutable(executable),
	}, nil
}

func darwinProcessStartTime(pid int) (string, error) {
	info, err := unix.SysctlKinfoProc("kern.proc.pid", pid)
	if err != nil {
		if err == unix.ENOENT || err == unix.ESRCH {
			return "", fmt.Errorf("inspect process %d start time: %w", pid, os.ErrNotExist)
		}
		return "", fmt.Errorf("inspect process %d start time: %w", pid, err)
	}
	if info == nil || info.Proc.P_pid != int32(pid) {
		return "", fmt.Errorf("inspect process %d start time: %w", pid, os.ErrNotExist)
	}
	start := info.Proc.P_starttime
	return fmt.Sprintf("%d:%d", start.Sec, start.Usec), nil
}

func darwinProcessExecutable(pid int) (string, error) {
	raw, err := unix.SysctlRaw("kern.procargs2", pid)
	if err != nil {
		if err == unix.ENOENT || err == unix.ESRCH {
			return "", fmt.Errorf("inspect process %d executable: %w", pid, os.ErrNotExist)
		}
		return "", fmt.Errorf("inspect process %d executable: %w", pid, err)
	}
	// kern.procargs2 begins with a native int argc, followed by a NUL-
	// terminated executable path.
	if len(raw) <= 4 {
		return "", fmt.Errorf("inspect process %d executable: malformed kern.procargs2", pid)
	}
	pathBytes := raw[4:]
	end := bytes.IndexByte(pathBytes, 0)
	if end <= 0 {
		return "", fmt.Errorf("inspect process %d executable: malformed kern.procargs2", pid)
	}
	return string(pathBytes[:end]), nil
}
