package mcpclient

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/google/go-cmp/cmp"
	"github.com/google/go-cmp/cmp/cmpopts"
	"github.com/modelcontextprotocol/go-sdk/jsonrpc"
	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/stretchr/testify/require"

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

	result, err := conn.Write(context.Background(), requestHeaders, req)
	if err != nil {
		t.Fatalf("Write returned error: %v", err)
	}
	if result.StatusCode != wantStatus {
		t.Fatalf("unexpected status code: got %d, want %d", result.StatusCode, wantStatus)
	}
	if diff := cmp.Diff(respHeaders, result.ResponseHeaders, sortStrings); diff != "" {
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

	result, err := conn.Write(context.Background(), nil, req)
	if !errors.Is(err, wantErr) {
		t.Fatalf("Write returned error %v, want %v", err, wantErr)
	}
	if result.StatusCode != 0 {
		t.Fatalf("unexpected status code: got %d want 0", result.StatusCode)
	}
	if result.ResponseHeaders != nil {
		t.Fatalf("expected nil headers, got %v", result.ResponseHeaders)
	}
	if fake.closeCalls != 1 {
		t.Fatalf("expected Close to be called once, got %d", fake.closeCalls)
	}
}

func TestForwardingConnectionPreservesRecognizedNonSuccessMCPError(t *testing.T) {
	t.Parallel()

	testCases := []struct {
		name       string
		statusCode int
		code       int64
		payload    string
	}{
		{
			name:       "capability_error",
			statusCode: http.StatusBadRequest,
			code:       -32003,
			payload:    `{"jsonrpc":"2.0","id":"capability","error":{"code":-32003,"message":"capability unavailable","data":{"capability":"sampling","required":true}},"future":"preserved"}`,
		},
		{
			name:       "version_error",
			statusCode: http.StatusNotFound,
			code:       -32004,
			payload:    `{"jsonrpc":"2.0","id":"version","error":{"code":-32004,"message":"unsupported protocol version","data":{"supported":["2025-06-18"]}}}`,
		},
		{
			name:       "other_target_error",
			statusCode: http.StatusInternalServerError,
			code:       -32042,
			payload:    `{"jsonrpc":"2.0","id":"other","error":{"code":-32042,"message":"target-owned","data":{"nested":{"safe":"opaque"}}}}`,
		},
	}

	for _, tc := range testCases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			id := mustMakeID(t, tc.name[:len(tc.name)-len("_error")])
			if tc.name == "other_target_error" {
				id = mustMakeID(t, "other")
			}
			wantHeaders := http.Header{
				"Content-Type":         {"application/json"},
				"Mcp-Protocol-Version": {"2025-06-18"},
			}
			writeErr := errors.New("SDK rejected non-success response")
			fake := &fakeConnection{
				writeFunc: func(ctx context.Context, _ jsonrpc.Message) error {
					carrier := internal.CarrierFromContext(ctx)
					require.NotNil(t, carrier)
					carrier.StoreResponse(tc.statusCode, wantHeaders)
					carrier.StoreResponseBodyCapture([]byte(tc.payload), false, nil)
					return writeErr
				},
			}

			result, err := (&forwardingConnection{base: fake}).Write(
				context.Background(),
				http.Header{"Authorization": {"Bearer redacted"}},
				&jsonrpc.Request{ID: id, Method: "initialize"},
			)
			require.NoError(t, err)
			require.Equal(t, tc.statusCode, result.StatusCode)
			require.Equal(t, wantHeaders, result.ResponseHeaders)
			require.NotNil(t, result.PreservedError)
			require.Equal(t, tc.code, result.PreservedError.Code())
			require.Equal(t, []byte(tc.payload), result.PreservedError.Payload())
			require.NotContains(t, string(result.PreservedError.Payload()), "tunnel_failure")
			require.Equal(t, 1, fake.closeCalls, "existing connection lifecycle must still close after SDK rejection")

			mutated := result.PreservedError.Payload()
			mutated[0] = 'x'
			require.Equal(t, []byte(tc.payload), result.PreservedError.Payload(), "payload accessor must be defensive")
		})
	}
}

