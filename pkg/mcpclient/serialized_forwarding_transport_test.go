package mcpclient

import (
	"bufio"
	"context"
	"errors"
	"io"
	"net/http"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/modelcontextprotocol/go-sdk/jsonrpc"
	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/stretchr/testify/require"
)

func TestNewSerializedForwardingTransportNilBaseReturnsNil(t *testing.T) {
	t.Parallel()

	require.Nil(t, NewSerializedForwardingTransport(nil))
	require.Nil(t, NewSerializedForwardingTransportWithDeadlineRetirement(nil))
}

func TestSerializedForwardingTransportHoldsLifecycleLockUntilMatchingResponse(t *testing.T) {
	t.Parallel()

	baseConn := newStubSerializedForwardingConnection()
	transport := NewSerializedForwardingTransport(&stubSerializedForwardingTransport{
		conn: baseConn,
	})
	require.NotNil(t, transport)
	serializedTransport := transport.(*serializedForwardingTransport)

	connA, err := transport.Connect(context.Background())
	require.NoError(t, err)
	idA, err := jsonrpc.MakeID("a")
	require.NoError(t, err)
	reqA := &jsonrpc.Request{ID: idA, Method: "tools/call"}
	_, err = connA.Write(context.Background(), nil, reqA)
	require.NoError(t, err)
	requireLifecycleLockHeld(t, serializedTransport)

	notification := &jsonrpc.Request{Method: "notifications/progress"}
	baseConn.enqueueRead(notification, nil)
	msg, err := connA.Read(context.Background())
	require.NoError(t, err)
	require.Same(t, notification, msg)
	requireLifecycleLockHeld(t, serializedTransport)

	responseA := &jsonrpc.Response{ID: idA}
	baseConn.enqueueRead(responseA, nil)
	msg, err = connA.Read(context.Background())
	require.NoError(t, err)
	require.Same(t, responseA, msg)
	requireLifecycleLockReleased(t, serializedTransport)

	idB, err := jsonrpc.MakeID("b")
	require.NoError(t, err)
	reqB := &jsonrpc.Request{ID: idB, Method: "tools/call"}
	_, err = connA.Write(context.Background(), nil, reqB)
	require.NoError(t, err)
	requireLifecycleLockHeld(t, serializedTransport)

	require.NoError(t, connA.Close())
}

func TestSerializedForwardingTransportReleasesAfterUpstreamErrorStatus(t *testing.T) {
	t.Parallel()

	baseConn := newStubSerializedForwardingConnection()
	baseConn.enqueueWriteResult(http.StatusBadGateway, nil, nil)
	baseConn.enqueueWriteResult(http.StatusOK, nil, nil)

	transport := NewSerializedForwardingTransport(&stubSerializedForwardingTransport{
		conn: baseConn,
	})
	require.NotNil(t, transport)

	connA, err := transport.Connect(context.Background())
	require.NoError(t, err)
	idA, err := jsonrpc.MakeID("a")
	require.NoError(t, err)
	reqA := &jsonrpc.Request{ID: idA, Method: "tools/call"}
	result, err := connA.Write(context.Background(), nil, reqA)
	require.NoError(t, err)
	require.Equal(t, http.StatusBadGateway, result.StatusCode)

	connB, err := transport.Connect(context.Background())
	require.NoError(t, err)
	idB, err := jsonrpc.MakeID("b")
	require.NoError(t, err)
	reqB := &jsonrpc.Request{ID: idB, Method: "tools/call"}
	result, err = connB.Write(context.Background(), nil, reqB)
	require.NoError(t, err)
	require.Equal(t, http.StatusOK, result.StatusCode)

	require.NoError(t, connB.Close())
}

