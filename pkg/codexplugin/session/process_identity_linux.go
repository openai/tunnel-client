//go:build linux

package session

import (
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
)

// CaptureProcessIdentity reads a Linux process's kernel start tick and
// executable path. Reading the start tick on both sides of the executable
// lookup prevents returning a mixed identity if the PID is reused mid-read.
func CaptureProcessIdentity(pid int) (ProcessIdentity, error) {
	if pid <= 0 {
		return ProcessIdentity{}, fmt.Errorf("process pid must be positive")
	}
	firstStart, err := linuxProcessStartTime(pid)
	if err != nil {
		return ProcessIdentity{}, err
	}
	executable, err := os.Readlink(filepath.Join("/proc", strconv.Itoa(pid), "exe"))
	if err != nil {
		if os.IsNotExist(err) {
			return ProcessIdentity{}, fmt.Errorf("inspect process %d executable: %w", pid, os.ErrNotExist)
		}
		return ProcessIdentity{}, fmt.Errorf("inspect process %d executable: %w", pid, err)
	}
	secondStart, err := linuxProcessStartTime(pid)
	if err != nil {
		return ProcessIdentity{}, err
	}
	if firstStart != secondStart {
		return ProcessIdentity{}, fmt.Errorf("process %d changed while capturing identity", pid)
	}
	return ProcessIdentity{
		StartTime: firstStart,
		// Linux appends this marker after an on-disk upgrade unlinks the
		// running image. It is not part of the executable identity.
		Executable: normalizeProcessExecutable(strings.TrimSuffix(executable, " (deleted)")),
	}, nil
}

func linuxProcessStartTime(pid int) (string, error) {
	data, err := os.ReadFile(filepath.Join("/proc", strconv.Itoa(pid), "stat"))
	if err != nil {
		if os.IsNotExist(err) {
			return "", fmt.Errorf("inspect process %d start time: %w", pid, os.ErrNotExist)
		}
		return "", fmt.Errorf("inspect process %d start time: %w", pid, err)
	}
	// The comm field is parenthesized and may itself contain spaces or ')'.
	closeParen := strings.LastIndexByte(string(data), ')')
	if closeParen < 0 || closeParen+1 >= len(data) {
		return "", fmt.Errorf("inspect process %d start time: malformed /proc stat", pid)
	}
	fields := strings.Fields(string(data[closeParen+1:]))
	// After comm, fields[0] is stat field 3 (state); starttime is field 22.
	if len(fields) <= 19 {
		return "", fmt.Errorf("inspect process %d start time: malformed /proc stat", pid)
	}
	return fields[19], nil
}
