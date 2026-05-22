package mcpclient

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestProbeStateSetAndWait(t *testing.T) {
	t.Parallel()

	state := NewProbeState()
	wantErr := errors.New(`calling "initialize": Unauthorized`)

	state.Set(wantErr)

	checkedAt, gotErr, ok := state.Wait(10 * time.Millisecond)
	require.True(t, ok)
	require.WithinDuration(t, time.Now(), checkedAt, time.Second)
	require.EqualError(t, gotErr, wantErr.Error())
}

func TestProbeStateWaitTimeout(t *testing.T) {
	t.Parallel()

	state := NewProbeState()

	checkedAt, gotErr, ok := state.Wait(10 * time.Millisecond)
	require.False(t, ok)
	require.True(t, checkedAt.IsZero())
	require.NoError(t, gotErr)
}

func TestProbeStateWaitUntilDone(t *testing.T) {
	t.Parallel()

	state := NewProbeState()
	waitDone := make(chan error, 1)
	go func() {
		waitDone <- state.WaitUntilDone(t.Context())
	}()

	state.Set(nil)

	require.NoError(t, <-waitDone)
}

func TestProbeStateWaitUntilDoneReturnsContextError(t *testing.T) {
	t.Parallel()

	state := NewProbeState()
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	require.ErrorIs(t, state.WaitUntilDone(ctx), context.Canceled)
}

func TestIsAuthRequiredProbeError(t *testing.T) {
	t.Parallel()

	require.True(t, IsAuthRequiredProbeError(errors.New(`calling "initialize": Unauthorized`)))
	require.True(t, IsAuthRequiredProbeError(errors.New("received 401 from upstream")))
	require.False(t, IsAuthRequiredProbeError(errors.New("dial tcp 127.0.0.1:1: connection refused")))
}

func TestIsTimeoutProbeError(t *testing.T) {
	t.Parallel()

	err := NewProbeTimeoutError(2*time.Second, context.DeadlineExceeded)

	require.True(t, IsTimeoutProbeError(err))
	require.ErrorIs(t, err, context.DeadlineExceeded)
	require.False(t, IsTimeoutProbeError(errors.New("dial tcp 127.0.0.1:1: connection refused")))
}
