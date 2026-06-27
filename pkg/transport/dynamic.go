package transport

import (
	"net/http"
	"sync/atomic"
)

// DynamicRoundTripper wraps an http.RoundTripper and allows dynamically
// swapping the active transport in a thread-safe manner without severing
// existing active TCP connections on the old transport.
type DynamicRoundTripper struct {
	rt atomic.Value
}

// NewDynamicRoundTripper creates a new DynamicRoundTripper with the provided
// initial transport.
func NewDynamicRoundTripper(initial http.RoundTripper) *DynamicRoundTripper {
	if initial == nil {
		initial = http.DefaultTransport
	}
	d := &DynamicRoundTripper{}
	d.rt.Store(initial)
	return d
}

// Update atomically replaces the active RoundTripper. New requests will use
// the new transport, while any existing requests currently in flight on the
// old transport will complete naturally.
func (d *DynamicRoundTripper) Update(rt http.RoundTripper) {
	if rt != nil {
		d.rt.Store(rt)
	}
}

// RoundTrip executes a single HTTP transaction using the currently active
// RoundTripper.
func (d *DynamicRoundTripper) RoundTrip(req *http.Request) (*http.Response, error) {
	return d.rt.Load().(http.RoundTripper).RoundTrip(req)
}