func TestSerializedForwardingTransportCanceledWaiterDoesNotConnectBase(t *testing.T) {
	t.Parallel()

	baseConn := newStubSerializedForwardingConnection()
	baseTransport := &stubSerializedForwardingTransport{conn: baseConn}
	transport := NewSerializedForwardingTransport(baseTransport)

	first, err := transport.Connect(context.Background())
	require.NoError(t, err)
	require.EqualValues(t, 1, baseTransport.connectCalls.Load())

	waitCtx, cancelWait := context.WithCancel(context.Background())
	doneObserved := make(chan struct{})
	waitCtx = &doneObservedContext{Context: waitCtx, observed: doneObserved}
	result := make(chan serializedConnectResult, 1)
	go func() {
		conn, connectErr := transport.Connect(waitCtx)
		result <- serializedConnectResult{conn: conn, err: connectErr}
	}()

	waitForSerializedSignal(t, doneObserved, "second Connect to wait for the lifecycle slot")
	cancelWait()
	got := waitForSerializedConnectResult(t, result)
	require.Nil(t, got.conn)
	require.ErrorIs(t, got.err, context.Canceled)
	require.EqualValues(t, 1, baseTransport.connectCalls.Load(), "canceled waiter must not call base.Connect")

	require.NoError(t, first.Close())
	third, err := transport.Connect(context.Background())
	require.NoError(t, err)
	require.EqualValues(t, 2, baseTransport.connectCalls.Load(), "released slot should admit the next lifecycle")
	require.NoError(t, third.Close())
}

func TestSerializedForwardingTransportReleasesAfterBaseConnectError(t *testing.T) {
	t.Parallel()

	baseErr := errors.New("connect failed")
	baseTransport := &failOnceSerializedForwardingTransport{
		conn: newStubSerializedForwardingConnection(),
		err:  baseErr,
	}
	transport := NewSerializedForwardingTransport(baseTransport)

	conn, err := transport.Connect(context.Background())
	require.Nil(t, conn)
	require.ErrorIs(t, err, baseErr)

	conn, err = transport.Connect(context.Background())
	require.NoError(t, err)
	require.NotNil(t, conn)
	require.NoError(t, conn.Close())
	require.EqualValues(t, 2, baseTransport.connectCalls.Load())
}

func TestSerializedForwardingTransportClosesAndReleasesWhenContextExpiresDuringBaseConnect(t *testing.T) {
	t.Parallel()

	firstConn := newStubSerializedForwardingConnection()
	secondConn := newStubSerializedForwardingConnection()
	baseTransport := &blockingFirstSerializedForwardingTransport{
		firstConn:  firstConn,
		secondConn: secondConn,
		started:    make(chan struct{}),
		release:    make(chan struct{}),
	}
	transport := NewSerializedForwardingTransport(baseTransport)
	ctx, cancel := context.WithCancel(context.Background())
	result := make(chan serializedConnectResult, 1)
	go func() {
		conn, connectErr := transport.Connect(ctx)
		result <- serializedConnectResult{conn: conn, err: connectErr}
	}()

	waitForSerializedSignal(t, baseTransport.started, "base Connect to start")
	cancel()
	close(baseTransport.release)
	got := waitForSerializedConnectResult(t, result)
	require.Nil(t, got.conn)
	require.ErrorIs(t, got.err, context.Canceled)
	require.EqualValues(t, 1, firstConn.closeCalls.Load(), "connection returned after cancellation must be closed")

	next, err := transport.Connect(context.Background())
	require.NoError(t, err)
	require.NotNil(t, next)
	require.EqualValues(t, 2, baseTransport.connectCalls.Load())
	require.NoError(t, next.Close())
}

func TestSerializedForwardingTransportReleasesAfterPreservedMCPError(t *testing.T) {
	t.Parallel()

	baseConn := newStubSerializedForwardingConnection()
	baseConn.writeResults <- stubWriteResult{
		statusCode:     http.StatusFound,
		preservedError: NewPreservedMCPError([]byte(`{"jsonrpc":"2.0","id":"a","error":{"code":-32003,"message":"capability"}}`), -32003),
	}
	baseConn.enqueueWriteResult(http.StatusOK, nil, nil)

	transport := NewSerializedForwardingTransport(&stubSerializedForwardingTransport{conn: baseConn})
	serializedTransport := transport.(*serializedForwardingTransport)
	connA, err := transport.Connect(context.Background())
	require.NoError(t, err)

	idA, err := jsonrpc.MakeID("a")
	require.NoError(t, err)
	result, err := connA.Write(context.Background(), nil, &jsonrpc.Request{ID: idA, Method: "initialize"})
	require.NoError(t, err)
	require.NotNil(t, result.PreservedError)
	requireLifecycleLockReleased(t, serializedTransport)

	connB, err := transport.Connect(context.Background())
	require.NoError(t, err)
	idB, err := jsonrpc.MakeID("b")
	require.NoError(t, err)
	_, err = connB.Write(context.Background(), nil, &jsonrpc.Request{ID: idB, Method: "ping"})
	require.NoError(t, err)
	require.NoError(t, connB.Close())
}

