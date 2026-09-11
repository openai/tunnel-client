package controlplane

import (
	"math"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/openai/tunnel-client/pkg/healthstate"
)

func TestPollHealthEvidenceRecoveryAndCopies(t *testing.T) {
	t.Parallel()
	now := time.Date(2026, 9, 10, 1, 0, 0, 0, time.UTC)
	h := NewPollHealth(nil)
	require.Equal(t, healthstate.StatusUnknown, h.Snapshot(now).Status)
	h.StartAttempt(now)
	require.Equal(t, 2.0, h.Snapshot(now.Add(2*time.Second)).Details.(healthstate.PollingDetails).CurrentPollAgeSeconds)
	h.Failed(now, "http_error", 503, time.Second)
	failure := h.Snapshot(now)
	require.Equal(t, healthstate.StatusDegraded, failure.Status)
	require.Equal(t, uint64(1), failure.Details.(healthstate.PollingDetails).ConsecutiveFailures)
	*failure.ObservedAt = time.Time{}
	h.Succeeded(now.Add(time.Second))
	success := h.Snapshot(now)
	require.Equal(t, healthstate.StatusOK, success.Status)
	details := success.Details.(healthstate.PollingDetails)
	require.Zero(t, details.ConsecutiveFailures)
	require.Empty(t, details.FailureCategory)
	require.NotNil(t, details.LastError)
	require.NotNil(t, details.LastSuccess)
	h.Backpressured(now)
	require.Equal(t, healthstate.StatusOK, h.Snapshot(now).Status)
}

func TestDeliveryHealthConcurrentCompletionAndCancellation(t *testing.T) {
	t.Parallel()
	h := NewDeliveryHealth()
	now := time.Now()
	var workers sync.WaitGroup
	for range 32 {
		workers.Go(func() {
			h.Begin()
			h.Attempt(false)
			h.AttemptFailed(now, "http_error", 503)
			h.Attempt(true)
			h.Finish(now, "accepted", "", 200)
			_ = h.Snapshot(now)
		})
	}
	workers.Wait()
	snapshot := h.Snapshot(now)
	d := snapshot.Details.(healthstate.DeliveryDetails)
	require.Equal(t, healthstate.StatusOK, snapshot.Status)
	require.Equal(t, uint64(32), d.Accepted)
	require.Equal(t, uint64(64), d.Attempts)
	require.Equal(t, uint64(32), d.Retries)
	require.Zero(t, d.InProgress)
	*d.LastAccepted = time.Time{}
	require.False(t, h.Snapshot(now).Details.(healthstate.DeliveryDetails).LastAccepted.IsZero())
	h.Begin()
	h.Finish(now, "already_fulfilled_or_unknown", "", 404)
	d = h.Snapshot(now).Details.(healthstate.DeliveryDetails)
	require.Equal(t, uint64(32), d.Accepted)
	require.Equal(t, uint64(33), d.Completed)
	h.Begin()
	h.Finish(now, "canceled", "canceled", 0)
	d = h.Snapshot(now).Details.(healthstate.DeliveryDetails)
	require.Zero(t, d.TerminalFailures)
	require.Equal(t, uint64(33), d.Completed)
	for _, count := range []*uint64{&h.details.Attempts, &h.details.Retries} {
		*count = math.MaxUint64
	}
	h.Attempt(true)
	require.Equal(t, uint64(math.MaxUint64), h.Snapshot(now).Details.(healthstate.DeliveryDetails).Attempts)
}

func TestQueueHealthDoesNotConsumeAndTracksPressure(t *testing.T) {
	t.Parallel()
	queue := make(PolledCommandQueue, 1)
	h := NewQueueHealth(queue)
	now := time.Now()
	require.Equal(t, healthstate.StatusOK, h.Snapshot(now).Status)
	changed := h.Changes()
	queue <- nil
	h.Enqueued(now)
	select {
	case <-changed:
	default:
		t.Fatal("enqueue observation did not wake subscriber")
	}
	h.Backpressure(now, true)
	for range 10 {
		snapshot := h.Snapshot(now.Add(2 * time.Second))
		require.Equal(t, "backpressured", snapshot.State)
		d := snapshot.Details.(healthstate.QueueDetails)
		require.Equal(t, 1, d.Depth)
		require.Equal(t, 1.0, d.Utilization)
		require.Equal(t, 2.0, d.BackpressureSeconds)
	}
	<-queue
	h.Dequeued(now.Add(2 * time.Second))
	h.Backpressure(now.Add(2*time.Second), false)
	require.Equal(t, "available", h.Snapshot(now.Add(time.Hour)).State)
	require.Equal(t, 2.0, h.Snapshot(now.Add(time.Hour)).Details.(healthstate.QueueDetails).BackpressureSeconds)
}

func TestDeliveryHealthFailureAfterCancellation(t *testing.T) {
	t.Parallel()
	for _, priorSuccess := range []bool{false, true} {
		t.Run(map[bool]string{false: "without_prior_success", true: "with_prior_success"}[priorSuccess], func(t *testing.T) {
			h := NewDeliveryHealth()
			now := time.Now()
			if priorSuccess {
				h.Begin()
				h.Finish(now, "accepted", "", 200)
			}
			h.Begin()
			h.Finish(now, "canceled", "canceled", 0)
			require.NotEqual(t, healthstate.StatusDegraded, h.Snapshot(now).Status)
			h.Begin()
			h.Attempt(false)
			h.AttemptFailed(now.Add(time.Second), "http_error", 503)
			snapshot := h.Snapshot(now.Add(time.Second))
			require.Equal(t, healthstate.StatusDegraded, snapshot.Status)
			require.Equal(t, "uploading", snapshot.State)
			details := snapshot.Details.(healthstate.DeliveryDetails)
			require.Equal(t, "http_error", details.FailureCategory)
			require.Equal(t, 503, details.HTTPStatus)
			require.Zero(t, details.TerminalFailures)
			h.Attempt(true)
			h.Finish(now.Add(2*time.Second), "accepted", "", 200)
			require.Equal(t, healthstate.StatusOK, h.Snapshot(now.Add(2*time.Second)).Status)
		})
	}
}
