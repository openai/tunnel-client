package mcpclient

import (
	"context"
	"errors"
	"io"
	"testing"
	"time"

	"github.com/modelcontextprotocol/go-sdk/jsonrpc"
	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/stretchr/testify/require"
)

var errTestConnect = errors.New("connect failed")

func TestNewSharedConnectionTransportReusesConnection(t *testing.T) {
	t.Parallel()

	base := &countingTransport{
		connectFn: func() (mcp.Connection, error) {
			return &fakeSharedConn{}, nil
		},
	}

	shared := NewSharedConnectionTransport(base)
	require.NotNil(t, shared)

	connA, err := shared.Connect(context.Background())
	require.NoError(t, err)
	require.NotNil(t, connA)

	connB, err := shared.Connect(context.Background())
	require.NoError(t, err)
	require.NotNil(t, connB)

	require.Same(t, connA, connB)
	require.Equal(t, 1, base.connectCalls)
}

func TestNewSharedConnectionTransportNilBase(t *testing.T) {
	t.Parallel()

	require.Nil(t, NewSharedConnectionTransport(nil))
	require.Nil(t, NewInitializeRestartingSharedConnectionTransport(nil))
}

func TestInitializeRestartingSharedConnectionTransportReconnectsForLaterInitialize(t *testing.T) {
	t.Parallel()

	var connections []*closeTrackingConn
	base := &countingTransport{
		connectFn: func() (mcp.Connection, error) {
			conn := &closeTrackingConn{}
			connections = append(connections, conn)
			return conn, nil
		},
	}

	shared := NewInitializeRestartingSharedConnectionTransport(base)
	require.NotNil(t, shared)

	conn, err := shared.Connect(context.Background())
	require.NoError(t, err)

	firstInitialize := &jsonrpc.Request{Method: "initialize"}
	require.NoError(t, conn.Write(context.Background(), firstInitialize))
	require.Equal(t, 1, base.connectCalls, "first initialize should use the initial connection")

	require.NoError(t, conn.Write(context.Background(), &jsonrpc.Request{Method: "tools/list"}))
	require.NoError(t, conn.Write(context.Background(), &jsonrpc.Request{Method: "initialize"}))

	require.Equal(t, 2, base.connectCalls)
	require.Len(t, connections, 2)
	require.Equal(t, 1, connections[0].closed)
	require.Equal(t, []string{"initialize", "tools/list"}, connections[0].writtenMethods())
	require.Equal(t, []string{"initialize"}, connections[1].writtenMethods())
}

func TestSharedConnectionTransportDoesNotRestartForInitializeByDefault(t *testing.T) {
	t.Parallel()

	var connections []*closeTrackingConn
	base := &countingTransport{
		connectFn: func() (mcp.Connection, error) {
			conn := &closeTrackingConn{}
			connections = append(connections, conn)
			return conn, nil
		},
	}

	shared := NewSharedConnectionTransport(base)
	require.NotNil(t, shared)

	conn, err := shared.Connect(context.Background())
	require.NoError(t, err)
	require.NoError(t, conn.Write(context.Background(), &jsonrpc.Request{Method: "initialize"}))
	require.NoError(t, conn.Write(context.Background(), &jsonrpc.Request{Method: "initialize"}))

	require.Equal(t, 1, base.connectCalls)
	require.Len(t, connections, 1)
	require.Zero(t, connections[0].closed)
	require.Equal(t, []string{"initialize", "initialize"}, connections[0].writtenMethods())
}

func TestNewSharedConnectionTransportRetriesAfterFailure(t *testing.T) {
	t.Parallel()

	var base *countingTransport
	base = &countingTransport{
		connectFn: func() (mcp.Connection, error) {
			if base.connectCalls == 0 {
				return nil, errTestConnect
			}
			return &fakeSharedConn{}, nil
		},
	}

	shared := NewSharedConnectionTransport(base)
	require.NotNil(t, shared)

	conn, err := shared.Connect(context.Background())
	require.ErrorIs(t, err, errTestConnect)
	require.Nil(t, conn)

	conn, err = shared.Connect(context.Background())
	require.NoError(t, err)
	require.NotNil(t, conn)
	require.Equal(t, 2, base.connectCalls)
}

func TestNewSharedConnectionTransportReconnectsAfterClose(t *testing.T) {
	t.Parallel()

	closer := &countingTransport{
		connectFn: func() (mcp.Connection, error) {
			return &closeTrackingConn{}, nil
		},
	}

	shared := NewSharedConnectionTransport(closer)
	require.NotNil(t, shared)

	conn, err := shared.Connect(context.Background())
	require.NoError(t, err)
	require.NotNil(t, conn)

	require.NoError(t, conn.Close())

	conn2, err := shared.Connect(context.Background())
	require.NoError(t, err)
	require.NotNil(t, conn2)
	require.NotSame(t, conn, conn2)
	require.Equal(t, 2, closer.connectCalls)
}

func TestSharedConnectionReadAfterCloseReturnsClosedPipe(t *testing.T) {
	t.Parallel()

	shared := NewSharedConnectionTransport(&countingTransport{
		connectFn: func() (mcp.Connection, error) {
			return &closeTrackingConn{}, nil
		},
	})
	require.NotNil(t, shared)

	conn, err := shared.Connect(context.Background())
	require.NoError(t, err)
	require.NoError(t, conn.Close())

	_, err = conn.Read(context.Background())
	require.ErrorIs(t, err, io.ErrClosedPipe)
}

