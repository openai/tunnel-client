package mcpclient

import (
	"context"
	"sync"

	"github.com/modelcontextprotocol/go-sdk/jsonrpc"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

type sharedConnectionTransport struct {
	base                          mcp.Transport
	preserveOnContextCancellation bool
	mu                            sync.Mutex
	conn                          mcp.Connection
}

// NewSharedConnectionTransport returns a transport wrapper that reuses the
// same underlying connection across Connect calls.
func NewSharedConnectionTransport(base mcp.Transport) mcp.Transport {
	return newSharedConnectionTransport(base)
}

func newSharedConnectionTransport(base mcp.Transport) mcp.Transport {
	return newSharedConnectionTransportWithContextCancellationPolicy(base, false)
}

// newContextCancellationPreservingSharedConnectionTransport keeps the shared
// physical connection open when one logical request context expires. Stdio
// uses this because all logical connections reuse the same child-process
// stdin/stdout pipes.
func newContextCancellationPreservingSharedConnectionTransport(base mcp.Transport) mcp.Transport {
	return newSharedConnectionTransportWithContextCancellationPolicy(base, true)
}

func newSharedConnectionTransportWithContextCancellationPolicy(base mcp.Transport, preserveOnContextCancellation bool) mcp.Transport {
	if base == nil {
		return nil
	}
	return &sharedConnectionTransport{
		base:                          base,
		preserveOnContextCancellation: preserveOnContextCancellation,
	}
}

func (t *sharedConnectionTransport) Connect(ctx context.Context) (mcp.Connection, error) {
	if t == nil || t.base == nil {
		return nil, nil
	}
	t.mu.Lock()
	if t.conn != nil {
		conn := t.conn
		t.mu.Unlock()
		return conn, nil
	}
	conn, err := t.base.Connect(ctx)
	if err != nil {
		t.mu.Unlock()
		return nil, err
	}
	var sharedConn *sharedConnection
	sharedConn = &sharedConnection{
		base:                          conn,
		preserveOnContextCancellation: t.preserveOnContextCancellation,
		onClose: func() {
			t.mu.Lock()
			defer t.mu.Unlock()
			if t.conn == sharedConn {
				t.conn = nil
			}
		},
	}
	t.conn = sharedConn
	t.mu.Unlock()
	return sharedConn, nil
}

type sharedConnection struct {
	base                          mcp.Connection
	preserveOnContextCancellation bool
	onClose                       func()
}

func (c *sharedConnection) preserveConnectionOnContextCancellation() bool {
	return c != nil && c.preserveOnContextCancellation
}

func (c *sharedConnection) Read(ctx context.Context) (jsonrpc.Message, error) {
	if c == nil || c.base == nil {
		return nil, nil
	}
	return c.base.Read(ctx)
}

func (c *sharedConnection) Write(ctx context.Context, msg jsonrpc.Message) error {
	if c == nil || c.base == nil {
		return nil
	}
	return c.base.Write(ctx, msg)
}

func (c *sharedConnection) Close() error {
	if c == nil || c.base == nil {
		return nil
	}
	if c.onClose != nil {
		c.onClose()
	}
	return c.base.Close()
}

func (c *sharedConnection) SessionID() string {
	if c == nil || c.base == nil {
		return ""
	}
	return c.base.SessionID()
}