func TestSerializedForwardingTransportRetiresDeadlineWithoutClosingAndDropsLateResponse(t *testing.T) {
	t.Parallel()

	baseConn := newStubSerializedForwardingConnection()
	transport := NewSerializedForwardingTransportWithDeadlineRetirement(&stubSerializedForwardingTransport{
		conn: baseConn,
	})
	require.NotNil(t, transport)
	serializedTransport := transport.(*serializedForwardingTransport)

	connA, err := transport.Connect(context.Background())
	require.NoError(t, err)
	idA, err := jsonrpc.MakeID("retired-a")
	require.NoError(t, err)
	_, err = connA.Write(context.Background(), nil, &jsonrpc.Request{ID: idA, Method: "tools/call"})
	require.NoError(t, err)
	requireLifecycleLockHeld(t, serializedTransport)

	retiring, ok := connA.(ResponseDeadlineRetiringConnection)
	require.True(t, ok, "deadline-retiring transport connection must expose retirement")
	require.True(t, retiring.RetireResponseDeadline())
	require.True(t, retiring.RetireResponseDeadline())
	requireLifecycleLockReleased(t, serializedTransport)

	require.NoError(t, connA.Close())
	require.NoError(t, connA.Close())
	require.Zero(t, baseConn.closeCalls.Load(), "retired logical connection must not close the shared physical connection")

	connB, err := transport.Connect(context.Background())
	require.NoError(t, err)
	idB, err := jsonrpc.MakeID("active-b")
	require.NoError(t, err)
	_, err = connB.Write(context.Background(), nil, &jsonrpc.Request{ID: idB, Method: "tools/call"})
	require.NoError(t, err)

	ambiguousServerRequestID, err := jsonrpc.MakeID("late-server-request")
	require.NoError(t, err)
	ambiguousServerRequest := &jsonrpc.Request{
		ID:     ambiguousServerRequestID,
		Method: "sampling/createMessage",
	}
	ambiguousNotification := &jsonrpc.Request{Method: "notifications/progress"}
	lateResponseA := &jsonrpc.Response{ID: idA}
	responseB := &jsonrpc.Response{ID: idB}
	baseConn.enqueueRead(ambiguousServerRequest, nil)
	baseConn.enqueueRead(ambiguousNotification, nil)
	baseConn.enqueueRead(lateResponseA, nil)
	baseConn.enqueueRead(responseB, nil)

	msg, err := connB.Read(context.Background())
	require.NoError(t, err)
	require.Same(t, responseB, msg, "late retired response must not leak into the next logical request")
	require.False(t, serializedTransport.hasRetiredResponseIDs(), "late response should clear the retired ID")
	requireLifecycleLockReleased(t, serializedTransport)
	require.Zero(t, baseConn.closeCalls.Load(), "retirement and late-response filtering must not close the shared physical connection")
}

func TestSerializedForwardingTransportRetiresCanceledRead(t *testing.T) {
	t.Parallel()

	baseConn := newStubSerializedForwardingConnection()
	transport := NewSerializedForwardingTransportWithDeadlineRetirement(&stubSerializedForwardingTransport{
		conn: baseConn,
	})
	require.NotNil(t, transport)
	serializedTransport := transport.(*serializedForwardingTransport)

	conn, err := transport.Connect(context.Background())
	require.NoError(t, err)
	id, err := jsonrpc.MakeID("canceled-read")
	require.NoError(t, err)
	_, err = conn.Write(context.Background(), nil, &jsonrpc.Request{ID: id, Method: "tools/call"})
	require.NoError(t, err)

	readCtx, cancel := context.WithCancel(context.Background())
	cancel()
	readCtx = ContextWithResponseDeadlineEnforcement(readCtx)
	baseConn.enqueueRead(nil, context.Canceled)
	msg, err := conn.Read(readCtx)
	require.Nil(t, msg)
	require.ErrorIs(t, err, context.Canceled)
	requireLifecycleLockHeld(t, serializedTransport)

	retiring, ok := conn.(ResponseDeadlineRetiringConnection)
	require.True(t, ok)
	require.True(t, retiring.RetireResponseDeadline())
	requireLifecycleLockReleased(t, serializedTransport)
	require.True(t, serializedTransport.isRetiredResponseID(id))

	require.NoError(t, conn.Close())
	require.Zero(t, baseConn.closeCalls.Load(), "canceled read must retire without closing the shared physical connection")
}

