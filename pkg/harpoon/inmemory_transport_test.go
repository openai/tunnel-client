package harpoon

import (
	"context"
	"io"
	"log/slog"
	"sync"
	"testing"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/stretchr/testify/require"

	"github.com/openai/tunnel-client/pkg/mcpclient"
)

func TestRestartableInMemoryTransportReconnectsAfterClose(t *testing.T) {
	t.Parallel()

	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)

	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	server := mcp.NewServer(&mcp.Implementation{Name: "harpoon", Version: "test"}, nil)
	transport := mcpclient.NewSharedConnectionTransport(
		newRestartableInMemoryTransport(ctx, server, logger),
	)
	require.NotNil(t, transport)

	client := mcp.NewClient(&mcp.Implementation{Name: "test-client", Version: "test"}, nil)

	firstCtx, firstCancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer firstCancel()
	first, err := client.Connect(firstCtx, transport, nil)
	require.NoError(t, err)
	require.NotNil(t, first.InitializeResult())
	require.NoError(t, first.Close())

	secondCtx, secondCancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer secondCancel()
	second, err := client.Connect(secondCtx, transport, nil)
	require.NoError(t, err)
	require.NotNil(t, second.InitializeResult())
	require.NoError(t, second.Close())
}

func TestRestartableInMemoryTransportClosesActiveConnectionsOnStop(t *testing.T) {
	t.Parallel()

	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)

	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	server := mcp.NewServer(&mcp.Implementation{Name: "harpoon", Version: "test"}, nil)
	base := newRestartableInMemoryTransport(ctx, server, logger)
	shared := mcpclient.NewSharedConnectionTransport(base)
	require.NotNil(t, shared)

	client := mcp.NewClient(&mcp.Implementation{Name: "test-client", Version: "test"}, nil)
	connectCtx, connectCancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer connectCancel()
	session, err := client.Connect(connectCtx, shared, nil)
	require.NoError(t, err)

	waitDone := make(chan error, 1)
	go func() { waitDone <- session.Wait() }()
	cancel()
	select {
	case <-waitDone:
	case <-time.After(5 * time.Second):
		t.Fatal("timed out waiting for active connection to close")
	}

	_, err = base.Connect(context.Background())
	require.ErrorIs(t, err, context.Canceled)
	_ = session.Close()
}

func TestRestartableInMemoryTransportReconnectsWhilePriorHandlerIsRunning(t *testing.T) {
	t.Parallel()

	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)

	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	server := mcp.NewServer(&mcp.Implementation{Name: "harpoon", Version: "test"}, nil)
	started := make(chan struct{}, 1)
	release := make(chan struct{})
	var releaseOnce sync.Once
	releaseHandler := func() {
		releaseOnce.Do(func() { close(release) })
	}
	t.Cleanup(releaseHandler)
	mcp.AddTool(server, &mcp.Tool{Name: "block"}, func(context.Context, *mcp.CallToolRequest, any) (*mcp.CallToolResult, any, error) {
		started <- struct{}{}
		<-release
		return &mcp.CallToolResult{}, nil, nil
	})

	shared := mcpclient.NewSharedConnectionTransport(
		newRestartableInMemoryTransport(ctx, server, logger),
	)
	require.NotNil(t, shared)

	firstTransport := &capturingTransport{base: shared}
	firstClient := mcp.NewClient(&mcp.Implementation{Name: "test-client-1", Version: "test"}, nil)
	firstCtx, firstCancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer firstCancel()
	first, err := firstClient.Connect(firstCtx, firstTransport, nil)
	require.NoError(t, err)
	require.NotNil(t, firstTransport.conn)

	callDone := make(chan error, 1)
	go func() {
		_, err := first.CallTool(context.Background(), &mcp.CallToolParams{Name: "block"})
		callDone <- err
	}()
	select {
	case <-started:
	case <-time.After(5 * time.Second):
		t.Fatal("timed out waiting for first handler to start")
	}

	// Dispatcher TTL expiry closes the shared transport connection directly,
	// while the server-side handler may still be unwinding.
	require.NoError(t, firstTransport.conn.Close())

	secondClient := mcp.NewClient(&mcp.Implementation{Name: "test-client-2", Version: "test"}, nil)
	secondCtx, secondCancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer secondCancel()
	second, err := secondClient.Connect(secondCtx, shared, nil)
	require.NoError(t, err)
	require.NotNil(t, second.InitializeResult())
	require.NoError(t, second.Close())

	releaseHandler()
	select {
	case <-callDone:
	case <-time.After(5 * time.Second):
		t.Fatal("timed out waiting for first handler to finish")
	}
	_ = first.Close()
}

func TestRestartableInMemoryTransportCancelsPriorHandlerOnClose(t *testing.T) {
	t.Parallel()

	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)

	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	server := mcp.NewServer(&mcp.Implementation{Name: "harpoon", Version: "test"}, nil)
	started := make(chan struct{}, 1)
	handlerCanceled := make(chan struct{})
	mcp.AddTool(server, &mcp.Tool{Name: "block-until-canceled"}, func(ctx context.Context, _ *mcp.CallToolRequest, _ any) (*mcp.CallToolResult, any, error) {
		started <- struct{}{}
		<-ctx.Done()
		close(handlerCanceled)
		return &mcp.CallToolResult{}, nil, ctx.Err()
	})

	shared := mcpclient.NewSharedConnectionTransport(
		newRestartableInMemoryTransport(ctx, server, logger),
	)
	require.NotNil(t, shared)

	capturing := &capturingTransport{base: shared}
	client := mcp.NewClient(&mcp.Implementation{Name: "test-client", Version: "test"}, nil)
	connectCtx, connectCancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer connectCancel()
	session, err := client.Connect(connectCtx, capturing, nil)
	require.NoError(t, err)
	require.NotNil(t, capturing.conn)

	callDone := make(chan error, 1)
	go func() {
		_, err := session.CallTool(context.Background(), &mcp.CallToolParams{Name: "block-until-canceled"})
		callDone <- err
	}()
	select {
	case <-started:
	case <-time.After(5 * time.Second):
		t.Fatal("timed out waiting for handler to start")
	}

	require.NoError(t, capturing.conn.Close())
	select {
	case <-handlerCanceled:
	case <-time.After(5 * time.Second):
		t.Fatal("timed out waiting for closed session to cancel handler")
	}
	select {
	case <-callDone:
	case <-time.After(5 * time.Second):
		t.Fatal("timed out waiting for canceled call to return")
	}
	_ = session.Close()
}

type capturingTransport struct {
	base mcp.Transport
	conn mcp.Connection
}

func (t *capturingTransport) Connect(ctx context.Context) (mcp.Connection, error) {
	conn, err := t.base.Connect(ctx)
	if err != nil {
		return nil, err
	}
	t.conn = conn
	return conn, nil
}
