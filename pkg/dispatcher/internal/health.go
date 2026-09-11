package dispatcherinternal

import (
	"context"
	"errors"
	"math"
	"net"
	"net/http"
	"sync"
	"time"

	"github.com/openai/tunnel-client/pkg/healthstate"
	"github.com/openai/tunnel-client/pkg/runtimeconfig"
)

type workOutcomeKey struct{}
type workOutcome struct{ failed, timedOut bool }

// Outcomes stay with this one synchronous processor call. Some existing paths
// intentionally return nil after forwarding an error or dropping an expired
// command; recording here preserves those return and response contracts.
func recordWorkFailure(ctx context.Context, err error, status int) {
	outcome, _ := ctx.Value(workOutcomeKey{}).(*workOutcome)
	if outcome == nil {
		return
	}
	timedOut := errors.Is(err, context.DeadlineExceeded) || errors.Is(context.Cause(ctx), errResponseDeadlineExceeded) || errors.Is(context.Cause(ctx), errConnectionTTLExceeded) || status == http.StatusRequestTimeout || status == http.StatusGatewayTimeout
	var networkError net.Error
	if errors.As(err, &networkError) && networkError.Timeout() {
		timedOut = true
	}
	if errors.Is(err, context.Canceled) && !timedOut {
		return
	}
	outcome.failed = true
	outcome.timedOut = outcome.timedOut || timedOut
}

// ActivityHealth observes actual execution, including the inline fallback.
type ActivityHealth struct {
	mu         sync.Mutex
	starts     []time.Time
	details    healthstate.DispatcherDetails
	state      string
	limited    bool
	changed    chan struct{}
	lastFailed bool
}

func NewActivityHealth(cfg *runtimeconfig.MCPConfig) *ActivityHealth {
	limit := 1
	if cfg != nil && cfg.MaxConcurrentRequests > 0 {
		limit = cfg.MaxConcurrentRequests
	}
	// Bound diagnostics independently of the configured execution pool. The
	// extra slot normally covers the existing inline submission fallback.
	slots := 1024
	if limit < slots {
		slots = limit + 1
	}
	return &ActivityHealth{starts: make([]time.Time, slots), details: healthstate.DispatcherDetails{PoolLimit: limit}, state: "starting", limited: limit >= 1024}
}
func (*ActivityHealth) Name() string { return "dispatcher" }
func healthTime(t time.Time) *time.Time {
	if t.IsZero() {
		return nil
	}
	t = t.UTC()
	return &t
}
func incrementHealth(n *uint64) {
	if *n < math.MaxUint64 {
		*n++
	}
}
func (s *ActivityHealth) signal() {
	if s.changed != nil {
		close(s.changed)
		s.changed = nil
	}
}

// Changes closes after the next work/lifecycle observation; read it before Snapshot.
func (s *ActivityHealth) Changes() <-chan struct{} {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.changed == nil {
		s.changed = make(chan struct{})
	}
	return s.changed
}
func (s *ActivityHealth) Accepting(accepting bool) {
	if s == nil {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	defer s.signal()
	s.details.Accepting = accepting
	if accepting {
		s.state = "accepting"
	} else {
		s.state = "draining"
	}
}
func (s *ActivityHealth) Stopped() {
	if s == nil {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	defer s.signal()
	s.state = "stopped"
	s.details.Accepting = false
}
func (s *ActivityHealth) begin(now time.Time) int {
	if s == nil {
		return -1
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	defer s.signal()
	incrementHealth(&s.details.Active)
	s.details.LastStart = healthTime(now)
	for i, t := range s.starts {
		if t.IsZero() {
			s.starts[i] = now
			return i
		}
	}
	s.limited = true
	return -1
}
func (s *ActivityHealth) finish(slot int, now time.Time, failed, timeout bool) {
	if s == nil {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	defer s.signal()
	if slot >= 0 && slot < len(s.starts) {
		s.starts[slot] = time.Time{}
	}
	if s.details.Active > 0 {
		s.details.Active--
	}
	incrementHealth(&s.details.Completed)
	if failed {
		incrementHealth(&s.details.Failures)
	}
	if timeout {
		incrementHealth(&s.details.Timeouts)
	}
	s.details.LastCompletion = healthTime(now)
	s.lastFailed = failed
}
func (s *ActivityHealth) Snapshot(now time.Time) healthstate.ComponentSnapshot {
	s.mu.Lock()
	defer s.mu.Unlock()
	d := s.details
	if d.LastStart != nil {
		d.LastStart = healthTime(*d.LastStart)
	}
	if d.LastCompletion != nil {
		d.LastCompletion = healthTime(*d.LastCompletion)
	}
	for _, t := range s.starts {
		if !t.IsZero() && now.After(t) {
			d.OldestActiveAgeSeconds = max(d.OldestActiveAgeSeconds, now.Sub(t).Seconds())
		}
	}
	status := healthstate.StatusOK
	if s.state == "starting" {
		status = healthstate.StatusUnknown
	}
	if s.lastFailed {
		status = healthstate.StatusDegraded
	}
	observed := d.LastStart
	if d.LastCompletion != nil && (observed == nil || d.LastCompletion.After(*observed)) {
		observed = d.LastCompletion
	}
	return healthstate.ComponentSnapshot{Status: status, State: s.state, ObservedAt: observed, Limited: s.limited, Details: d}
}
