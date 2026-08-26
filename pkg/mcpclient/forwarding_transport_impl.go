package mcpclient

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"

	"github.com/modelcontextprotocol/go-sdk/jsonrpc"
	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/openai/tunnel-client/pkg/mcpclient/internal"
)

type responseDeadlineEnforcementContextKey struct{}

// ContextWithResponseDeadlineEnforcement marks MCP work whose full lifecycle
// is bounded by a tunnel command response deadline. Legacy commands without
// response_timeout intentionally remain unmarked.
func ContextWithResponseDeadlineEnforcement(ctx context.Context) context.Context {
	if ctx == nil {
		return nil
	}
	return context.WithValue(ctx, responseDeadlineEnforcementContextKey{}, true)
}

func hasResponseDeadlineEnforcement(ctx context.Context) bool {
	if ctx == nil {
		return false
	}
	marked, _ := ctx.Value(responseDeadlineEnforcementContextKey{}).(bool)
	return marked
}

var _ ForwardingTransport = (*forwardingTransport)(nil)
var _ SessionTerminatingTransport = (*forwardingTransport)(nil)

// forwardingTransport bridges the public ForwardingTransport interface to the
// internal implementation.
type forwardingTransport struct {
	base mcp.Transport
}

func (t *forwardingTransport) Connect(ctx context.Context) (ForwardingConnection, error) {
	if t == nil || t.base == nil {
		return nil, nil
	}
	conn, err := t.base.Connect(ctx)
	if err != nil {
		return nil, err
	}
	return &forwardingConnection{
		base: conn,
	}, nil
}

func (t *forwardingTransport) TerminateSession(ctx context.Context, headers http.Header) (int, http.Header, error) {
	if t == nil || t.base == nil {
		return 0, nil, nil
	}
	ctxWithHeaders, carrier, err := internal.ContextWithHeaders(ctx, headers)
	if err != nil {
		return 0, nil, err
	}
	if streamable, ok := unwrapStreamableClientTransport(t.base); ok &&
		(hasResponseDeadlineEnforcement(ctx) || SessionIDFromHeaders(headers) != nil) {
		// go-sdk v1.7 does not send DELETE from Connection.Close unless that
		// specific SDK connection observed the session ID itself. Tunnel
		// session-termination commands carry a session ID established by an
		// earlier, independent command, so issue the DELETE directly here.
		return terminateStreamableSession(ctxWithHeaders, streamable, headers)
	}

	conn, err := t.base.Connect(ctxWithHeaders)
	if err != nil {
		return 0, nil, err
	}
	err = conn.Close()
	statusCode, responseHeaders := carrier.ResponseStatusAndHeaders()
	return statusCode, responseHeaders, err
}

func unwrapStreamableClientTransport(transport mcp.Transport) (*mcp.StreamableClientTransport, bool) {
	switch typed := transport.(type) {
	case *mcp.StreamableClientTransport:
		return typed, typed != nil
	case *mcp.LoggingTransport:
		if typed == nil {
			return nil, false
		}
		return unwrapStreamableClientTransport(typed.Transport)
	default:
		return nil, false
	}
}

// terminateStreamableSession issues the protocol DELETE directly so the
// command context remains attached to the network request and externally
// established session IDs are not lost on a fresh SDK connection. The SDK
// connection Close method uses a detached lifecycle context and only knows
// session IDs observed by that specific connection.
func terminateStreamableSession(ctx context.Context, transport *mcp.StreamableClientTransport, headers http.Header) (int, http.Header, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodDelete, transport.Endpoint, nil)
	if err != nil {
		return 0, nil, err
	}
	if headers != nil {
		req.Header = headers.Clone()
	}

	client := transport.HTTPClient
	if client == nil {
		client = http.DefaultClient
	}
	resp, err := client.Do(req)
	if err != nil {
		return 0, nil, err
	}
	defer func() {
		if resp.Body != nil {
			_ = resp.Body.Close()
		}
	}()
	return resp.StatusCode, resp.Header.Clone(), nil
}

var _ ForwardingConnection = (*forwardingConnection)(nil)

// forwardingConnection delegates all behavior to the internal connection
// implementation while satisfying the public ForwardingConnection interface.
type forwardingConnection struct {
	base mcp.Connection
}

func (c *forwardingConnection) Close() error {
	if c.base == nil {
		return nil
	}
	return c.base.Close()
}

