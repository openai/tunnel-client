package adminui

import (
	"context"
	"log/slog"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestLogBufferRecentAndOverwrite(t *testing.T) {
	t.Parallel()

	b := NewLogBufferWithCapacity(3)

	emit := func(msg string) {
		r := slog.NewRecord(time.Now(), slog.LevelInfo, msg, 0)
		b.Handle(context.Background(), r)
	}

	emit("one")
	emit("two")
	emit("three")

	got := b.Recent(10)
	require.Len(t, got, 3)
	require.Equal(t, "one", got[0].Message)
	require.Equal(t, "two", got[1].Message)
	require.Equal(t, "three", got[2].Message)

	emit("four")
	got = b.Recent(10)
	require.Len(t, got, 3)
	require.Equal(t, "two", got[0].Message)
	require.Equal(t, "three", got[1].Message)
	require.Equal(t, "four", got[2].Message)
}

func TestLogBufferRedactsBearerToken(t *testing.T) {
	t.Parallel()

	b := NewLogBufferWithCapacity(10)
	r := slog.NewRecord(time.Now(), slog.LevelInfo, "raw http request", 0)
	r.AddAttrs(slog.String("dump", "Authorization: Bearer sk-proj-abcdefg1234567890\r\n"))
	b.Handle(context.Background(), r)

	got := b.Recent(1)
	require.Len(t, got, 1)
	require.Contains(t, got[0].Attrs["dump"], "Authorization: Bearer [REDACTED]")
	require.NotContains(t, got[0].Attrs["dump"], "sk-proj-")
}

func TestLogBufferSubscribeIsBestEffort(t *testing.T) {
	t.Parallel()

	b := NewLogBufferWithCapacity(10)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	ch := b.Subscribe(ctx)
	require.NotNil(t, ch)

	r := slog.NewRecord(time.Now(), slog.LevelInfo, "hello", 0)
	b.Handle(ctx, r)

	select {
	case ev := <-ch:
		require.Equal(t, "hello", ev.Message)
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for subscribed log event")
	}
}
