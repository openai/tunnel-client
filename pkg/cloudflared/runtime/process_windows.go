//go:build windows

package runtime

import "os/exec"

func configureChildProcess(_ *exec.Cmd) {}
