package mcpclient

import (
	"context"
	"errors"
	"fmt"
	"net/http"
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
	select {
	case <-p.done:
		p.mu.Lock()
		defer p.mu.Unlock()
		return p.checkedAt, p.err, true
	default:
	}
	if timeout <= 0 {
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
	var statusErr *ProbeHTTPStatusError
	return errors.As(err, &statusErr) && statusErr.StatusCode == http.StatusUnauthorized
}

// ProbeHTTPStatusError preserves the HTTP response status observed while the
// startup probe failed, without relying on the SDK's formatted error text.
type ProbeHTTPStatusError struct {
	StatusCode int
	Cause      error
}

func (e *ProbeHTTPStatusError) Error() string {
	if e == nil || e.Cause == nil {
		return ""
	}
	return e.Cause.Error()
}

func (e *ProbeHTTPStatusError) Unwrap() error {
	if e == nil {
		return nil
	}
	return e.Cause
}

// NewProbeHTTPStatusError attaches the observed HTTP status to a failed
// startup probe. It returns cause unchanged when no response status exists.
func NewProbeHTTPStatusError(statusCode int, cause error) error {
	if cause == nil || statusCode == 0 {
		return cause
	}
	return &ProbeHTTPStatusError{
		StatusCode: statusCode,
		Cause:      cause,
	}
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

// StartupWaitTimeoutError reports that the opt-in listener startup wait
// exhausted its budget while the target continued returning a retryable
// pre-connect failure. It deliberately remains distinct from ProbeTimeoutError:
// the legacy probe timeout is readiness-compatible, while exhausting the
// operator-requested listener gate must keep readiness failed.
type StartupWaitTimeoutError struct {
	Timeout time.Duration
	Cause   error
}

func (e *StartupWaitTimeoutError) Error() string {
	if e == nil {
		return ""
	}
	return fmt.Sprintf("mcp startup wait timed out after %s: %v", e.Timeout, e.Cause)
}

func (e *StartupWaitTimeoutError) Unwrap() error {
	if e == nil {
		return nil
	}
	return e.Cause
}

// NewStartupWaitTimeoutError constructs the non-ready error recorded when the
// opt-in MCP listener wait exhausts its configured budget.
func NewStartupWaitTimeoutError(timeout time.Duration, cause error) error {
	return &StartupWaitTimeoutError{
		Timeout: timeout,
		Cause:   cause,
	}
}

// IsStartupWaitTimeoutError reports whether err was produced by an exhausted
// opt-in MCP listener startup wait.
func IsStartupWaitTimeoutError(err error) bool {
	var timeoutErr *StartupWaitTimeoutError
	return errors.As(err, &timeoutErr)
}
