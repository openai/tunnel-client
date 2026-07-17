//go:build !windows

package cloudflared

import (
	"os/exec"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestConfigureChildProcessStartsNewSession(t *testing.T) {
	t.Parallel()

	cmd := exec.Command("cloudflared")
	configureChildProcess(cmd)

	require.NotNil(t, cmd.SysProcAttr)
	require.True(t, cmd.SysProcAttr.Setsid)
}