func (c *forwardingConnection) Write(ctx context.Context, header http.Header, msg jsonrpc.Message) (ForwardingWriteResult, error) {
	if c.base == nil {
		return ForwardingWriteResult{}, nil
	}
	ctxWithHeaders, carrier, err := internal.ContextWithHeaders(ctx, header)
	if err != nil {
		return ForwardingWriteResult{}, err
	}

	err = c.base.Write(ctxWithHeaders, msg)
	result := ForwardingWriteResult{}
	if carrier != nil {
		result.StatusCode, result.ResponseHeaders = carrier.ResponseStatusAndHeaders()
	}

	if err != nil && shouldCloseAfterForwardingError(c.base, ctx, err) {
		_ = c.base.Close()
	}

	if result.StatusCode != 0 && (result.StatusCode < http.StatusOK || result.StatusCode >= http.StatusMultipleChoices) {
		preserved, responseErr := preservedMCPError(msg, carrier)
		if preserved != nil {
			result.PreservedError = preserved
			return result, nil
		}
		if responseErr != nil {
			return result, responseErr
		}
	}

	return result, err
}

type rawJSONRPCErrorResponse struct {
	JSONRPC string          `json:"jsonrpc"`
	Method  json.RawMessage `json:"method"`
	Result  json.RawMessage `json:"result"`
	Error   *struct {
		Code    *int64  `json:"code"`
		Message *string `json:"message"`
	} `json:"error"`
}

func preservedMCPError(msg jsonrpc.Message, carrier *internal.HeaderCarrier) (*PreservedMCPError, error) {
	req, ok := msg.(*jsonrpc.Request)
	if !ok || req == nil || !req.ID.IsValid() {
		return nil, newNonProtocolResponseError(NonProtocolResponseInvalidMCPError, nil)
	}
	if carrier == nil {
		return nil, newNonProtocolResponseError(NonProtocolResponseBodyMissing, nil)
	}

	body, tooLarge, readErr, captured := carrier.ResponseBodyCapture()
	if !captured {
		return nil, newNonProtocolResponseError(NonProtocolResponseBodyMissing, nil)
	}
	if readErr != nil {
		return nil, newNonProtocolResponseError(NonProtocolResponseBodyUnreadable, readErr)
	}
	if tooLarge {
		return nil, newNonProtocolResponseError(NonProtocolResponseBodyTooLarge, nil)
	}
	if len(body) == 0 {
		return nil, newNonProtocolResponseError(NonProtocolResponseBodyMissing, nil)
	}

	var raw rawJSONRPCErrorResponse
	if !json.Valid(body) {
		return nil, newNonProtocolResponseError(NonProtocolResponseMalformedJSON, nil)
	}
	if decodeErr := json.Unmarshal(body, &raw); decodeErr != nil {
		return nil, newNonProtocolResponseError(NonProtocolResponseInvalidMCPError, decodeErr)
	}
	if raw.JSONRPC != "2.0" || len(raw.Method) != 0 || len(raw.Result) != 0 || raw.Error == nil || raw.Error.Code == nil || raw.Error.Message == nil {
		return nil, newNonProtocolResponseError(NonProtocolResponseInvalidMCPError, nil)
	}

	decoded, decodeErr := jsonrpc.DecodeMessage(body)
	if decodeErr != nil {
		return nil, newNonProtocolResponseError(NonProtocolResponseInvalidMCPError, decodeErr)
	}
	response, ok := decoded.(*jsonrpc.Response)
	if !ok || response == nil || response.Error == nil || !response.ID.IsValid() || response.ID != req.ID {
		return nil, newNonProtocolResponseError(NonProtocolResponseInvalidMCPError, nil)
	}

	return NewPreservedMCPError(body, *raw.Error.Code), nil
}

func newNonProtocolResponseError(kind NonProtocolResponseKind, cause error) error {
	return &NonProtocolResponseError{kind: kind, cause: cause}
}

func (c *forwardingConnection) Read(ctx context.Context) (jsonrpc.Message, error) {
	if c.base == nil {
		return nil, nil
	}
	msg, err := c.base.Read(ctx)
	if err != nil && shouldCloseAfterForwardingError(c.base, ctx, err) {
		_ = c.base.Close()
	}
	return msg, err
}

type contextCancellationPreservingConnection interface {
	preserveConnectionOnContextCancellation() bool
}

func shouldCloseAfterForwardingError(conn mcp.Connection, ctx context.Context, err error) bool {
	if err == nil {
		return false
	}
	if !errors.Is(err, context.Canceled) && !errors.Is(err, context.DeadlineExceeded) {
		return true
	}
	if !hasResponseDeadlineEnforcement(ctx) {
		return true
	}
	preserving, ok := conn.(contextCancellationPreservingConnection)
	return !ok || !preserving.preserveConnectionOnContextCancellation()
}
