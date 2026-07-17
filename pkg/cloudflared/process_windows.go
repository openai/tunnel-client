//go:build windows

package cloudflared

import "os/exec"

func configureChildProcess(_ *exec.Cmd) {}
