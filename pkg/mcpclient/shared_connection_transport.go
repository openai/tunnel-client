package mcpclient

import (
	"context"
	"io"
	"sync"

	"github.com/modelcontextprotocol/go-sdk/jsonrpc"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

type sharedConnectionTransport struct {
	base                          mcp.Transport
	preserveOnContextCancellation bool
	restartOnInitialize           bool
	mu                            sync.Mutex
	conn                          *sharedConnection
}

// NewSharedConnectionTransport returns a transport wrapper that reuses the
// same underlying connection across Connect calls.
func NewSharedConnectionTransport(base mcp.Transport) mcp.Transport {
	return newSharedConnectionTransport(base)
}

// NewInitializeRestartingSharedConnectionTransport returns a shared transport
// that starts a fresh underlying connection whenever a later logical request
// sends initialize.
//
// Tunnel-service's legacy Harpoon client opens a new MCP client session for
// each OAuth shim call, while tunnel-client serializes those calls over one
// in-memory transport. go-sdk v1.7 rejects a second initialize on an already
// initialized server session, so Harpoon uses this narrow compatibility
// wrapper to preserve the old initialize/initialized flow without changing
// self-contained 2026 requests.
//
// Callers must serialize logical request lifecycles while using this wrapper;
// Harpoon satisfies that requirement with its serialized forwarding transport.
func NewInitializeRestartingSharedConnectionTransport(base mcp.Transport) mcp.Transport {
	return newSharedConnectionTransportWithPolicies(base, false, true)
}

func newSharedConnectionTransport(base mcp.Transport) mcp.Transport {
	return newSharedConnectionTransportWithPolicies(base, false, false)
}

// newContextCancellationPreservingSharedConnectionTransport keeps the shared
// physical connection open when one logical request context expires. Stdio
// uses this because all logical connections reuse the same child-process
// stdin/stdout pipes.
func newContextCancellationPreservingSharedConnectionTransport(base mcp.Transport) mcp.Transport {
	return newSharedConnectionTransportWithPolicies(base, true, false)
}

func newSharedConnectionTransportWithPolicies(base mcp.Transport, preserveOnContextCancellation, restartOnInitialize bool) mcp.Transport {
	if base == nil {
		return nil
	}
	return &sharedConnectionTransport{
		base:                          base,
		preserveOnContextCancellation: preserveOnContextCancellation,
		restartOnInitialize:           restartOnInitialize,
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
	sharedConn := &sharedConnection{
		base:                          conn,
		owner:                         t,
		preserveOnContextCancellation: t.preserveOnContextCancellation,
	}
	t.conn = sharedConn
	t.mu.Unlock()
	return sharedConn, nil
}

type sharedConnection struct {
	base                          mcp.Connection
	owner                         *sharedConnectionTransport
	preserveOnContextCancellation bool
	hasWritten                    bool
}

func (c *sharedConnection) preserveConnectionOnContextCancellation() bool {
	return c != nil && c.preserveOnContextCancellation
}

func (c *sharedConnection) Read(ctx context.Context) (jsonrpc.Message, error) {
	base := c.currentBase()
	if base == nil {
		return nil, io.ErrClosedPipe
	}
	return base.Read(ctx)
}

func (c *sharedConnection) Write(ctx context.Context, msg jsonrpc.Message) error {
	if c == nil {
		return nil
	}
	if err := c.restartForInitialize(ctx, msg); err != nil {
		return err
	}
	base := c.currentBase()
	if base == nil {
		return io.ErrClosedPipe
	}
	err := base.Write(ctx, msg)
	if err == nil {
		c.markWritten()
	}
	return err
}

func (c *sharedConnection) Close() error {
	if c == nil {
		return nil
	}
	base := c.detach()
	if base == nil {
		return nil
	}
	return base.Close()
}

func (c *sharedConnection) SessionID() string {
	base := c.currentBase()
	if base == nil {
		return ""
	}
	return base.SessionID()
}

func (c *sharedConnection) currentBase() mcp.Connection {
	if c == nil {
		return nil
	}
	if c.owner == nil {
		return c.base
	}
	c.owner.mu.Lock()
	defer c.owner.mu.Unlock()
	return c.base
}

func (c *sharedConnection) markWritten() {
	if c == nil {
		return
	}
	if c.owner == nil {
		c.hasWritten = true
		return
	}
	c.owner.mu.Lock()
	c.hasWritten = true
	c.owner.mu.Unlock()
}

func (c *sharedConnection) restartForInitialize(ctx context.Context, msg jsonrpc.Message) error {
	if c == nil || c.owner == nil || !c.owner.restartOnInitialize || !isInitializeRequest(msg) {
		return nil
	}

	c.owner.mu.Lock()
	if c.owner.conn != c || !c.hasWritten {
		c.owner.mu.Unlock()
		return nil
	}
	oldBase := c.base
	newBase, err := c.owner.base.Connect(ctx)
	if err != nil {
		c.owner.mu.Unlock()
		return err
	}
	c.base = newBase
	c.hasWritten = false
	c.owner.mu.Unlock()

	if oldBase != nil {
		_ = oldBase.Close()
	}
	return nil
}

func (c *sharedConnection) detach() mcp.Connection {
	if c == nil {
		return nil
	}
	if c.owner == nil {
		base := c.base
		c.base = nil
		return base
	}
	c.owner.mu.Lock()
	defer c.owner.mu.Unlock()
	if c.owner.conn == c {
		c.owner.conn = nil
	}
	base := c.base
	c.base = nil
	c.hasWritten = false
	return base
}

func isInitializeRequest(msg jsonrpc.Message) bool {
	request, ok := msg.(*jsonrpc.Request)
	return ok && request != nil && request.Method == "initialize"
}
