package mcpclient

import (
	"context"
	"errors"
	"net/http"
	"testing"

	"github.com/google/go-cmp/cmp"
	"github.com/google/go-cmp/cmp/cmpopts"
	"github.com/modelcontextprotocol/go-sdk/jsonrpc"
	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/openai/tunnel-client/pkg/mcpclient/internal"
)

func TestForwardingConnectionPropagatesHeaders(t *testing.T) {
	respHeaders := http.Header{"X-Response": {"ok"}, "Another": {"value"}}
	const wantStatus = http.StatusAccepted
	sortStrings := cmpopts.SortSlices(func(a, b string) bool { return a < b })

	callID := mustMakeID(t, "call-1")

	fake := &fakeConnection{
		writeFunc: func(ctx context.Context, msg jsonrpc.Message) error {
			carrier := internal.CarrierFromContext(ctx)
			if carrier == nil {
				t.Fatalf("carrier missing in context")
			}
			carrier.StoreResponse(wantStatus, respHeaders)
			return nil
		},
		readFunc: func(ctx context.Context) (jsonrpc.Message, error) {
			return &jsonrpc.Response{
				ID: callID,
			}, nil
		},
	}

	conn := &forwardingConnection{
		base: fake,
	}

	req := &jsonrpc.Request{
		ID:     callID,
		Method: "testMethod",
	}

	requestHeaders := http.Header{"X-Forward": {"value"}}

	statusCode, gotWriteHeaders, err := conn.Write(context.Background(), requestHeaders, req)
	if err != nil {
		t.Fatalf("Write returned error: %v", err)
	}
	if statusCode != wantStatus {
		t.Fatalf("unexpected status code: got %d, want %d", statusCode, wantStatus)
	}
	if diff := cmp.Diff(respHeaders, gotWriteHeaders, sortStrings); diff != "" {
		t.Fatalf("write headers mismatch (-want +got):\n%s", diff)
	}

	msg, err := conn.Read(context.Background())
	if err != nil {
		t.Fatalf("Read returned error: %v", err)
	}
	if _, ok := msg.(*jsonrpc.Response); !ok {
		t.Fatalf("expected jsonrpc.Response, got %T", msg)
	}

	if fake.lastForwardedHeader == nil {
		t.Fatalf("request headers were not forwarded to fake connection")
	}
	if diff := cmp.Diff(requestHeaders, fake.lastForwardedHeader, sortStrings); diff != "" {
		t.Fatalf("request headers mismatch (-want +got):\n%s", diff)
	}
	if fake.closeCalls != 0 {
		t.Fatalf("unexpected close calls on successful write/read path: got %d", fake.closeCalls)
	}
}

func TestForwardingConnectionWriteErrorClosesBase(t *testing.T) {
	t.Parallel()

	wantErr := errors.New("write failed")
	fake := &fakeConnection{
		writeFunc: func(context.Context, jsonrpc.Message) error {
			return wantErr
		},
	}

	conn := &forwardingConnection{base: fake}
	req := &jsonrpc.Request{ID: mustMakeID(t, "call-write-error"), Method: "testMethod"}

	status, headers, err := conn.Write(context.Background(), nil, req)
	if !errors.Is(err, wantErr) {
		t.Fatalf("Write returned error %v, want %v", err, wantErr)
	}
	if status != 0 {
		t.Fatalf("unexpected status code: got %d want 0", status)
	}
	if headers != nil {
		t.Fatalf("expected nil headers, got %v", headers)
	}
	if fake.closeCalls != 1 {
		t.Fatalf("expected Close to be called once, got %d", fake.closeCalls)
	}
}

func TestForwardingConnectionReadErrorClosesBase(t *testing.T) {
	t.Parallel()

	wantErr := errors.New("read failed")
	fake := &fakeConnection{
		readFunc: func(context.Context) (jsonrpc.Message, error) {
			return nil, wantErr
		},
	}

	conn := &forwardingConnection{base: fake}

	msg, err := conn.Read(context.Background())
	if !errors.Is(err, wantErr) {
		t.Fatalf("Read returned error %v, want %v", err, wantErr)
	}
	if msg != nil {
		t.Fatalf("expected nil message, got %T", msg)
	}
	if fake.closeCalls != 1 {
		t.Fatalf("expected Close to be called once, got %d", fake.closeCalls)
	}
}

func TestForwardingTransportConnectNilBaseReturnsNil(t *testing.T) {
	t.Parallel()

	transport := &forwardingTransport{}
	conn, err := transport.Connect(context.Background())
	if err != nil {
		t.Fatalf("Connect returned error: %v", err)
	}
	if conn != nil {
		t.Fatalf("expected nil connection, got %T", conn)
	}
}

func TestForwardingTransportConnectPropagatesBaseError(t *testing.T) {
	t.Parallel()

	transport := &forwardingTransport{base: &failingTransport{err: errors.New("connect failed")}}
	conn, err := transport.Connect(context.Background())
	if err == nil {
		t.Fatalf("expected error, got nil")
	}
	if conn != nil {
		t.Fatalf("expected nil connection, got %T", conn)
	}
}

