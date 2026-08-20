package runtimeharpoon

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"sync"

	"github.com/modelcontextprotocol/go-sdk/jsonrpc"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// restartableInMemoryTransport creates a fresh in-memory MCP connection and
// server session for every underlying Connect call.
//
// mcp.InMemoryTransport owns one net.Pipe pair and is only safe for one
// Connect. The dispatcher intentionally closes the Harpoon connection when a
// request exits early so late responses cannot leak into the next serialized
// request. Reusing the original InMemoryTransport after that close would reuse
// its closed pipe forever.
type restartableInMemoryTransport struct {
	ctx    context.Context
	server *mcp.Server
	logger *slog.Logger

	mu              sync.Mutex
	stopped         bool
	connections     map[*restartableInMemoryConnection]struct{}
	sessionContexts map[*mcp.ServerSession]context.Context
}

func newRestartableInMemoryTransport(ctx context.Context, server *mcp.Server, logger *slog.Logger) mcp.Transport {
	if ctx == nil || server == nil {
		return nil
	}
	transport := &restartableInMemoryTransport{
		ctx:    ctx,
		server: server,
		logger: logger,
	}
	// go-sdk intentionally detaches server request contexts from the context
	// passed to Server.Connect. Reattach each request to its isolated session
	// context so closing a TTL-expired client pipe also cancels in-flight
	// Harpoon handlers instead of letting them run until their own timeout.
	server.AddReceivingMiddleware(transport.withSessionCancellation)
	go func() {
		<-ctx.Done()
		transport.stop()
	}()
	return transport
}

// NewRestartableInMemoryTransport creates the shared reconnectable in-memory
// transport used by both runtime and package-owned adapters.
func NewRestartableInMemoryTransport(ctx context.Context, server *mcp.Server, logger *slog.Logger) mcp.Transport {
	return newRestartableInMemoryTransport(ctx, server, logger)
}

func (t *restartableInMemoryTransport) Connect(ctx context.Context) (mcp.Connection, error) {
	if t == nil || t.server == nil {
		return nil, nil
	}
	if ctx == nil {
		ctx = context.Background()
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if err := t.ctx.Err(); err != nil {
		return nil, err
	}

	serverTransport, clientTransport := mcp.NewInMemoryTransports()
	sessionCtx, cancel := context.WithCancel(t.ctx)
	session, err := t.server.Connect(sessionCtx, serverTransport, nil)
	if err != nil {
		cancel()
		return nil, err
	}
	t.registerSession(session, sessionCtx)
	go t.wait(session, cancel)

	conn, err := clientTransport.Connect(ctx)
	if err != nil {
		cancel()
		_ = session.Close()
		return nil, err
	}
	connection := &restartableInMemoryConnection{
		base:   conn,
		cancel: cancel,
	}
	connection.onClose = func() { t.unregister(connection) }
	if err := t.register(connection); err != nil {
		_ = connection.Close()
		return nil, err
	}
	return connection, nil
}

func (t *restartableInMemoryTransport) register(connection *restartableInMemoryConnection) error {
	t.mu.Lock()
	defer t.mu.Unlock()
	if err := t.ctx.Err(); err != nil {
		return err
	}
	if t.stopped {
		return context.Canceled
	}
	if t.connections == nil {
		t.connections = make(map[*restartableInMemoryConnection]struct{})
	}
	t.connections[connection] = struct{}{}
	return nil
}

func (t *restartableInMemoryTransport) unregister(connection *restartableInMemoryConnection) {
	t.mu.Lock()
	defer t.mu.Unlock()
	delete(t.connections, connection)
}

func (t *restartableInMemoryTransport) registerSession(session *mcp.ServerSession, ctx context.Context) {
	if t == nil || session == nil || ctx == nil {
		return
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.sessionContexts == nil {
		t.sessionContexts = make(map[*mcp.ServerSession]context.Context)
	}
	t.sessionContexts[session] = ctx
}

func (t *restartableInMemoryTransport) unregisterSession(session *mcp.ServerSession) {
	if t == nil || session == nil {
		return
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	delete(t.sessionContexts, session)
}

func (t *restartableInMemoryTransport) sessionContext(session *mcp.ServerSession) context.Context {
	if t == nil || session == nil {
		return nil
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.sessionContexts[session]
}

func (t *restartableInMemoryTransport) withSessionCancellation(next mcp.MethodHandler) mcp.MethodHandler {
	return func(ctx context.Context, method string, req mcp.Request) (mcp.Result, error) {
		if req == nil {
			return next(ctx, method, req)
		}
		session, ok := req.GetSession().(*mcp.ServerSession)
		if !ok {
			return next(ctx, method, req)
		}
		sessionCtx := t.sessionContext(session)
		if sessionCtx == nil {
			return next(ctx, method, req)
		}

		handlerCtx, cancel := context.WithCancel(ctx)
		stop := context.AfterFunc(sessionCtx, cancel)
		if sessionCtx.Err() != nil {
			cancel()
		}
		defer func() {
			stop()
			cancel()
		}()
		return next(handlerCtx, method, req)
	}
}

// stop closes every current client-side pipe. The SDK detaches Server.Connect
// from context cancellation, so canceling the root context alone would leave
// idle sessions blocked in Wait during Fx shutdown.
func (t *restartableInMemoryTransport) stop() {
	t.mu.Lock()
	if t.stopped {
		t.mu.Unlock()
		return
	}
	t.stopped = true
	connections := make([]*restartableInMemoryConnection, 0, len(t.connections))
	for connection := range t.connections {
		connections = append(connections, connection)
	}
	t.mu.Unlock()

	for _, connection := range connections {
		_ = connection.Close()
	}
}

// wait observes the server side of one isolated in-memory session. A timed-out
// request may keep its handler alive while it unwinds, so reconnects must not
// wait here before creating their own pipe and session.
func (t *restartableInMemoryTransport) wait(session *mcp.ServerSession, cancel context.CancelFunc) {
	defer func() {
		cancel()
		t.unregisterSession(session)
	}()
	err := session.Wait()
	if err != nil &&
		!errors.Is(err, context.Canceled) &&
		!errors.Is(err, io.EOF) &&
		!errors.Is(err, io.ErrClosedPipe) &&
		t.logger != nil {
		t.logger.Warn("harpoon in-memory server session stopped", slog.String("error", err.Error()))
	}
}

type restartableInMemoryConnection struct {
	base    mcp.Connection
	cancel  context.CancelFunc
	onClose func()

	closeOnce sync.Once
	closeErr  error
}

func (c *restartableInMemoryConnection) Read(ctx context.Context) (jsonrpc.Message, error) {
	if c == nil || c.base == nil {
		return nil, nil
	}
	return c.base.Read(ctx)
}

func (c *restartableInMemoryConnection) Write(ctx context.Context, msg jsonrpc.Message) error {
	if c == nil || c.base == nil {
		return nil
	}
	return c.base.Write(ctx, msg)
}

func (c *restartableInMemoryConnection) Close() error {
	if c == nil || c.base == nil {
		return nil
	}
	c.closeOnce.Do(func() {
		if c.cancel != nil {
			c.cancel()
		}
		c.closeErr = c.base.Close()
		if c.onClose != nil {
			c.onClose()
		}
	})
	return c.closeErr
}

func (c *restartableInMemoryConnection) SessionID() string {
	if c == nil || c.base == nil {
		return ""
	}
	return c.base.SessionID()
}