func TestSerializedForwardingTransportUnmarkedCanceledReadKeepsNormalReleasePath(t *testing.T) {
	t.Parallel()

	baseConn := newStubSerializedForwardingConnection()
	transport := NewSerializedForwardingTransportWithDeadlineRetirement(&stubSerializedForwardingTransport{
		conn: baseConn,
	})
	serializedTransport := transport.(*serializedForwardingTransport)

	conn, err := transport.Connect(context.Background())
	require.NoError(t, err)
	id, err := jsonrpc.MakeID("unmarked-canceled-read")
	require.NoError(t, err)
	_, err = conn.Write(context.Background(), nil, &jsonrpc.Request{ID: id, Method: "tools/call"})
	require.NoError(t, err)

	readCtx, cancel := context.WithCancel(context.Background())
	cancel()
	baseConn.enqueueRead(nil, context.Canceled)
	_, err = conn.Read(readCtx)
	require.ErrorIs(t, err, context.Canceled)
	requireLifecycleLockReleased(t, serializedTransport)
	require.False(t, serializedTransport.hasRetiredResponseIDs())
}

func TestSerializedForwardingTransportLetsNextResponseThroughWhenRetiredRequestNeverResponds(t *testing.T) {
	t.Parallel()

	baseConn := newStubSerializedForwardingConnection()
	transport := NewSerializedForwardingTransportWithDeadlineRetirement(&stubSerializedForwardingTransport{
		conn: baseConn,
	})
	require.NotNil(t, transport)
	serializedTransport := transport.(*serializedForwardingTransport)

	connA, err := transport.Connect(context.Background())
	require.NoError(t, err)
	idA, err := jsonrpc.MakeID("never-responds")
	require.NoError(t, err)
	_, err = connA.Write(context.Background(), nil, &jsonrpc.Request{ID: idA, Method: "tools/call"})
	require.NoError(t, err)
	retiring, ok := connA.(ResponseDeadlineRetiringConnection)
	require.True(t, ok)
	require.True(t, retiring.RetireResponseDeadline())
	requireLifecycleLockReleased(t, serializedTransport)

	connB, err := transport.Connect(context.Background())
	require.NoError(t, err)
	idB, err := jsonrpc.MakeID("next-response")
	require.NoError(t, err)
	_, err = connB.Write(context.Background(), nil, &jsonrpc.Request{ID: idB, Method: "tools/list"})
	require.NoError(t, err)
	responseB := &jsonrpc.Response{ID: idB}
	baseConn.enqueueRead(responseB, nil)

	msg, err := connB.Read(context.Background())
	require.NoError(t, err)
	require.Same(t, responseB, msg)
	require.True(t, serializedTransport.isRetiredResponseID(idA), "missing late response stays tombstoned")
	requireLifecycleLockReleased(t, serializedTransport)
	require.Zero(t, baseConn.closeCalls.Load())
}

