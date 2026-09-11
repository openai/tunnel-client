package controlplane

import (
	"math"
	"sync"
	"time"

	"github.com/openai/tunnel-client/pkg/healthstate"
	"github.com/openai/tunnel-client/pkg/runtimeconfig"
)

func increment(value *uint64) {
	if *value < math.MaxUint64 {
		*value++
	}
}
func timestamp(value time.Time) *time.Time {
	if value.IsZero() {
		return nil
	}
	value = value.UTC()
	return &value
}
func elapsed(now, since time.Time) float64 {
	if since.IsZero() || now.Before(since) {
		return 0
	}
	return now.Sub(since).Seconds()
}

// PollHealth records bounded observations of the polling loop.
type PollHealth struct {
	mu                                                         sync.Mutex
	changed                                                    chan struct{}
	state                                                      string
	lastAttempt, lastSuccess, lastError, nextRetry, observedAt time.Time
	failures                                                   uint64
	configuredWait, effectiveWait, deadline                    time.Duration
	category                                                   string
	httpStatus                                                 int
}

func NewPollHealth(cfg *runtimeconfig.ControlPlaneConfig) *PollHealth {
	if cfg == nil {
		cfg = &runtimeconfig.ControlPlaneConfig{}
	}
	return &PollHealth{state: "starting", configuredWait: cfg.PollTimeoutOrDefault(), effectiveWait: cfg.PollTimeoutOrDefault(), deadline: cfg.PollDeadlineTimeoutOrDefault()}
}
func (*PollHealth) Name() string { return "control-plane" }
func (s *PollHealth) StartAttempt(now time.Time) {
	if s == nil {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	defer s.signal()
	s.state, s.lastAttempt, s.observedAt, s.nextRetry = "polling", now, now, time.Time{}
}
func (s *PollHealth) RequestLimits(wait, deadline time.Duration) {
	if s == nil {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	defer s.signal()
	s.effectiveWait, s.deadline = wait, deadline
}
func (s *PollHealth) Succeeded(now time.Time) {
	if s == nil {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	defer s.signal()
	s.lastSuccess, s.observedAt = now, now
	s.failures, s.category, s.httpStatus = 0, "", 0
	s.state, s.nextRetry = "idle", time.Time{}
}
func (s *PollHealth) Failed(now time.Time, category string, status int, delay time.Duration) {
	if s == nil {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	defer s.signal()
	increment(&s.failures)
	s.state, s.lastError, s.observedAt = "backoff", now, now
	s.category, s.httpStatus, s.nextRetry = category, status, now.Add(delay)
}
func (s *PollHealth) Backpressured(now time.Time) {
	if s == nil {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	defer s.signal()
	s.state, s.observedAt = "backpressured", now
}
func (s *PollHealth) Stopped(now time.Time) {
	if s == nil {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	defer s.signal()
	s.state, s.observedAt, s.nextRetry = "stopped", now, time.Time{}
}
func (s *PollHealth) Snapshot(now time.Time) healthstate.ComponentSnapshot {
	s.mu.Lock()
	defer s.mu.Unlock()
	status := healthstate.StatusUnknown
	if s.failures > 0 {
		status = healthstate.StatusDegraded
	} else if !s.lastSuccess.IsZero() {
		status = healthstate.StatusOK
	}
	age := float64(0)
	if s.state == "polling" {
		age = elapsed(now, s.lastAttempt)
	}
	return healthstate.ComponentSnapshot{Status: status, State: s.state, ReasonCode: s.category, ObservedAt: timestamp(s.observedAt), Details: healthstate.PollingDetails{
		LastAttempt: timestamp(s.lastAttempt), LastSuccess: timestamp(s.lastSuccess), LastError: timestamp(s.lastError), ConsecutiveFailures: s.failures,
		CurrentPollAgeSeconds: age, ConfiguredWaitSeconds: s.configuredWait.Seconds(), EffectiveWaitSeconds: s.effectiveWait.Seconds(), DeadlineSeconds: s.deadline.Seconds(),
		NextRetry: timestamp(s.nextRetry), FailureCategory: s.category, HTTPStatus: s.httpStatus,
	}}
}

// Changes closes after the next poll observation; obtain it before Snapshot.
func (s *PollHealth) Changes() <-chan struct{} {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.changed == nil {
		s.changed = make(chan struct{})
	}
	return s.changed
}
func (s *PollHealth) signal() {
	if s.changed != nil {
		close(s.changed)
		s.changed = nil
	}
}

// DeliveryHealth observes logical delivery completion separately from attempts.
type DeliveryHealth struct {
	mu         sync.Mutex
	changed    chan struct{}
	details    healthstate.DeliveryDetails
	observedAt time.Time
}

func NewDeliveryHealth() *DeliveryHealth {
	return &DeliveryHealth{details: healthstate.DeliveryDetails{Disposition: "not_observed"}}
}
func (*DeliveryHealth) Name() string { return "response-delivery" }
func (s *DeliveryHealth) Begin() {
	if s == nil {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	defer s.signal()
	increment(&s.details.InProgress)
}
func (s *DeliveryHealth) Attempt(retry bool) {
	if s == nil {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	defer s.signal()
	increment(&s.details.Attempts)
	if retry {
		increment(&s.details.Retries)
	}
}
func (s *DeliveryHealth) AttemptFailed(now time.Time, category string, status int) {
	if s == nil {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	defer s.signal()
	s.details.LastFailure, s.details.FailureCategory, s.details.HTTPStatus = timestamp(now), category, status
	s.observedAt = now
}
func (s *DeliveryHealth) Finish(now time.Time, disposition, category string, status int) {
	if s == nil {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	defer s.signal()
	if s.details.InProgress > 0 {
		s.details.InProgress--
	}
	s.observedAt = now
	s.details.LastCompleted, s.details.Disposition = timestamp(now), disposition
	s.details.FailureCategory, s.details.HTTPStatus = category, status
	if disposition == "failed" {
		increment(&s.details.TerminalFailures)
		s.details.LastFailure = timestamp(now)
	} else if disposition != "canceled" {
		increment(&s.details.Completed)
		if disposition == "accepted" {
			increment(&s.details.Accepted)
			s.details.LastAccepted = timestamp(now)
		}
	}
}
func (s *DeliveryHealth) Snapshot(time.Time) healthstate.ComponentSnapshot {
	s.mu.Lock()
	defer s.mu.Unlock()
	d := s.details
	// Copy the timestamps as well as the scalar fields; callers own their snapshot.
	for _, field := range []**time.Time{&d.LastAccepted, &d.LastCompleted, &d.LastFailure} {
		if *field != nil {
			*field = timestamp(**field)
		}
	}
	status := healthstate.StatusUnknown
	if (d.FailureCategory != "" && d.FailureCategory != "canceled") || d.Disposition == "failed" {
		status = healthstate.StatusDegraded
	} else if d.Completed > 0 {
		status = healthstate.StatusOK
	}
	state := d.Disposition
	if d.InProgress > 0 {
		state = "uploading"
	}
	return healthstate.ComponentSnapshot{Status: status, State: state, ReasonCode: d.FailureCategory, ObservedAt: timestamp(s.observedAt), Details: d}
}

// Changes closes after the next observation; obtain it before taking a snapshot.
func (s *DeliveryHealth) Changes() <-chan struct{} {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.changed == nil {
		s.changed = make(chan struct{})
	}
	return s.changed
}
func (s *DeliveryHealth) signal() {
	if s.changed != nil {
		close(s.changed)
		s.changed = nil
	}
}

// QueueHealth reads channel length without receiving any queued command.
type QueueHealth struct {
	mu                                      sync.Mutex
	changed                                 chan struct{}
	queue                                   PolledCommandQueue
	lastEnqueue, lastDequeue, pressureStart time.Time
	enqueued, dequeued                      uint64
	pressure                                time.Duration
}

func NewQueueHealth(queue PolledCommandQueue) *QueueHealth { return &QueueHealth{queue: queue} }
func (*QueueHealth) Name() string                          { return "queue" }
func (s *QueueHealth) Enqueued(now time.Time) {
	if s == nil {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	defer s.signal()
	s.lastEnqueue = now
	increment(&s.enqueued)
}
func (s *QueueHealth) Dequeued(now time.Time) {
	if s == nil {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	defer s.signal()
	s.lastDequeue = now
	increment(&s.dequeued)
}
func (s *QueueHealth) Backpressure(now time.Time, active bool) {
	if s == nil {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	defer s.signal()
	if active {
		if s.pressureStart.IsZero() {
			s.pressureStart = now
		}
		return
	}
	if !s.pressureStart.IsZero() {
		delta := now.Sub(s.pressureStart)
		if delta > 0 {
			if delta > time.Duration(math.MaxInt64)-s.pressure {
				s.pressure = time.Duration(math.MaxInt64)
			} else {
				s.pressure += delta
			}
		}
		s.pressureStart = time.Time{}
	}
}
func (s *QueueHealth) Snapshot(now time.Time) healthstate.ComponentSnapshot {
	s.mu.Lock()
	defer s.mu.Unlock()
	d := healthstate.QueueDetails{Depth: len(s.queue), Capacity: cap(s.queue), LastEnqueue: timestamp(s.lastEnqueue), LastDequeue: timestamp(s.lastDequeue), Enqueued: s.enqueued, Dequeued: s.dequeued, BackpressureSeconds: s.pressure.Seconds() + elapsed(now, s.pressureStart)}
	if d.Capacity > 0 {
		d.Utilization = float64(d.Depth) / float64(d.Capacity)
	}
	state := "available"
	if d.Capacity > 0 && d.Depth == d.Capacity || !s.pressureStart.IsZero() {
		state = "backpressured"
	}
	observed := s.lastEnqueue
	if s.lastDequeue.After(observed) {
		observed = s.lastDequeue
	}
	return healthstate.ComponentSnapshot{Status: healthstate.StatusOK, State: state, ObservedAt: timestamp(observed), Details: d}
}

// Changes closes after the next enqueue, dequeue, or backpressure observation.
func (s *QueueHealth) Changes() <-chan struct{} {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.changed == nil {
		s.changed = make(chan struct{})
	}
	return s.changed
}
func (s *QueueHealth) signal() {
	if s.changed != nil {
		close(s.changed)
		s.changed = nil
	}
}
