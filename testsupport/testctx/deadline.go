package testctx

import (
	"context"
	"testing"
	"time"
)

type deadlineReporter interface {
	Deadline() (time.Time, bool)
}

// WithDeadline returns a test-scoped context that expires reserve before the
// outer Go test timeout when the runner exposes one.
func WithDeadline(t testing.TB, reserve time.Duration) (context.Context, context.CancelFunc) {
	t.Helper()

	reporter, ok := t.(deadlineReporter)
	if !ok {
		return context.WithCancel(t.Context())
	}
	if deadline, ok := reporter.Deadline(); ok {
		return withDeadline(t.Context(), deadline, reserve)
	}
	return context.WithCancel(t.Context())
}

func withDeadline(parent context.Context, deadline time.Time, reserve time.Duration) (context.Context, context.CancelFunc) {
	if reserve < 0 {
		reserve = 0
	}
	return context.WithDeadline(parent, deadline.Add(-reserve))
}