func TestSerializedForwardingTransportDropsLateResponseAfterNewerResponseCompletes(t *testing.T) {
	t.Parallel()

	baseConn := newStubSerializedForwardingConnection()
	transport := NewSerializedForwardingTransportWithDeadlineRetirement(&stubSerializedForwardingTransport{
		conn: baseConn,
	})
	serializedTransport := transport.(*serializedForwardingTransport)

	connA, err := transport.Connect(context.Background())
	require.NoError(t, err)
	idA, err := jsonrpc.MakeID("retired-a")
	require.NoError(t, err)
	_, err = connA.Write(context.Background(), nil, &jsonrpc.Request{ID: idA, Method: "tools/call"})
	require.NoError(t, err)
	retiring := connA.(ResponseDeadlineRetiringConnection)
	require.True(t, retiring.RetireResponseDeadline())

	connB, err := transport.Connect(context.Background())
	require.NoError(t, err)
	idB, err := jsonrpc.MakeID("active-b")
	require.NoError(t, err)
	_, err = connB.Write(context.Background(), nil, &jsonrpc.Request{ID: idB, Method: "tools/list"})
	require.NoError(t, err)
	responseB := &jsonrpc.Response{ID: idB}
	baseConn.enqueueRead(responseB, nil)
	msg, err := connB.Read(context.Background())
	require.NoError(t, err)
	require.Same(t, responseB, msg)
	require.True(t, serializedTransport.isRetiredResponseID(idA))

	connC, err := transport.Connect(context.Background())
	require.NoError(t, err)
	idC, err := jsonrpc.MakeID("active-c")
	require.NoError(t, err)
	_, err = connC.Write(context.Background(), nil, &jsonrpc.Request{ID: idC, Method: "tools/list"})
	require.NoError(t, err)
	responseC := &jsonrpc.Response{ID: idC}
	baseConn.enqueueRead(&jsonrpc.Response{ID: idA}, nil)
	baseConn.enqueueRead(responseC, nil)
	msg, err = connC.Read(context.Background())
	require.NoError(t, err)
	require.Same(t, responseC, msg)
	require.False(t, serializedTransport.hasRetiredResponseIDs())
	requireLifecycleLockReleased(t, serializedTransport)
}

func TestSerializedForwardingTransportPreservesStdioPipesAfterCanceledRead(t *testing.T) {
	t.Parallel()

	clientReader, serverWriter := io.Pipe()
	serverReader, clientWriter := io.Pipe()
	t.Cleanup(func() {
		_ = clientReader.Close()
		_ = serverWriter.Close()
		_ = serverReader.Close()
		_ = clientWriter.Close()
	})

	serverReadErrs := make(chan error, 2)
	go func() {
		reader := bufio.NewReader(serverReader)
		for i := 0; i < 2; i++ {
			_, err := reader.ReadBytes('\n')
			serverReadErrs <- err
			if err != nil {
				return
			}
		}
	}()

	var writeFailed atomic.Bool
	ioTransport := &mcp.IOTransport{
		Reader: clientReader,
		Writer: &stdioErrWriter{
			writer: clientWriter,
			onError: func(error) {
				writeFailed.Store(true)
			},
		},
	}
	shared := newContextCancellationPreservingSharedConnectionTransport(ioTransport)
	transport := NewSerializedForwardingTransportWithDeadlineRetirement(NewForwardingTransport(shared))

	connA, err := transport.Connect(context.Background())
	require.NoError(t, err)
	idA, err := jsonrpc.MakeID("retired-a")
	require.NoError(t, err)
	_, err = connA.Write(context.Background(), nil, &jsonrpc.Request{ID: idA, Method: "tools/call"})
	require.NoError(t, err)
	require.NoError(t, waitForSerializedError(t, serverReadErrs, "server to read request A"))

	readCtx, cancelRead := context.WithCancel(context.Background())
	cancelRead()
	readCtx = ContextWithResponseDeadlineEnforcement(readCtx)
	_, err = connA.Read(readCtx)
	require.ErrorIs(t, err, context.Canceled)
	retiring, ok := connA.(ResponseDeadlineRetiringConnection)
	require.True(t, ok)
	require.True(t, retiring.RetireResponseDeadline())
	require.NoError(t, connA.Close())

	connB, err := transport.Connect(context.Background())
	require.NoError(t, err)
	idB, err := jsonrpc.MakeID("active-b")
	require.NoError(t, err)
	_, err = connB.Write(context.Background(), nil, &jsonrpc.Request{ID: idB, Method: "tools/call"})
	require.NoError(t, err)
	require.NoError(t, waitForSerializedError(t, serverReadErrs, "server to read request B"))
	require.False(t, writeFailed.Load(), "request B must not write into a pipe closed by request A")

	responseWriteErr := make(chan error, 1)
	go func() {
		_, err := io.WriteString(serverWriter, `{"jsonrpc":"2.0","id":"retired-a","result":{}}
{"jsonrpc":"2.0","id":"active-b","result":{}}
`)
		responseWriteErr <- err
	}()

	msg, err := connB.Read(context.Background())
	require.NoError(t, err)
	response, ok := msg.(*jsonrpc.Response)
	require.True(t, ok)
	require.Equal(t, idB, response.ID)
	require.NoError(t, waitForSerializedError(t, responseWriteErr, "server to write late and active responses"))
	require.False(t, writeFailed.Load(), "retiring request A must not trigger the stdio write-error callback")
}