func TestForwardingTransportTerminateSessionForwardsHeadersAndCapturesResponse(t *testing.T) {
	t.Parallel()

	wantHeaders := http.Header{"X-Response": {"ok"}}
	wantRequestHeaders := http.Header{"Mcp-Session-Id": {"session-123"}}
	transport := &forwardingTransport{
		base: contextCapturingTransport{
			connect: func(ctx context.Context) (mcp.Connection, error) {
				return closeFuncConnection{
					closeFunc: func() error {
						carrier := internal.CarrierFromContext(ctx)
						if carrier == nil {
							t.Fatal("carrier missing in session termination context")
						}
						if diff := cmp.Diff(wantRequestHeaders, carrier.RequestHeaders(), cmpopts.SortSlices(func(a, b string) bool { return a < b })); diff != "" {
							t.Fatalf("session termination request headers mismatch (-want +got):\n%s", diff)
						}
						carrier.StoreResponse(http.StatusNoContent, wantHeaders)
						return nil
					},
				}, nil
			},
		},
	}

	statusCode, gotHeaders, err := transport.TerminateSession(context.Background(), wantRequestHeaders)
	if err != nil {
		t.Fatalf("TerminateSession returned error: %v", err)
	}
	if statusCode != http.StatusNoContent {
		t.Fatalf("unexpected status code: got %d want %d", statusCode, http.StatusNoContent)
	}
	if diff := cmp.Diff(wantHeaders, gotHeaders, cmpopts.SortSlices(func(a, b string) bool { return a < b })); diff != "" {
		t.Fatalf("session termination response headers mismatch (-want +got):\n%s", diff)
	}
}

func TestForwardingConnectionCloseDelegates(t *testing.T) {
	t.Parallel()

	fake := &closeTrackingConnection{}
	conn := &forwardingConnection{base: fake}
	if err := conn.Close(); err != nil {
		t.Fatalf("Close returned error: %v", err)
	}
	if !fake.closed {
		t.Fatalf("expected base connection Close to be called")
	}
}

func TestForwardingConnectionCloseNilBaseReturnsNil(t *testing.T) {
	t.Parallel()

	conn := &forwardingConnection{base: nil}
	if err := conn.Close(); err != nil {
		t.Fatalf("Close returned error: %v", err)
	}
}

func TestForwardingConnectionWriteNilBaseReturnsZeroes(t *testing.T) {
	t.Parallel()

	callID := mustMakeID(t, "call-nil-base")
	req := &jsonrpc.Request{ID: callID, Method: "noop"}

	conn := &forwardingConnection{base: nil}
	status, headers, err := conn.Write(context.Background(), http.Header{"X-Test": {"true"}}, req)
	if err != nil {
		t.Fatalf("Write returned error: %v", err)
	}
	if status != 0 {
		t.Fatalf("unexpected status code: got %d want 0", status)
	}
	if headers != nil {
		t.Fatalf("expected nil headers, got %v", headers)
	}
}

func TestForwardingConnectionReadNilBaseReturnsNils(t *testing.T) {
	t.Parallel()

	conn := &forwardingConnection{base: nil}
	msg, err := conn.Read(context.Background())
	if err != nil {
		t.Fatalf("Read returned error: %v", err)
	}
	if msg != nil {
		t.Fatalf("expected nil message, got %T", msg)
	}
}

func TestForwardingConnectionWriteNilContextReturnsError(t *testing.T) {
	t.Parallel()

	callID := mustMakeID(t, "call-nil-ctx")
	req := &jsonrpc.Request{ID: callID, Method: "noop"}

	conn := &forwardingConnection{base: &fakeConnection{}}
	//lint:ignore SA1012 exercising nil-context guard in ContextWithHeaders
	_, _, err := conn.Write(nil, nil, req)
	if err == nil {
		t.Fatalf("expected error, got nil")
	}
}

type fakeConnection struct {
	writeFunc           func(context.Context, jsonrpc.Message) error
	readFunc            func(context.Context) (jsonrpc.Message, error)
	lastForwardedHeader http.Header
	closeCalls          int
}

func (f *fakeConnection) Read(ctx context.Context) (jsonrpc.Message, error) {
	if f.readFunc != nil {
		return f.readFunc(ctx)
	}
	return nil, nil
}

func (f *fakeConnection) Write(ctx context.Context, msg jsonrpc.Message) error {
	if carrier := internal.CarrierFromContext(ctx); carrier != nil {
		f.lastForwardedHeader = carrier.RequestHeaders()
	}
	if f.writeFunc == nil {
		return nil
	}
	return f.writeFunc(ctx, msg)
}

func (f *fakeConnection) Close() error {
	f.closeCalls++
	return nil
}

func (f *fakeConnection) SessionID() string { return "" }

func mustMakeID(tb testing.TB, v any) jsonrpc.ID {
	tb.Helper()
	id, err := jsonrpc.MakeID(v)
	if err != nil {
		tb.Fatalf("jsonrpc.MakeID(%v): %v", v, err)
	}
	return id
}

type failingTransport struct {
	err error
}

func (t *failingTransport) Connect(context.Context) (mcp.Connection, error) {
	return nil, t.err
}

type contextCapturingTransport struct {
	connect func(context.Context) (mcp.Connection, error)
}

func (t contextCapturingTransport) Connect(ctx context.Context) (mcp.Connection, error) {
	return t.connect(ctx)
}

type closeFuncConnection struct {
	closeFunc func() error
}

func (c closeFuncConnection) Read(context.Context) (jsonrpc.Message, error) { return nil, nil }
func (c closeFuncConnection) Write(context.Context, jsonrpc.Message) error  { return nil }
func (c closeFuncConnection) Close() error {
	if c.closeFunc == nil {
		return nil
	}
	return c.closeFunc()
}
func (c closeFuncConnection) SessionID() string { return "" }

type closeTrackingConnection struct {
	closed bool
}

func (c *closeTrackingConnection) Read(context.Context) (jsonrpc.Message, error) { return nil, nil }
func (c *closeTrackingConnection) Write(context.Context, jsonrpc.Message) error  { return nil }
func (c *closeTrackingConnection) Close() error                                  { c.closed = true; return nil }
func (c *closeTrackingConnection) SessionID() string                             { return "" }