func TestForwardingConnectionPreservesHTTPMCPErrorEndToEnd(t *testing.T) {
	t.Parallel()

	const payload = `{"jsonrpc":"2.0","id":"http-error","error":{"code":-32004,"message":"unsupported version","data":{"supported":["2025-06-18"]}}}`
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodDelete {
			w.WriteHeader(http.StatusNoContent)
			return
		}
		require.Equal(t, http.MethodPost, r.Method)
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("Mcp-Protocol-Version", "2025-06-18")
		w.Header().Set("WWW-Authenticate", `Bearer error="invalid_request"`)
		w.WriteHeader(http.StatusMethodNotAllowed)
		_, _ = io.WriteString(w, payload)
	}))
	t.Cleanup(server.Close)

	httpClient := &http.Client{Transport: internal.NewForwardingRoundTripper(http.DefaultTransport)}
	transport := NewForwardingTransport(&mcp.StreamableClientTransport{
		Endpoint:   server.URL,
		HTTPClient: httpClient,
	})
	conn, err := transport.Connect(context.Background())
	require.NoError(t, err)
	require.NotNil(t, conn)

	result, err := conn.Write(
		context.Background(),
		http.Header{"Accept": {"application/json, text/event-stream"}},
		&jsonrpc.Request{ID: mustMakeID(t, "http-error"), Method: "initialize"},
	)
	require.NoError(t, err)
	require.Equal(t, http.StatusMethodNotAllowed, result.StatusCode)
	require.Equal(t, "2025-06-18", result.ResponseHeaders.Get("Mcp-Protocol-Version"))
	require.Equal(t, `Bearer error="invalid_request"`, result.ResponseHeaders.Get("WWW-Authenticate"))
	require.NotNil(t, result.PreservedError)
	require.EqualValues(t, -32004, result.PreservedError.Code())
	require.Equal(t, payload, string(result.PreservedError.Payload()))
}

func TestForwardingConnectionReturnsTypedNonProtocolResponse(t *testing.T) {
	t.Parallel()

	readErr := errors.New("response body read failed")
	testCases := []struct {
		name        string
		body        []byte
		captured    bool
		tooLarge    bool
		readErr     error
		requestID   string
		wantKind    NonProtocolResponseKind
		wantWrapped error
	}{
		{name: "missing_capture", requestID: "request", wantKind: NonProtocolResponseBodyMissing},
		{name: "empty_body", captured: true, requestID: "request", wantKind: NonProtocolResponseBodyMissing},
		{name: "unreadable_body", body: []byte(`{`), captured: true, readErr: readErr, requestID: "request", wantKind: NonProtocolResponseBodyUnreadable, wantWrapped: readErr},
		{name: "oversized_body", body: []byte(`{"jsonrpc":"2.0"}`), captured: true, tooLarge: true, requestID: "request", wantKind: NonProtocolResponseBodyTooLarge},
		{name: "malformed_json", body: []byte(`{"jsonrpc":`), captured: true, requestID: "request", wantKind: NonProtocolResponseMalformedJSON},
		{name: "non_json", body: []byte(`bad gateway`), captured: true, requestID: "request", wantKind: NonProtocolResponseMalformedJSON},
		{name: "success_response", body: []byte(`{"jsonrpc":"2.0","id":"request","result":{"ok":true}}`), captured: true, requestID: "request", wantKind: NonProtocolResponseInvalidMCPError},
		{name: "error_and_result", body: []byte(`{"jsonrpc":"2.0","id":"request","result":null,"error":{"code":-32003,"message":"invalid"}}`), captured: true, requestID: "request", wantKind: NonProtocolResponseInvalidMCPError},
		{name: "missing_error_message", body: []byte(`{"jsonrpc":"2.0","id":"request","error":{"code":-32003}}`), captured: true, requestID: "request", wantKind: NonProtocolResponseInvalidMCPError},
		{name: "mismatched_id", body: []byte(`{"jsonrpc":"2.0","id":"different","error":{"code":-32003,"message":"invalid"}}`), captured: true, requestID: "request", wantKind: NonProtocolResponseInvalidMCPError},
		{name: "notification_has_no_response", body: []byte(`{"jsonrpc":"2.0","id":"request","error":{"code":-32003,"message":"invalid"}}`), captured: true, wantKind: NonProtocolResponseInvalidMCPError},
	}

	for _, tc := range testCases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			fake := &fakeConnection{
				writeFunc: func(ctx context.Context, _ jsonrpc.Message) error {
					carrier := internal.CarrierFromContext(ctx)
					require.NotNil(t, carrier)
					carrier.StoreResponse(http.StatusBadGateway, http.Header{"Content-Type": {"application/json"}})
					if tc.captured {
						carrier.StoreResponseBodyCapture(tc.body, tc.tooLarge, tc.readErr)
					}
					return errors.New("SDK rejected non-success response")
				},
			}
			req := &jsonrpc.Request{Method: "initialize"}
			if tc.requestID != "" {
				req.ID = mustMakeID(t, tc.requestID)
			}

			result, err := (&forwardingConnection{base: fake}).Write(context.Background(), nil, req)
			require.Error(t, err)
			require.Equal(t, http.StatusBadGateway, result.StatusCode)
			require.Nil(t, result.PreservedError)
			var responseErr *NonProtocolResponseError
			require.ErrorAs(t, err, &responseErr)
			require.Equal(t, tc.wantKind, responseErr.Kind())
			if tc.wantWrapped != nil {
				require.ErrorIs(t, err, tc.wantWrapped)
			}
			if len(tc.body) > 0 {
				require.NotContains(t, err.Error(), string(tc.body), "typed error must not include the response body")
			}
			require.Equal(t, 1, fake.closeCalls)
		})
	}
}