func TestSharedConnectionWriteAfterCloseReturnsClosedPipe(t *testing.T) {
	t.Parallel()

	base := &closeTrackingConn{}
	shared := NewSharedConnectionTransport(&countingTransport{
		connectFn: func() (mcp.Connection, error) { return base, nil },
	})
	conn, err := shared.Connect(context.Background())
	require.NoError(t, err)
	require.NoError(t, conn.Close())

	err = conn.Write(context.Background(), &jsonrpc.Request{Method: "notifications/initialized"})
	require.ErrorIs(t, err, io.ErrClosedPipe)
	require.Empty(t, base.writes)
}

func TestSharedConnectionLateCloseReturnsWriteErrorAndReconnects(t *testing.T) {
	t.Parallel()

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	id, err := jsonrpc.MakeID("request-a")
	require.NoError(t, err)
	response := &jsonrpc.Response{ID: id}
	var connections []*responseTrackingSharedConn
	base := &countingTransport{
		connectFn: func() (mcp.Connection, error) {
			conn := &responseTrackingSharedConn{response: response}
			connections = append(connections, conn)
			return conn, nil
		},
	}
	transport := NewSerializedForwardingTransport(NewForwardingTransport(NewSharedConnectionTransport(base)))
	first, err := transport.Connect(ctx)
	require.NoError(t, err)
	_, err = first.Write(ctx, nil, &jsonrpc.Request{ID: id, Method: "tools/list"})
	require.NoError(t, err)
	msg, err := first.Read(ctx)
	require.NoError(t, err)
	require.Same(t, response, msg)

	// Reading the terminal response admits another command before response-post cleanup.
	second, err := transport.Connect(ctx)
	require.NoError(t, err)
	require.Equal(t, 1, base.connectCalls)
	require.NoError(t, first.Close())
	_, err = second.Write(ctx, nil, &jsonrpc.Request{Method: "notifications/initialized"})
	require.ErrorIs(t, err, io.ErrClosedPipe)
	require.Equal(t, []string{"tools/list"}, connections[0].writtenMethods())
	require.Equal(t, 1, connections[0].closed)

	// The failed write releases serialization and the next command gets a fresh connection.
	third, err := transport.Connect(ctx)
	require.NoError(t, err)
	require.Equal(t, 2, base.connectCalls)
	require.Len(t, connections, 2)
	require.NotSame(t, connections[0], connections[1])
	_, err = third.Write(ctx, nil, &jsonrpc.Request{Method: "notifications/initialized"})
	require.NoError(t, err)
	require.Equal(t, []string{"tools/list"}, connections[0].writtenMethods())
	require.Equal(t, []string{"notifications/initialized"}, connections[1].writtenMethods())
	require.Zero(t, connections[1].closed)
	require.NoError(t, second.Close())
	require.NoError(t, third.Close())
}

func TestNewSharedConnectionTransportReconnectsAfterForwardingWriteError(t *testing.T) {
	t.Parallel()

	writeErr := errors.New("write failed")
	var base *countingTransport
	base = &countingTransport{
		connectFn: func() (mcp.Connection, error) {
			if base.connectCalls == 0 {
				return &closeTrackingConn{writeErr: writeErr}, nil
			}
			return &closeTrackingConn{}, nil
		},
	}

	forwarding := NewForwardingTransport(NewSharedConnectionTransport(base))
	require.NotNil(t, forwarding)

	conn, err := forwarding.Connect(context.Background())
	require.NoError(t, err)
	require.NotNil(t, conn)

	req := &jsonrpc.Request{Method: "testMethod"}
	_, err = conn.Write(context.Background(), nil, req)
	require.ErrorIs(t, err, writeErr)

	conn2, err := forwarding.Connect(context.Background())
	require.NoError(t, err)
	require.NotNil(t, conn2)
	require.NotSame(t, conn, conn2)
	require.Equal(t, 2, base.connectCalls)
}

type countingTransport struct {
	connectCalls int
	connectFn    func() (mcp.Connection, error)
}

func (t *countingTransport) Connect(context.Context) (mcp.Connection, error) {
	conn, err := t.connectFn()
	t.connectCalls++
	return conn, err
}

type fakeSharedConn struct{}

func (fakeSharedConn) Read(context.Context) (jsonrpc.Message, error) { return nil, nil }
func (fakeSharedConn) Write(context.Context, jsonrpc.Message) error  { return nil }
func (fakeSharedConn) Close() error                                  { return nil }
func (fakeSharedConn) SessionID() string                             { return "" }

type closeTrackingConn struct {
	closed   int
	readErr  error
	writeErr error
	writes   []jsonrpc.Message
}

type responseTrackingSharedConn struct {
	closeTrackingConn
	response jsonrpc.Message
}

func (c *responseTrackingSharedConn) Read(context.Context) (jsonrpc.Message, error) {
	return c.response, nil
}

func (c *closeTrackingConn) Read(context.Context) (jsonrpc.Message, error) {
	return nil, c.readErr
}

func (c *closeTrackingConn) Write(_ context.Context, msg jsonrpc.Message) error {
	c.writes = append(c.writes, msg)
	return c.writeErr
}

func (c *closeTrackingConn) Close() error {
	c.closed++
	return nil
}

func (c *closeTrackingConn) SessionID() string { return "" }

func (c *closeTrackingConn) writtenMethods() []string {
	methods := make([]string, 0, len(c.writes))
	for _, msg := range c.writes {
		request, ok := msg.(*jsonrpc.Request)
		if !ok || request == nil {
			continue
		}
		methods = append(methods, request.Method)
	}
	return methods
}
