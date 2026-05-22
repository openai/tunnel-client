package mcpclient

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"
)

// ProbeState tracks the result of the one-time startup probe against the main MCP channel.
type ProbeState struct {
	done      chan struct{}
	mu        sync.Mutex
	checkedAt time.Time
	err       error
	once      sync.Once
}

// NewProbeState constructs an empty ProbeState.
func NewProbeState() *ProbeState {
	return &ProbeState{done: make(chan struct{})}
}

// IsDone reports whether the probe has completed.
func (p *ProbeState) IsDone() bool {
	if p == nil {
		return false
	}
	select {
	case <-p.done:
		return true
	default:
		return false
	}
}

// Set records the probe result and signals waiters.
func (p *ProbeState) Set(err error) {
	if p == nil {
		return
	}
	p.once.Do(func() {
		p.mu.Lock()
		defer p.mu.Unlock()
		p.checkedAt = time.Now().UTC()
		p.err = err
		close(p.done)
	})
}

// Wait blocks until the probe result is available or the timeout elapses.
func (p *ProbeState) Wait(timeout time.Duration) (time.Time, error, bool) {
	if p == nil {
		return time.Time{}, nil, false
	}
	timer := time.NewTimer(timeout)
	defer timer.Stop()
	select {
	case <-p.done:
		p.mu.Lock()
		defer p.mu.Unlock()
		return p.checkedAt, p.err, true
	case <-timer.C:
		return time.Time{}, nil, false
	}
}

// WaitUntilDone blocks until the startup probe records a result or ctx is canceled.
func (p *ProbeState) WaitUntilDone(ctx context.Context) error {
	if p == nil {
		return errors.New("mcp probe state is nil")
	}
	if ctx == nil {
		return errors.New("mcp probe wait context is nil")
	}
	select {
	case <-p.done:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

// IsAuthRequiredProbeError reports whether a probe error indicates that the target MCP server
// is reachable but requires OAuth or other request authentication before initialize succeeds.
func IsAuthRequiredProbeError(err error) bool {
	if err == nil {
		return false
	}
	msg := strings.ToLower(err.Error())
	return strings.Contains(msg, "unauthorized") ||
		strings.Contains(msg, "401") ||
		strings.Contains(msg, "www-authenticate")
}

// ProbeTimeoutError reports that the startup probe did not complete before the deadline.
type ProbeTimeoutError struct {
	Timeout time.Duration
	Cause   error
}

func (e *ProbeTimeoutError) Error() string {
	return fmt.Sprintf("mcp probe timed out after %s: %v", e.Timeout, e.Cause)
}

func (e *ProbeTimeoutError) Unwrap() error {
	if e == nil {
		return nil
	}
	return e.Cause
}

func NewProbeTimeoutError(timeout time.Duration, cause error) error {
	return &ProbeTimeoutError{
		Timeout: timeout,
		Cause:   cause,
	}
}

func IsTimeoutProbeError(err error) bool {
	var timeoutErr *ProbeTimeoutError
	return errors.As(err, &timeoutErr)
}
