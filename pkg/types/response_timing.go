package types

// MaxTargetElapsedUS is the inclusive v1 response-timing limit (24 hours).
const MaxTargetElapsedUS int64 = 86_400_000_000

// ResponseTiming reports client-observed elapsed time through a matching
// terminal MCP response. It includes the downstream network and server work,
// and excludes uploading the terminal response to the control plane.
type ResponseTiming struct {
	Version         int    `json:"version"`
	Transport       string `json:"transport"`
	TargetElapsedUS int64  `json:"target_elapsed_us"`
}

// Valid reports whether timing satisfies the v1 version, transport and duration bounds.
func (t ResponseTiming) Valid() bool {
	return t.Version == 1 && (t.Transport == "stdio" || t.Transport == "streamable_http") &&
		t.TargetElapsedUS >= 0 && t.TargetElapsedUS <= MaxTargetElapsedUS
}

// TraceContext is an optional W3C carrier supplied by the control plane.
// Receiving it does not enable forwarding to the customer MCP server.
type TraceContext struct {
	Traceparent string `json:"traceparent"`
	Tracestate  string `json:"tracestate,omitempty"`
}
