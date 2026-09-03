package session

import (
	"os"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestCaptureProcessIdentityMatchesCurrentProcess(t *testing.T) {
	t.Parallel()

	identity, err := CaptureProcessIdentity(os.Getpid())
	require.NoError(t, err)
	require.NotEmpty(t, identity.StartTime)
	require.NotEmpty(t, identity.Executable)

	matches, err := ProcessIdentityMatches(os.Getpid(), identity)
	require.NoError(t, err)
	require.True(t, matches)
}

func TestProcessIdentityMatchesFailsClosedForChangedOrIncompleteIdentity(t *testing.T) {
	t.Parallel()

	identity, err := CaptureProcessIdentity(os.Getpid())
	require.NoError(t, err)

	changedStart := identity
	changedStart.StartTime += "-different"
	matches, err := ProcessIdentityMatches(os.Getpid(), changedStart)
	require.NoError(t, err)
	require.False(t, matches)

	changedExecutable := identity
	changedExecutable.Executable += "-different"
	matches, err = ProcessIdentityMatches(os.Getpid(), changedExecutable)
	require.NoError(t, err)
	require.False(t, matches)

	matches, err = ProcessIdentityMatches(os.Getpid(), ProcessIdentity{})
	require.NoError(t, err)
	require.False(t, matches)
}

func TestWaitForProcessIdentityExitTreatsReusedPIDAsExited(t *testing.T) {
	t.Parallel()

	identity, err := CaptureProcessIdentity(os.Getpid())
	require.NoError(t, err)
	identity.StartTime += "-different"

	require.True(t, WaitForProcessIdentityExit(os.Getpid(), identity))
}
