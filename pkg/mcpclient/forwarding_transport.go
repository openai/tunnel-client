package mcpclient

import (
	"context"
	"net/http"

	"github.com/modelcontextprotocol/go-sdk/jsonrpc"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// ForwardingTransport decorates an mcp.Transport so callers can attach
// per-request headers and capture the response headers returned by the MCP
// server.
type ForwardingTransport interface {
	Connect(ctx context.Context) (ForwardingConnection, error)
}

// SessionTerminatingTransport can explicitly close an MCP Streamable HTTP session and report
// the upstream HTTP response returned by the MCP server.
type SessionTerminatingTransport interface {
	TerminateSession(ctx context.Context, headers http.Header) (int, http.Header, error)
}

// ForwardingConnection extends mcp.Connection with helpers that return the
// response headers collected from the underlying HTTP transport.
type ForwardingConnection interface {
	// Write writes a new message to the connection.
	//
	// Write may be called concurrently, as calls or responses may occur
	// concurrently in user code.
	//
	// It returns the downstream HTTP result together with an error (if any)
	// encountered while writing or processing the response. A recognized MCP
	// error returned with a non-success HTTP status is carried in
	// PreservedError and is not returned as a transport error.
	Write(context.Context, http.Header, jsonrpc.Message) (ForwardingWriteResult, error)

	Read(ctx context.Context) (jsonrpc.Message, error)

	// Close closes the connection. It is implicitly called for non-context
	// Read or Write failures. Shared deadline-retiring connections may preserve
	// their physical transport when one request context expires.
	//
	// Close may be called multiple times, potentially concurrently.
	Close() error
}

// ResponseDeadlineRetiringConnection can retire one timed-out request
// lifecycle without closing a shared physical transport. Implementations must
// discard any later response for the retired request before another request can
// observe it.
//
// RetireResponseDeadline is idempotent. Callers should use it only after a
// response deadline or equivalent per-request forwarding window expires. It
// returns true when the logical request was retired without closing the
// physical transport; false means the caller should keep its normal close
// path.
type ResponseDeadlineRetiringConnection interface {
	RetireResponseDeadline() bool
}

// ForwardingWriteResult is the downstream HTTP result observed while writing
// an MCP message.
type ForwardingWriteResult struct {
	StatusCode      int
	ResponseHeaders http.Header
	PreservedError  *PreservedMCPError
}

// PreservedMCPError is a recognized target-owned JSON-RPC error response. Its
// payload stays opaque so exact code, message, data, and future fields survive
// the tunnel unchanged.
type PreservedMCPError struct {
	payload []byte
	code    int64
}

// NewPreservedMCPError constructs an opaque preserved response. Normal runtime
// callers receive these only after forwardingConnection validates the target
// JSON-RPC response; the constructor also supports alternate ForwardingConnection
// implementations and focused dispatcher tests.
func NewPreservedMCPError(payload []byte, code int64) *PreservedMCPError {
	return &PreservedMCPError{
		payload: append([]byte(nil), payload...),
		code:    code,
	}
}

// Payload returns a defensive copy of the exact target JSON-RPC payload.
func (e *PreservedMCPError) Payload() []byte {
	if e == nil {
		return nil
	}
	return append([]byte(nil), e.payload...)
}

// Code returns the bounded JSON-RPC error code for safe diagnostics.
func (e *PreservedMCPError) Code() int64 {
	if e == nil {
		return 0
	}
	return e.code
}

// NonProtocolResponseKind identifies why a downstream non-success response
// could not be preserved as an MCP JSON-RPC error.
type NonProtocolResponseKind string

const (
	NonProtocolResponseBodyMissing     NonProtocolResponseKind = "response_body_missing"
	NonProtocolResponseBodyUnreadable  NonProtocolResponseKind = "response_body_unreadable"
	NonProtocolResponseBodyTooLarge    NonProtocolResponseKind = "response_body_too_large"
	NonProtocolResponseMalformedJSON   NonProtocolResponseKind = "malformed_json"
	NonProtocolResponseInvalidMCPError NonProtocolResponseKind = "invalid_mcp_error"
)

// NonProtocolResponseError is handed to the separate fallback-classification
// path when a downstream response is not a valid, preservable MCP error.
type NonProtocolResponseError struct {
	kind  NonProtocolResponseKind
	cause error
}

// Kind returns a bounded reason suitable for classification and tests.
func (e *NonProtocolResponseError) Kind() NonProtocolResponseKind {
	if e == nil {
		return ""
	}
	return e.kind
}

func (e *NonProtocolResponseError) Error() string {
	if e == nil {
		return "downstream response is not a valid MCP error"
	}
	return "downstream response is not a valid MCP error: " + string(e.kind)
}

func (e *NonProtocolResponseError) Unwrap() error {
	if e == nil {
		return nil
	}
	return e.cause
}

// NewForwardingTransport wraps the provided transport with header-forwarding
// capabilities.
func NewForwardingTransport(base mcp.Transport) ForwardingTransport {
	if base == nil {
		return nil
	}
	return &forwardingTransport{base: base}
}
