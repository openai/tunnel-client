//go:build windows

package session

import (
	"errors"
	"fmt"
	"os/exec"

	"golang.org/x/sys/windows"
)

const windowsStillActive = 259

func PIDIsRunning(pid int) bool {
	if pid <= 0 {
		return false
	}

	handle, err := windows.OpenProcess(windows.PROCESS_QUERY_LIMITED_INFORMATION, false, uint32(pid))
	if err != nil {
		return false
	}
	defer windows.CloseHandle(handle)

	var exitCode uint32
	if err := windows.GetExitCodeProcess(handle, &exitCode); err != nil {
		return false
	}
	return exitCode == windowsStillActive
}

func TerminateProcess(pid int) error {
	if pid <= 0 {
		return nil
	}

	handle, err := windows.OpenProcess(windows.PROCESS_QUERY_LIMITED_INFORMATION|windows.PROCESS_TERMINATE, false, uint32(pid))
	if err != nil {
		if errors.Is(err, windows.ERROR_INVALID_PARAMETER) {
			return nil
		}
		return fmt.Errorf("terminate process %d: %w", pid, err)
	}
	defer windows.CloseHandle(handle)

	if err := windows.TerminateProcess(handle, 1); err != nil {
		var exitCode uint32
		if errors.Is(err, windows.ERROR_ACCESS_DENIED) && windows.GetExitCodeProcess(handle, &exitCode) == nil && exitCode != windowsStillActive {
			return nil
		}
		return fmt.Errorf("terminate process %d: %w", pid, err)
	}
	return nil
}

func configureDetachedProcess(*exec.Cmd) {}
