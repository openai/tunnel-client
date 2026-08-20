package internal

import (
	"context"
	"errors"
	"net/http"
	"sync"
)

type ctxKey struct{}

// HeaderCarrier stores per-request headers that should be forwarded to the MCP
// server as well as the response headers returned by the transport.
type HeaderCarrier struct {
	request              http.Header
	mu                   sync.Mutex
	response             http.Header
	statusCode           int
	responseBody         []byte
	responseBodyCaptured bool
	responseBodyTooLarge bool
	responseBodyReadErr  error
	transportErr         error
}

// ContextWithHeaders returns a context that carries the provided headers so the
// forwarding RoundTripper can inject them into the outbound HTTP request. A
// carrier instance is returned so callers can inspect the response headers once
// the request completes.
func ContextWithHeaders(ctx context.Context, headers http.Header) (context.Context, *HeaderCarrier, error) {
	if ctx == nil {
		return nil, nil, errors.New("context is nil")
	}
	var clone http.Header
	if headers != nil {
		clone = headers.Clone()
	}
	carrier := &HeaderCarrier{request: clone}
	return context.WithValue(ctx, ctxKey{}, carrier), carrier, nil
}

// CarrierFromContext extracts the header carrier embedded in the context, if
// present.
func CarrierFromContext(ctx context.Context) *HeaderCarrier {
	if ctx == nil {
		return nil
	}
	carrier, _ := ctx.Value(ctxKey{}).(*HeaderCarrier)
	return carrier
}

// ApplyRequestHeaders injects the stored request headers into the outgoing HTTP
// request, overriding any existing values for the same header names.
func (c *HeaderCarrier) ApplyRequestHeaders(dst http.Header) {
	if c == nil || len(c.request) == 0 {
		return
	}
	for k, values := range c.request {
		dst.Del(k)
		for _, v := range values {
			dst.Add(k, v)
		}
	}
}

// StoreResponse records the HTTP status code and a defensive copy of the
// response headers so they can be returned to the caller once the request
// completes.
func (c *HeaderCarrier) StoreResponse(statusCode int, headers http.Header) {
	if c == nil {
		return
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if headers != nil {
		c.response = headers.Clone()
	} else {
		c.response = nil
	}
	c.statusCode = statusCode
}

// ResponseStatusAndHeaders returns the captured HTTP status code together with a
// defensive copy of the response headers.
func (c *HeaderCarrier) ResponseStatusAndHeaders() (int, http.Header) {
	if c == nil {
		return 0, nil
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	var clone http.Header
	if c.response != nil {
		clone = c.response.Clone()
	}
	return c.statusCode, clone
}

// StoreTransportError records the underlying RoundTripper error before higher
// protocol layers can flatten its error chain into text.
func (c *HeaderCarrier) StoreTransportError(err error) {
	if c == nil || err == nil {
		return
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	c.transportErr = err
}

// TransportError returns the underlying RoundTripper error, if one was
// captured for this request context.
func (c *HeaderCarrier) TransportError() error {
	if c == nil {
		return nil
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.transportErr
}

// StoreResponseBodyCapture records a bounded copy of a non-success response
// body before the MCP SDK consumes it. The original body remains available to
// the SDK through the forwarding RoundTripper.
func (c *HeaderCarrier) StoreResponseBodyCapture(body []byte, tooLarge bool, readErr error) {
	if c == nil {
		return
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	c.responseBody = append(c.responseBody[:0], body...)
	c.responseBodyCaptured = true
	c.responseBodyTooLarge = tooLarge
	c.responseBodyReadErr = readErr
}

// ResponseBodyCapture returns the bounded non-success response body capture.
// The body is defensively copied so callers cannot mutate carrier state.
func (c *HeaderCarrier) ResponseBodyCapture() (body []byte, tooLarge bool, readErr error, captured bool) {
	if c == nil {
		return nil, false, nil, false
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	return append([]byte(nil), c.responseBody...), c.responseBodyTooLarge, c.responseBodyReadErr, c.responseBodyCaptured
}

// RequestHeaders returns the headers that will be forwarded with the request.
func (c *HeaderCarrier) RequestHeaders() http.Header {
	if c == nil {
		return nil
	}
	if c.request == nil {
		return nil
	}
	return c.request.Clone()
}