func TestSerializedForwardingTransportRejectsRetiredResponseIDReuse(t *testing.T) {
	t.Parallel()

	baseConn := newStubSerializedForwardingConnection()
	transport := NewSerializedForwardingTransportWithDeadlineRetirement(&stubSerializedForwardingTransport{
		conn: baseConn,
	})
	require.NotNil(t, transport)
	serializedTransport := transport.(*serializedForwardingTransport)

	connA, err := transport.Connect(context.Background())
	require.NoError(t, err)
	id, err := jsonrpc.MakeID("reused")
	require.NoError(t, err)
	_, err = connA.Write(context.Background(), nil, &jsonrpc.Request{ID: id, Method: "tools/call"})
	require.NoError(t, err)

	retiring, ok := connA.(ResponseDeadlineRetiringConnection)
	require.True(t, ok, "deadline-retiring transport connection must expose retirement")
	require.True(t, retiring.RetireResponseDeadline())
	requireLifecycleLockReleased(t, serializedTransport)

	connB, err := transport.Connect(context.Background())
	require.NoError(t, err)
	_, err = connB.Write(context.Background(), nil, &jsonrpc.Request{ID: id, Method: "tools/call"})
	require.Error(t, err)
	require.Contains(t, err.Error(), "is still retired")
	require.EqualValues(t, 1, baseConn.writeCalls.Load(), "reused ID must be rejected before it reaches the shared physical connection")
	require.True(t, serializedTransport.isRetiredResponseID(id), "failed reuse must keep the old ID retired")
	requireLifecycleLockReleased(t, serializedTransport)
	require.Zero(t, baseConn.closeCalls.Load())
}

func TestSerializedForwardingTransportRetiresDeadlineBeforeWriteWithoutTombstone(t *testing.T) {
	t.Parallel()

	baseConn := newStubSerializedForwardingConnection()
	transport := NewSerializedForwardingTransportWithDeadlineRetirement(&stubSerializedForwardingTransport{
		conn: baseConn,
	})
	require.NotNil(t, transport)
	serializedTransport := transport.(*serializedForwardingTransport)

	conn, err := transport.Connect(context.Background())
	require.NoError(t, err)
	retiring, ok := conn.(ResponseDeadlineRetiringConnection)
	require.True(t, ok)
	require.True(t, retiring.RetireResponseDeadline())
	requireLifecycleLockReleased(t, serializedTransport)
	require.False(t, serializedTransport.hasRetiredResponseIDs())
	require.NoError(t, conn.Close())
	require.Zero(t, baseConn.closeCalls.Load())

	next, err := transport.Connect(context.Background())
	require.NoError(t, err)
	require.NoError(t, next.Close())
}

func TestSerializedForwardingTransportRetiresDeadlineAfterNotificationWrite(t *testing.T) {
	t.Parallel()

	baseConn := newStubSerializedForwardingConnection()
	transport := NewSerializedForwardingTransportWithDeadlineRetirement(&stubSerializedForwardingTransport{
		conn: baseConn,
	})
	serializedTransport := transport.(*serializedForwardingTransport)

	conn, err := transport.Connect(context.Background())
	require.NoError(t, err)
	_, err = conn.Write(context.Background(), nil, &jsonrpc.Request{Method: "notifications/initialized"})
	require.NoError(t, err)
	requireLifecycleLockReleased(t, serializedTransport)

	retiring := conn.(ResponseDeadlineRetiringConnection)
	require.True(t, retiring.RetireResponseDeadline())
	require.False(t, serializedTransport.hasRetiredResponseIDs())
	require.NoError(t, conn.Close())
	require.Zero(t, baseConn.closeCalls.Load(), "deadline after notification write must not close shared stdio")

	next, err := transport.Connect(context.Background())
	require.NoError(t, err)
	require.NoError(t, next.Close())
}

