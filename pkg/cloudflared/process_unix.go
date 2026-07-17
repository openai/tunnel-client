//go:build !windows

package cloudflared

import (
	"os/exec"
	"syscall"
)

// configureChildProcess keeps terminal signals delivered to tunnel-client
// from racing the supervisor's orderly shutdown path.
func configureChildProcess(cmd *exec.Cmd) {
	if cmd == nil {
		return
	}
	if cmd.SysProcAttr == nil {
		cmd.SysProcAttr = &syscall.SysProcAttr{}
	}
	cmd.SysProcAttr.Setsid = true
}
