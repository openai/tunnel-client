package types

import "net/http"

// TunnelID identifies a specific tunnel client instance.
type TunnelID string

// String returns the string form of the tunnel identifier.
func (t TunnelID) String() string {
	return string(t)
}

// RequestID identifies a single command request polled from the control plane.
type RequestID string

// String returns the string form of the polled request identifier.
func (r RequestID) String() string {
	return string(r)
}

const (
	// Both identifiers use the same wire header, but each is scoped to a different hop:
	// - ControlPlaneRequestID tracks plugin-service/connectors talking to tunnel-service.
	// - TunnelServiceRequestID tracks tunnel-client talking back to tunnel-service.
	controlPlaneRequestIDHeader  = "X-Request-Id"
	tunnelServiceRequestIDHeader = "X-Request-Id"
)

// ControlPlaneRequestID traces a request path through plugin-service/connectors
// while they communicate with tunnel-service. The ID is echoed back via the
// X-Request-Id header so clients can correlate failures with server traces.
type ControlPlaneRequestID string

// String returns the string version of the upstream request identifier.
func (r ControlPlaneRequestID) String() string {
	return string(r)
}

// NewControlPlaneRequestIDFromHeader extracts the request identifier from the
// provided HTTP headers. The boolean indicates whether the header was present.
func NewControlPlaneRequestIDFromHeader(h http.Header) (ControlPlaneRequestID, bool) {
	if h == nil {
		return "", false
	}
	value := h.Get(controlPlaneRequestIDHeader)
	if value == "" {
		return "", false
	}
	return ControlPlaneRequestID(value), true
}

// TunnelServiceRequestID identifies HTTP requests issued by tunnel-client to
// tunnel-service (e.g. POST /response). The service returns the identifier in
// the X-Request-Id header so clients can reference server-side traces.
type TunnelServiceRequestID string

// String returns the string value of the tunnel service request identifier.
func (r TunnelServiceRequestID) String() string {
	return string(r)
}

// NewTunnelServiceRequestIDFromHeader extracts the tunnel-service request ID
// from the provided HTTP headers. The boolean indicates whether the header was present.
func NewTunnelServiceRequestIDFromHeader(h http.Header) (TunnelServiceRequestID, bool) {
	if h == nil {
		return "", false
	}
	value := h.Get(tunnelServiceRequestIDHeader)
	if value == "" {
		return "", false
	}
	return TunnelServiceRequestID(value), true
}