func TestSerializedForwardingTransportDoesNotRetireInitialize(t *testing.T) {
	t.Parallel()

	baseConn := newStubSerializedForwardingConnection()
	transport := NewSerializedForwardingTransportWithDeadlineRetirement(&stubSerializedForwardingTransport{
		conn: baseConn,
	})
	require.NotNil(t, transport)
	serializedTransport := transport.(*serializedForwardingTransport)

	conn, err := transport.Connect(context.Background())
	require.NoError(t, err)
	id, err := jsonrpc.MakeID("initialize")
	require.NoError(t, err)
	_, err = conn.Write(context.Background(), nil, &jsonrpc.Request{ID: id, Method: "initialize"})
	require.NoError(t, err)

	retiring, ok := conn.(ResponseDeadlineRetiringConnection)
	require.True(t, ok)
	require.False(t, retiring.RetireResponseDeadline())
	requireLifecycleLockHeld(t, serializedTransport)
	require.False(t, serializedTransport.hasRetiredResponseIDs())

	require.NoError(t, conn.Close())
	require.EqualValues(t, 1, baseConn.closeCalls.Load())
}

func TestSerializedForwardingTransportRetiresInitializeBeforeWriteCompletes(t *testing.T) {
	t.Parallel()

	baseConn := newStubSerializedForwardingConnection()
	baseConn.enqueueWriteResult(0, nil, context.Canceled)
	transport := NewSerializedForwardingTransportWithDeadlineRetirement(&stubSerializedForwardingTransport{
		conn: baseConn,
	})
	serializedTransport := transport.(*serializedForwardingTransport)

	conn, err := transport.Connect(context.Background())
	require.NoError(t, err)
	id, err := jsonrpc.MakeID("initialize-not-written")
	require.NoError(t, err)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	ctx = ContextWithResponseDeadlineEnforcement(ctx)
	_, err = conn.Write(ctx, nil, &jsonrpc.Request{ID: id, Method: "initialize"})
	require.ErrorIs(t, err, context.Canceled)
	requireLifecycleLockHeld(t, serializedTransport)

	retiring := conn.(ResponseDeadlineRetiringConnection)
	require.True(t, retiring.RetireResponseDeadline())
	requireLifecycleLockReleased(t, serializedTransport)
	require.False(t, serializedTransport.hasRetiredResponseIDs())
	require.NoError(t, conn.Close())
	require.Zero(t, baseConn.closeCalls.Load(), "initialize that never wrote bytes must not close shared stdio")
}

func TestSerializedForwardingTransportFailsClosedWhenRetiredResponseLimitReached(t *testing.T) {
	t.Parallel()

	baseConn := newStubSerializedForwardingConnection()
	transport := NewSerializedForwardingTransportWithDeadlineRetirement(&stubSerializedForwardingTransport{
		conn: baseConn,
	})
	serializedTransport := transport.(*serializedForwardingTransport)
	for i := 0; i < maxRetiredResponseIDs; i++ {
		id, err := jsonrpc.MakeID(float64(i))
		require.NoError(t, err)
		require.True(t, serializedTransport.retireResponseID(id))
	}

	conn, err := transport.Connect(context.Background())
	require.NoError(t, err)
	id, err := jsonrpc.MakeID("overflow")
	require.NoError(t, err)
	_, err = conn.Write(context.Background(), nil, &jsonrpc.Request{ID: id, Method: "tools/call"})
	require.NoError(t, err)

	retiring := conn.(ResponseDeadlineRetiringConnection)
	require.False(t, retiring.RetireResponseDeadline())
	requireLifecycleLockHeld(t, serializedTransport)
	require.False(t, serializedTransport.isRetiredResponseID(id))

	require.NoError(t, conn.Close())
	require.EqualValues(t, 1, baseConn.closeCalls.Load())
}

func requireLifecycleLockHeld(t *testing.T, transport *serializedForwardingTransport) {
	t.Helper()
	require.Equal(t, 1, len(transport.lifecycleSlot), "lifecycle slot was released")
}

func requireLifecycleLockReleased(t *testing.T, transport *serializedForwardingTransport) {
	t.Helper()
	require.Empty(t, transport.lifecycleSlot, "lifecycle slot was still held")
}

