package mcpclient

import (
	"context"
	"net/http"

	"github.com/modelcontextprotocol/go-sdk/jsonrpc"
	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/openai/tunnel-client/pkg/mcpclient/internal"
)

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
	conn, err := t.base.Connect(ctxWithHeaders)
	if err != nil {
		return 0, nil, err
	}
	err = conn.Close()
	statusCode, responseHeaders := carrier.ResponseStatusAndHeaders()
	return statusCode, responseHeaders, err
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

func (c *forwardingConnection) Write(ctx context.Context, header http.Header, msg jsonrpc.Message) (int, http.Header, error) {
	if c.base == nil {
		return 0, nil, nil
	}
	ctxWithHeaders, carrier, err := internal.ContextWithHeaders(ctx, header)
	if err != nil {
		return 0, nil, err
	}

	err = c.base.Write(ctxWithHeaders, msg)
	var (
		respHeaders http.Header
		statusCode  int
	)
	if carrier != nil {
		statusCode, respHeaders = carrier.ResponseStatusAndHeaders()
	}

	if err != nil {
		_ = c.base.Close()
	}

	return statusCode, respHeaders, err
}

func (c *forwardingConnection) Read(ctx context.Context) (jsonrpc.Message, error) {
	if c.base == nil {
		return nil, nil
	}
	msg, err := c.base.Read(ctx)
	if err != nil {
		_ = c.base.Close()
	}
	return msg, err
}
