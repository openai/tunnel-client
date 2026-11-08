package oauth

import (
	"context"
	"errors"
	"net/http"
	"sync"
)

// startupProbeSession is present only on the OAuth startup owner's context.
// Ordinary and queued discovery calls retain the original HTTP client path.
// Startup discovery is sequential, so current carries the last exchange across
// fetcher boundaries until the control-flow owner decides what happens next.
type startupProbeSession struct {
	state *DiscoveryState
	owner context.Context

	mu      sync.Mutex
	current *probeReceipt
}

type startupProbeSessionContextKey struct{}

func withStartupProbeSession(ctx context.Context, state *DiscoveryState, owner context.Context) context.Context {
	if state == nil {
		return ctx
	}
	return context.WithValue(ctx, startupProbeSessionContextKey{}, &startupProbeSession{state: state, owner: owner})
}

func startupProbeSessionFromContext(ctx context.Context) *startupProbeSession {
	if ctx == nil {
		return nil
	}
	session, _ := ctx.Value(startupProbeSessionContextKey{}).(*startupProbeSession)
	return session
}

func (s *startupProbeSession) begin(kind probeType, method string) *probeReceipt {
	if s == nil {
		return nil
	}
	receipt := s.state.beginProbe(kind, method)
	s.mu.Lock()
	s.current = receipt
	s.mu.Unlock()
	return receipt
}

// takeCurrent claims the leaf for a control decision only while the startup
// owner is live. This check is the decision boundary: cancellation observed
// here leaves the receipt for owner cleanup, while a later concurrent
// cancellation does not rewrite the claimed decision.
func (s *startupProbeSession) takeCurrent() *probeReceipt {
	if s == nil {
		return nil
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.owner != nil && s.owner.Err() != nil {
		// The startup owner resolves active receipts as cancelled when it exits.
		// Child probe and fetch deadlines do not cancel this owner context.
		return nil
	}
	receipt := s.current
	s.current = nil
	return receipt
}

func (s *startupProbeSession) finish(receipt *probeReceipt, disposition probeDisposition) {
	if s == nil || receipt == nil {
		return
	}
	s.mu.Lock()
	if s.owner != nil && s.owner.Err() != nil {
		s.mu.Unlock()
		return
	}
	if s.current == receipt {
		s.current = nil
	}
	s.mu.Unlock()
	// This decision owns the receipt once it observed a live startup owner.
	// A later concurrent cancellation does not rewrite completed history.
	receipt.finish(disposition)
}

func takeStartupProbe(ctx context.Context) *probeReceipt {
	return startupProbeSessionFromContext(ctx).takeCurrent()
}

func finishStartupProbe(ctx context.Context, disposition probeDisposition) {
	takeStartupProbe(ctx).finish(disposition)
}

// startupProbeOperation associates redirect decisions with the preceding
// exchange. Each wrapped client is private to one logical request operation;
// timeout retries may reuse it after the previous exchange has been resolved.
type startupProbeOperation struct {
	session *startupProbeSession
	kind    probeType

	mu   sync.Mutex
	last *probeReceipt
}

func (o *startupProbeOperation) begin(method string) *probeReceipt {
	receipt := o.session.begin(o.kind, method)
	o.mu.Lock()
	o.last = receipt
	o.mu.Unlock()
	return receipt
}

func (o *startupProbeOperation) finishLast(disposition probeDisposition) {
	o.mu.Lock()
	receipt := o.last
	o.last = nil
	o.mu.Unlock()
	o.session.finish(receipt, disposition)
}

type startupProbeTransport struct {
	base      http.RoundTripper
	operation *startupProbeOperation
}

func (t *startupProbeTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	method := ""
	if req != nil {
		method = req.Method
		if method == "" {
			method = http.MethodGet
		}
	}
	receipt := t.operation.begin(method)
	resp, err := t.base.RoundTrip(req)
	if resp != nil {
		// Publish headers before callers read or validate the response body.
		// The receipt remains pending until its control-flow owner resolves it.
		receipt.observeResponse(resp.StatusCode, resp.Header)
	}
	return resp, err
}

// CancelRequest preserves the legacy cancellation path used by http.Client
// for transports that predate request contexts.
func (t *startupProbeTransport) CancelRequest(req *http.Request) {
	if canceler, ok := t.base.(interface{ CancelRequest(*http.Request) }); ok {
		canceler.CancelRequest(req)
	}
}

// withStartupProbeHTTPClient observes each invocation of the configured
// RoundTripper, including redirects. The existing trust and redirect wrappers
// must already be applied so their final decisions remain authoritative.
// An exchange attempt may fail before reaching the server; a status is recorded
// only when the transport returns a response.
func withStartupProbeHTTPClient(ctx context.Context, client *http.Client, kind probeType) *http.Client {
	session := startupProbeSessionFromContext(ctx)
	if session == nil || client == nil {
		return client
	}

	cloned := *client
	base := client.Transport
	if base == nil {
		base = http.DefaultTransport
	}
	operation := &startupProbeOperation{session: session, kind: kind}
	cloned.Transport = &startupProbeTransport{base: base, operation: operation}
	priorPolicy := client.CheckRedirect
	cloned.CheckRedirect = func(req *http.Request, via []*http.Request) error {
		var err error
		if priorPolicy != nil {
			err = priorPolicy(req, via)
		} else if len(via) >= 10 {
			// Match net/http's default policy when no caller policy is present.
			err = errors.New("stopped after 10 redirects")
		}
		if err == nil {
			operation.finishLast(probeDispositionRedirect)
		}
		return err
	}
	return &cloned
}