type stubSerializedForwardingTransport struct {
	conn         ForwardingConnection
	connectCalls atomic.Int32
}

func (s *stubSerializedForwardingTransport) Connect(context.Context) (ForwardingConnection, error) {
	s.connectCalls.Add(1)
	return s.conn, nil
}

type failOnceSerializedForwardingTransport struct {
	conn         ForwardingConnection
	err          error
	connectCalls atomic.Int32
}

type blockingFirstSerializedForwardingTransport struct {
	firstConn    ForwardingConnection
	secondConn   ForwardingConnection
	started      chan struct{}
	release      chan struct{}
	connectCalls atomic.Int32
}

func (s *blockingFirstSerializedForwardingTransport) Connect(context.Context) (ForwardingConnection, error) {
	if s.connectCalls.Add(1) == 1 {
		close(s.started)
		<-s.release
		return s.firstConn, nil
	}
	return s.secondConn, nil
}

func (s *failOnceSerializedForwardingTransport) Connect(context.Context) (ForwardingConnection, error) {
	if s.connectCalls.Add(1) == 1 {
		return nil, s.err
	}
	return s.conn, nil
}

type doneObservedContext struct {
	context.Context
	observed chan struct{}
	once     sync.Once
}

func (c *doneObservedContext) Done() <-chan struct{} {
	c.once.Do(func() { close(c.observed) })
	return c.Context.Done()
}

type serializedConnectResult struct {
	conn ForwardingConnection
	err  error
}

func waitForSerializedSignal(t *testing.T, signal <-chan struct{}, description string) {
	t.Helper()
	select {
	case <-signal:
	case <-time.After(2 * time.Second):
		t.Fatalf("timed out waiting for %s", description)
	}
}

func waitForSerializedConnectResult(t *testing.T, result <-chan serializedConnectResult) serializedConnectResult {
	t.Helper()
	select {
	case got := <-result:
		return got
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for serialized Connect result")
		return serializedConnectResult{}
	}
}

func waitForSerializedError(t *testing.T, result <-chan error, description string) error {
	t.Helper()
	select {
	case err := <-result:
		return err
	case <-time.After(2 * time.Second):
		t.Fatalf("timed out waiting for %s", description)
		return nil
	}
}

type stubSerializedForwardingConnection struct {
	writeResults chan stubWriteResult
	readResults  chan stubReadResult
	writeCalls   atomic.Int32
	closeCalls   atomic.Int32
}

type stubWriteResult struct {
	statusCode     int
	headers        http.Header
	preservedError *PreservedMCPError
	err            error
}

type stubReadResult struct {
	msg jsonrpc.Message
	err error
}

func newStubSerializedForwardingConnection() *stubSerializedForwardingConnection {
	return &stubSerializedForwardingConnection{
		writeResults: make(chan stubWriteResult, 8),
		readResults:  make(chan stubReadResult, 8),
	}
}

func (s *stubSerializedForwardingConnection) enqueueWriteResult(
	statusCode int,
	headers http.Header,
	err error,
) {
	s.writeResults <- stubWriteResult{
		statusCode: statusCode,
		headers:    headers,
		err:        err,
	}
}

func (s *stubSerializedForwardingConnection) enqueueRead(msg jsonrpc.Message, err error) {
	s.readResults <- stubReadResult{msg: msg, err: err}
}

func (s *stubSerializedForwardingConnection) Write(
	_ context.Context,
	_ http.Header,
	_ jsonrpc.Message,
) (ForwardingWriteResult, error) {
	s.writeCalls.Add(1)
	select {
	case result := <-s.writeResults:
		return ForwardingWriteResult{
			StatusCode:      result.statusCode,
			ResponseHeaders: result.headers,
			PreservedError:  result.preservedError,
		}, result.err
	default:
		return ForwardingWriteResult{StatusCode: http.StatusOK}, nil
	}
}

func (s *stubSerializedForwardingConnection) Read(context.Context) (jsonrpc.Message, error) {
	result := <-s.readResults
	return result.msg, result.err
}

func (s *stubSerializedForwardingConnection) Close() error {
	s.closeCalls.Add(1)
	return nil
}
