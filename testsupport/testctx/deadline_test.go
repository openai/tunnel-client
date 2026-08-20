package testctx

import (
	"context"
	"testing"
	"time"
)

func TestWithDeadlineReservesTimeBeforeOuterDeadline(t *testing.T) {
	t.Parallel()

	outerDeadline := time.Unix(100, 0)
	ctx, cancel := withDeadline(context.Background(), outerDeadline, 5*time.Second)
	defer cancel()

	deadline, ok := ctx.Deadline()
	if !ok {
		t.Fatal("expected context deadline")
	}
	want := outerDeadline.Add(-5 * time.Second)
	if !deadline.Equal(want) {
		t.Fatalf("expected deadline %s, got %s", want, deadline)
	}
}

func TestWithDeadlineUsesRunnerDeadline(t *testing.T) {
	t.Parallel()

	outerDeadline, ok := t.Deadline()
	if !ok {
		t.Skip("test runner disabled the outer test deadline")
	}

	ctx, cancel := WithDeadline(t, time.Second)
	defer cancel()

	deadline, ok := ctx.Deadline()
	if !ok {
		t.Fatal("expected context deadline")
	}
	want := outerDeadline.Add(-time.Second)
	if !deadline.Equal(want) {
		t.Fatalf("expected deadline %s, got %s", want, deadline)
	}
}

func TestWithDeadlineIgnoresNegativeReserve(t *testing.T) {
	t.Parallel()

	outerDeadline := time.Unix(100, 0)
	ctx, cancel := withDeadline(context.Background(), outerDeadline, -time.Second)
	defer cancel()

	deadline, ok := ctx.Deadline()
	if !ok {
		t.Fatal("expected context deadline")
	}
	if !deadline.Equal(outerDeadline) {
		t.Fatalf("expected deadline %s, got %s", outerDeadline, deadline)
	}
}