func TestForwardingConnectionPreservesTypedLocalTransportError(t *testing.T) {
	t.Parallel()

	fake := &fakeConnection{
		writeFunc: func(context.Context, jsonrpc.Message) error {
			return io.ErrClosedPipe
		},
	}
	result, err := (&forwardingConnection{base: fake}).Write(
		context.Background(),
		nil,
		&jsonrpc.Request{ID: mustMakeID(t, "closed-pipe"), Method: "initialize"},
	)
	require.ErrorIs(t, err, io.ErrClosedPipe)
	require.Zero(t, result.StatusCode)
	require.Nil(t, result.PreservedError)
	var responseErr *NonProtocolResponseError
	require.False(t, errors.As(err, &responseErr), "local transport errors must not be relabeled as target responses")
	require.Equal(t, 1, fake.closeCalls)
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

func TestForwardingConnectionContextCancellationPreservesConfiguredSharedBase(t *testing.T) {
	t.Parallel()

	base := &fakeConnection{
		readFunc: func(ctx context.Context) (jsonrpc.Message, error) {
			return nil, ctx.Err()
		},
	}
	connectCalls := 0
	shared := newContextCancellationPreservingSharedConnectionTransport(contextCapturingTransport{
		connect: func(context.Context) (mcp.Connection, error) {
			connectCalls++
			return base, nil
		},
	})
	transport := NewForwardingTransport(shared)

	conn, err := transport.Connect(context.Background())
	require.NoError(t, err)
	require.NotNil(t, conn)

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	ctx = ContextWithResponseDeadlineEnforcement(ctx)

	msg, err := conn.Read(ctx)
	require.Nil(t, msg)
	require.ErrorIs(t, err, context.Canceled)
	require.Zero(t, base.closeCalls, "request cancellation must not close the shared physical connection")

	next, err := transport.Connect(context.Background())
	require.NoError(t, err)
	require.NotNil(t, next)
	require.Equal(t, 1, connectCalls, "preserved shared connection must remain cached")
}

func TestForwardingConnectionWriteContextCancellationPreservesConfiguredSharedBase(t *testing.T) {
	t.Parallel()

	base := &fakeConnection{
		writeFunc: func(ctx context.Context, _ jsonrpc.Message) error {
			return ctx.Err()
		},
	}
	connectCalls := 0
	shared := newContextCancellationPreservingSharedConnectionTransport(contextCapturingTransport{
		connect: func(context.Context) (mcp.Connection, error) {
			connectCalls++
			return base, nil
		},
	})
	transport := NewForwardingTransport(shared)

	conn, err := transport.Connect(context.Background())
	require.NoError(t, err)
	require.NotNil(t, conn)

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	ctx = ContextWithResponseDeadlineEnforcement(ctx)
	_, err = conn.Write(ctx, nil, &jsonrpc.Request{ID: mustMakeID(t, "canceled-write"), Method: "tools/call"})
	require.ErrorIs(t, err, context.Canceled)
	require.Zero(t, base.closeCalls, "request cancellation must not close the shared physical connection")

	next, err := transport.Connect(context.Background())
	require.NoError(t, err)
	require.NotNil(t, next)
	require.Equal(t, 1, connectCalls, "preserved shared connection must remain cached")
}

func TestForwardingConnectionUnmarkedContextCancellationStillClosesConfiguredSharedBase(t *testing.T) {
	t.Parallel()

	base := &fakeConnection{
		readFunc: func(ctx context.Context) (jsonrpc.Message, error) {
			return nil, ctx.Err()
		},
	}
	shared := newContextCancellationPreservingSharedConnectionTransport(contextCapturingTransport{
		connect: func(context.Context) (mcp.Connection, error) {
			return base, nil
		},
	})
	transport := NewForwardingTransport(shared)

	conn, err := transport.Connect(context.Background())
	require.NoError(t, err)

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err = conn.Read(ctx)
	require.ErrorIs(t, err, context.Canceled)
	require.Equal(t, 1, base.closeCalls, "legacy unmarked cancellation keeps the normal close path")
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

func TestForwardingTransportTerminateStreamableSessionCancelsDelete(t *testing.T) {
	t.Parallel()

	blockingRoundTripper := &blockingTerminationRoundTripper{
		started:  make(chan *http.Request, 1),
		canceled: make(chan struct{}),
	}
	streamable := &mcp.StreamableClientTransport{
		Endpoint: "https://mcp.example.test/rpc",
		HTTPClient: &http.Client{
			Transport: internal.NewForwardingRoundTripper(blockingRoundTripper),
		},
	}
	transport := NewForwardingTransport(&mcp.LoggingTransport{
		Transport: streamable,
		Writer:    io.Discard,
	})
	terminator, ok := transport.(SessionTerminatingTransport)
	if !ok {
		t.Fatalf("transport %T does not support session termination", transport)
	}

	ctx, cancel := context.WithCancel(context.Background())
	ctx = ContextWithResponseDeadlineEnforcement(ctx)
	result := make(chan terminationResult, 1)
	go func() {
		statusCode, headers, err := terminator.TerminateSession(ctx, http.Header{
			"Mcp-Session-Id": {"session-deadline"},
		})
		result <- terminationResult{statusCode: statusCode, headers: headers, err: err}
	}()

	req := waitForTerminationRequest(t, blockingRoundTripper.started)
	if req.Method != http.MethodDelete {
		t.Fatalf("termination method = %s, want DELETE", req.Method)
	}
	if got := req.Header.Get("Mcp-Session-Id"); got != "session-deadline" {
		t.Fatalf("termination session header = %q, want session-deadline", got)
	}
	cancel()
	waitForTerminationCancellation(t, blockingRoundTripper.canceled)

	got := waitForTerminationResult(t, result)
	if !errors.Is(got.err, context.Canceled) {
		t.Fatalf("TerminateSession error = %v, want context.Canceled", got.err)
	}
	if got.statusCode != 0 || got.headers != nil {
		t.Fatalf("canceled termination response = (%d, %v), want (0, nil)", got.statusCode, got.headers)
	}
}

func TestForwardingTransportTerminateStreamableSessionReturnsHTTPResponse(t *testing.T) {
	t.Parallel()

	var gotRequest *http.Request
	streamable := &mcp.StreamableClientTransport{
		Endpoint: "https://mcp.example.test/rpc",
		HTTPClient: &http.Client{Transport: terminationRoundTripperFunc(func(req *http.Request) (*http.Response, error) {
			gotRequest = req
			return &http.Response{
				StatusCode: http.StatusNoContent,
				Header:     http.Header{"X-Terminated": {"true"}},
				Body:       http.NoBody,
				Request:    req,
			}, nil
		})},
	}
	terminator := NewForwardingTransport(streamable).(SessionTerminatingTransport)

	ctx := ContextWithResponseDeadlineEnforcement(context.Background())
	statusCode, headers, err := terminator.TerminateSession(ctx, http.Header{
		"Mcp-Session-Id": {"session-success"},
	})
	if err != nil {
		t.Fatalf("TerminateSession returned error: %v", err)
	}
	if statusCode != http.StatusNoContent || headers.Get("X-Terminated") != "true" {
		t.Fatalf("termination response = (%d, %v), want (204, X-Terminated=true)", statusCode, headers)
	}
	if gotRequest == nil || gotRequest.Method != http.MethodDelete || gotRequest.Header.Get("Mcp-Session-Id") != "session-success" {
		t.Fatalf("unexpected termination request: %#v", gotRequest)
	}
}

func TestForwardingTransportExternalStreamableSessionTerminationIssuesDelete(t *testing.T) {
	t.Parallel()

	var gotRequest *http.Request
	streamable := &mcp.StreamableClientTransport{
		Endpoint: "https://mcp.example.test/rpc",
		HTTPClient: &http.Client{Transport: terminationRoundTripperFunc(func(req *http.Request) (*http.Response, error) {
			gotRequest = req
			return &http.Response{
				StatusCode: http.StatusNoContent,
				Header:     http.Header{"X-Terminated": {"true"}},
				Body:       http.NoBody,
				Request:    req,
			}, nil
		})},
	}
	terminator := NewForwardingTransport(streamable).(SessionTerminatingTransport)

	statusCode, headers, err := terminator.TerminateSession(context.Background(), http.Header{
		"Mcp-Session-Id": {"legacy-session"},
	})
	if err != nil {
		t.Fatalf("TerminateSession returned error: %v", err)
	}
	if statusCode != http.StatusNoContent || headers.Get("X-Terminated") != "true" {
		t.Fatalf("termination response = (%d, %v), want (204, X-Terminated=true)", statusCode, headers)
	}
	if gotRequest == nil || gotRequest.Method != http.MethodDelete || gotRequest.Header.Get(HeaderSessionID) != "legacy-session" {
		t.Fatalf("unexpected termination request: %#v", gotRequest)
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
	result, err := conn.Write(context.Background(), http.Header{"X-Test": {"true"}}, req)
	if err != nil {
		t.Fatalf("Write returned error: %v", err)
	}
	if result.StatusCode != 0 {
		t.Fatalf("unexpected status code: got %d want 0", result.StatusCode)
	}
	if result.ResponseHeaders != nil {
		t.Fatalf("expected nil headers, got %v", result.ResponseHeaders)
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
	_, err := conn.Write(nil, nil, req)
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

type blockingTerminationRoundTripper struct {
	started  chan *http.Request
	canceled chan struct{}
}

type terminationRoundTripperFunc func(*http.Request) (*http.Response, error)

func (f terminationRoundTripperFunc) RoundTrip(req *http.Request) (*http.Response, error) {
	return f(req)
}

func (r *blockingTerminationRoundTripper) RoundTrip(req *http.Request) (*http.Response, error) {
	r.started <- req
	<-req.Context().Done()
	close(r.canceled)
	return nil, req.Context().Err()
}

type terminationResult struct {
	statusCode int
	headers    http.Header
	err        error
}

func waitForTerminationRequest(t *testing.T, started <-chan *http.Request) *http.Request {
	t.Helper()
	select {
	case req := <-started:
		return req
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for session termination DELETE")
		return nil
	}
}

func waitForTerminationCancellation(t *testing.T, canceled <-chan struct{}) {
	t.Helper()
	select {
	case <-canceled:
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for session termination DELETE cancellation")
	}
}

func waitForTerminationResult(t *testing.T, result <-chan terminationResult) terminationResult {
	t.Helper()
	select {
	case got := <-result:
		return got
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for session termination result")
		return terminationResult{}
	}
}
