package state

import (
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestAcquireStateLockSerializesWholeMapMutations(t *testing.T) {
	t.Parallel()

	root := Root{Path: t.TempDir()}
	releaseFirst, err := AcquireStateLock(root)
	require.NoError(t, err)
	firstReleased := false
	t.Cleanup(func() {
		if !firstReleased {
			_ = releaseFirst()
		}
	})

	secondAcquired := make(chan struct{})
	secondDone := make(chan error, 1)
	go func() {
		releaseSecond, err := AcquireStateLock(root)
		if err != nil {
			secondDone <- err
			return
		}
		close(secondAcquired)
		secondDone <- releaseSecond()
	}()

	select {
	case <-secondAcquired:
		t.Fatal("second state lock acquired before the first lock was released")
	case <-time.After(100 * time.Millisecond):
	}
	require.NoError(t, releaseFirst())
	firstReleased = true
	select {
	case <-secondAcquired:
	case <-time.After(2 * time.Second):
		t.Fatal("second state lock did not acquire after release")
	}
	require.NoError(t, <-secondDone)
}
