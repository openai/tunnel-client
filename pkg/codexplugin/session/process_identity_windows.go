//go:build windows

package session

import (
	"errors"
	"fmt"
	"os"

	"golang.org/x/sys/windows"
)

// CaptureProcessIdentity reads creation time and full image path from one
// Windows process handle, so both fields necessarily describe one process.
func CaptureProcessIdentity(pid int) (ProcessIdentity, error) {
	if pid <= 0 {
		return ProcessIdentity{}, fmt.Errorf("process pid must be positive")
	}
	handle, err := windows.OpenProcess(windows.PROCESS_QUERY_LIMITED_INFORMATION, false, uint32(pid))
	if err != nil {
		if errors.Is(err, windows.ERROR_INVALID_PARAMETER) {
			return ProcessIdentity{}, fmt.Errorf("inspect process %d identity: %w", pid, os.ErrNotExist)
		}
		return ProcessIdentity{}, fmt.Errorf("inspect process %d identity: %w", pid, err)
	}
	defer windows.CloseHandle(handle)

	var creation, exit, kernel, user windows.Filetime
	if err := windows.GetProcessTimes(handle, &creation, &exit, &kernel, &user); err != nil {
		return ProcessIdentity{}, fmt.Errorf("inspect process %d start time: %w", pid, err)
	}
	buffer := make([]uint16, windows.MAX_LONG_PATH)
	size := uint32(len(buffer))
	if err := windows.QueryFullProcessImageName(handle, 0, &buffer[0], &size); err != nil {
		return ProcessIdentity{}, fmt.Errorf("inspect process %d executable: %w", pid, err)
	}
	if size == 0 {
		return ProcessIdentity{}, fmt.Errorf("inspect process %d executable: empty image path", pid)
	}
	return ProcessIdentity{
		StartTime:  fmt.Sprintf("%d", creation.Nanoseconds()),
		Executable: normalizeProcessExecutable(windows.UTF16ToString(buffer[:size])),
	}, nil
}
