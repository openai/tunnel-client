package mcpclient

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"testing"

	"github.com/modelcontextprotocol/go-sdk/jsonrpc"
	"github.com/stretchr/testify/require"
)

func TestStdioInitializationRejectsOperationalRequestsWithoutWriting(t *testing.T) {
	t.Parallel()
	for _, rawID := range []any{float64(0), "request-0"} {
		for _, method := range []string{"tools/call", "tools/list", "resources/read", "prompts/get", "custom/operation"} {
			t.Run(method+"/"+fmt.Sprint(rawID), func(t *testing.T) {
				t.Parallel()
				transport, base := initializationTransport(false)
				request := &jsonrpc.Request{ID: initializationID(t, rawID), Method: method}
				initializationRequireRejected(t, transport, base, request)
				require.Empty(t, base.writtenMethods())
				require.Zero(t, base.closeCalls.Load())
			})
		}
	}
}

func TestStdioInitializationRequiresResponseThenNotification(t *testing.T) {
	t.Parallel()
	for _, shim := range []bool{false, true} {
		name := "caller_notification"
		if shim {
			name = "shim_notification"
		}
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			transport, base := initializationTransport(shim)
			initID := initializationID(t, "initialize")
			initializationRoundTrip(t, transport, base, &jsonrpc.Request{ID: initID, Method: "initialize"}, &jsonrpc.Response{ID: initID})
			request := &jsonrpc.Request{ID: initializationID(t, float64(0)), Method: "tools/call"}
			if !shim {
				initializationRequireRejected(t, transport, base, request)
				initializationNotify(t, transport, initializedNotificationMethod)
			}
			initializationRoundTrip(t, transport, base, request, &jsonrpc.Response{ID: request.ID, Result: json.RawMessage(`{"ok":true}`)})
			require.Equal(t, []string{"initialize", initializedNotificationMethod, "tools/call"}, base.writtenMethods())
			require.Zero(t, base.closeCalls.Load())
		})
	}
}

func TestStdioInitializationUnsolicitedNotificationDoesNotOpenGate(t *testing.T) {
	t.Parallel()
	transport, base := initializationTransport(false)
	initializationNotify(t, transport, initializedNotificationMethod)
	request := &jsonrpc.Request{ID: initializationID(t, "tool"), Method: "tools/call"}
	initializationRequireRejected(t, transport, base, request)
	initID := initializationID(t, "initialize")
	initializationRoundTrip(t, transport, base, &jsonrpc.Request{ID: initID, Method: "initialize"}, &jsonrpc.Response{ID: initID})
	initializationRequireRejected(t, transport, base, request)
	initializationNotify(t, transport, initializedNotificationMethod)
	initializationRoundTrip(t, transport, base, request, &jsonrpc.Response{ID: request.ID})
	require.Equal(t, []string{initializedNotificationMethod, "initialize", initializedNotificationMethod, "tools/call"}, base.writtenMethods())
}

func TestStdioInitializationFailedOrMismatchedResponseDoesNotOpenGate(t *testing.T) {
	t.Parallel()
	for _, name := range []string{"error", "mismatched_id"} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			transport, base := initializationTransport(false)
			initID := initializationID(t, "initialize")
			response := &jsonrpc.Response{ID: initID}
			if name == "error" {
				response.Error = errors.New("initialize rejected")
			} else {
				response.ID = initializationID(t, "unrelated")
			}
			initializationRoundTrip(t, transport, base, &jsonrpc.Request{ID: initID, Method: "initialize"}, response)
			initializationNotify(t, transport, initializedNotificationMethod)
			initializationRequireRejected(t, transport, base, &jsonrpc.Request{ID: initializationID(t, "tool"), Method: "tools/call"})
			require.Equal(t, []string{"initialize", initializedNotificationMethod}, base.writtenMethods())
		})
	}
}

func TestStdioInitializationFailedNotificationDoesNotOpenGate(t *testing.T) {
	t.Parallel()
	for _, shim := range []bool{false, true} {
		for _, failure := range []string{"write_error", "http_error", "preserved_error"} {
			name := "explicit/" + failure
			if shim {
				name = "shim/" + failure
			}
			t.Run(name, func(t *testing.T) {
				t.Parallel()
				transport, base := initializationTransport(shim)
				initID := initializationID(t, "initialize")
				conn, err := transport.Connect(context.Background())
				require.NoError(t, err)
				result, err := conn.Write(context.Background(), nil, &jsonrpc.Request{ID: initID, Method: "initialize"})
				require.NoError(t, err)
				require.Nil(t, result.PreservedError)
				if !shim {
					base.enqueueRead(&jsonrpc.Response{ID: initID}, nil)
					_, err = conn.Read(context.Background())
					require.NoError(t, err)
				}
				switch failure {
				case "write_error":
					base.enqueueWriteResult(0, nil, io.ErrClosedPipe)
				case "http_error":
					base.enqueueWriteResult(http.StatusBadGateway, nil, nil)
				case "preserved_error":
					base.writeResults <- stubWriteResult{statusCode: http.StatusBadRequest, preservedError: NewPreservedMCPError([]byte(`{"jsonrpc":"2.0","id":null,"error":{"code":-32000,"message":"rejected"}}`), -32000)}
				}
				if shim {
					base.enqueueRead(&jsonrpc.Response{ID: initID}, nil)
					_, err = conn.Read(context.Background())
					require.Error(t, err)
				} else {
					note, err := transport.Connect(context.Background())
					require.NoError(t, err)
					result, err := note.Write(context.Background(), nil, &jsonrpc.Request{Method: initializedNotificationMethod})
					require.True(t, err != nil || result.StatusCode >= 400 || result.PreservedError != nil)
				}
				initializationRequireRejected(t, transport, base, &jsonrpc.Request{ID: initializationID(t, "tool"), Method: "tools/call"})
			})
		}
	}
}

func TestStdioInitializationModernRequestsPassWithoutOpeningLegacyGate(t *testing.T) {
	t.Parallel()
	for _, testCase := range []struct {
		name   string
		params string
		modern bool
	}{
		{"current", `{"_meta":{"io.modelcontextprotocol/protocolVersion":"2026-07-28","io.modelcontextprotocol/clientCapabilities":{}},"name":"echo","arguments":{}}`, true},
		{"future", `{"_meta":{"io.modelcontextprotocol/protocolVersion":"2027-01-01","io.modelcontextprotocol/clientCapabilities":{"sampling":{}}}}`, true},
		{"legacy", `{"_meta":{"io.modelcontextprotocol/protocolVersion":"2025-11-25","io.modelcontextprotocol/clientCapabilities":{}}}`, false},
		{"missing_capabilities", `{"_meta":{"io.modelcontextprotocol/protocolVersion":"2026-07-28"}}`, false},
		{"null_capabilities", `{"_meta":{"io.modelcontextprotocol/protocolVersion":"2026-07-28","io.modelcontextprotocol/clientCapabilities":null}}`, false},
		{"array_capabilities", `{"_meta":{"io.modelcontextprotocol/protocolVersion":"2026-07-28","io.modelcontextprotocol/clientCapabilities":[]}}`, false},
		{"non_date_version", `{"_meta":{"io.modelcontextprotocol/protocolVersion":"future","io.modelcontextprotocol/clientCapabilities":{}}}`, false},
		{"missing_params", "", false},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			t.Parallel()
			transport, base := initializationTransport(false)
			request := &jsonrpc.Request{ID: initializationID(t, "modern"), Method: "tools/call", Params: json.RawMessage(testCase.params)}
			if testCase.modern {
				initializationRoundTrip(t, transport, base, request, &jsonrpc.Response{ID: request.ID})
				base.writesMu.Lock()
				written := base.writes[0].(*jsonrpc.Request)
				base.writesMu.Unlock()
				require.Equal(t, request, written, "self-contained request must remain unchanged")
				initializationNotify(t, transport, initializedNotificationMethod)
			} else {
				initializationRequireRejected(t, transport, base, request)
			}
			initializationRequireRejected(t, transport, base, &jsonrpc.Request{ID: initializationID(t, "legacy"), Method: "tools/call"})
		})
	}
}

func TestStdioInitializationAllowsProtocolControlMessages(t *testing.T) {
	t.Parallel()
	transport, base := initializationTransport(false)
	for _, method := range []string{"ping", "server/discover"} {
		id := initializationID(t, method)
		initializationRoundTrip(t, transport, base, &jsonrpc.Request{ID: id, Method: method}, &jsonrpc.Response{ID: id})
	}
	initializationNotify(t, transport, "notifications/roots/list_changed")
	initializationNotify(t, transport, "notifications/cancelled")
	conn, err := transport.Connect(context.Background())
	require.NoError(t, err)
	result, err := conn.Write(context.Background(), nil, &jsonrpc.Response{ID: initializationID(t, "server-request")})
	require.NoError(t, err)
	require.Nil(t, result.PreservedError)
	require.EqualValues(t, 5, base.writeCalls.Load())
	initializationRequireRejected(t, transport, base, &jsonrpc.Request{ID: initializationID(t, "tool"), Method: "tools/call"})
}

func TestStdioInitializationModernLifecycleDoesNotChangeLegacyReadiness(t *testing.T) {
	t.Parallel()
	for _, ready := range []bool{false, true} {
		t.Run(fmt.Sprint(ready), func(t *testing.T) {
			t.Parallel()
			transport, base := initializationTransport(true)
			if ready {
				initializationReady(t, transport, base, true)
			}
			before := base.writeCalls.Load()
			params := json.RawMessage(`{"_meta":{"io.modelcontextprotocol/protocolVersion":"2026-07-28","io.modelcontextprotocol/clientCapabilities":{}}}`)
			id := initializationID(t, "modern-initialize")
			initializationRoundTrip(t, transport, base, &jsonrpc.Request{ID: id, Method: "initialize", Params: params}, &jsonrpc.Response{ID: id})
			note, err := transport.Connect(context.Background())
			require.NoError(t, err)
			result, err := note.Write(context.Background(), nil, &jsonrpc.Request{Method: initializedNotificationMethod, Params: params})
			require.NoError(t, err)
			require.Nil(t, result.PreservedError)
			require.Equal(t, before+2, base.writeCalls.Load(), "modern lifecycle messages must neither synthesize nor suppress a notification")
			request := &jsonrpc.Request{ID: initializationID(t, "legacy-tool"), Method: "tools/call"}
			if ready {
				initializationRoundTrip(t, transport, base, request, &jsonrpc.Response{ID: request.ID})
			} else {
				initializationRequireRejected(t, transport, base, request)
			}
		})
	}
}

func TestStdioInitializationRejectedRequestRetiresWithoutPoisoningSameID(t *testing.T) {
	t.Parallel()
	transport, base := initializationTransport(false)
	request := &jsonrpc.Request{ID: initializationID(t, float64(0)), Method: "tools/call"}
	conn := initializationRequireRejected(t, transport, base, request)
	retiring := conn.(ResponseDeadlineRetiringConnection)
	require.True(t, retiring.RetireResponseDeadline())
	require.True(t, retiring.RetireResponseDeadline(), "local rejection retirement must be idempotent")
	require.NoError(t, conn.Close())
	require.False(t, transport.hasRetiredResponseIDs(), "request never reached the child, so there can be no late response")
	require.Zero(t, base.closeCalls.Load())
	initializationReady(t, transport, base, false)
	initializationRoundTrip(t, transport, base, request, &jsonrpc.Response{ID: request.ID})
	require.Equal(t, request.ID, base.writtenRequestIDs()[1], "locally rejected ID must not acquire a downstream alias")
}

func TestStdioInitializationReadySurvivesOrdinaryResponseDeadline(t *testing.T) {
	t.Parallel()
	transport, base := initializationTransport(true)
	initializationReady(t, transport, base, true)
	id := initializationID(t, "slow-tool")
	conn, err := transport.Connect(context.Background())
	require.NoError(t, err)
	result, err := conn.Write(ContextWithResponseDeadlineEnforcement(context.Background()), nil, &jsonrpc.Request{ID: id, Method: "tools/call"})
	require.NoError(t, err)
	require.Nil(t, result.PreservedError)
	require.True(t, conn.(ResponseDeadlineRetiringConnection).RetireResponseDeadline())
	require.NoError(t, conn.Close())
	require.Zero(t, base.closeCalls.Load())
	// The next reader must discard the old response and still use the ready child.
	base.enqueueRead(&jsonrpc.Response{ID: id}, nil)
	nextID := initializationID(t, "next-tool")
	initializationRoundTrip(t, transport, base, &jsonrpc.Request{ID: nextID, Method: "tools/call"}, &jsonrpc.Response{ID: nextID})
	require.False(t, transport.hasRetiredResponseIDs())
}

func TestStdioInitializationReadyClearsOnCloseOrFatalIO(t *testing.T) {
	t.Parallel()
	for _, failure := range []string{"close", "read_eof", "write_error"} {
		t.Run(failure, func(t *testing.T) {
			t.Parallel()
			transport, base := initializationTransport(true)
			initializationReady(t, transport, base, true)
			conn, err := transport.Connect(context.Background())
			require.NoError(t, err)
			if failure == "close" {
				require.NoError(t, conn.Close())
			} else {
				if failure == "write_error" {
					base.enqueueWriteResult(0, nil, io.ErrClosedPipe)
				}
				_, err = conn.Write(context.Background(), nil, &jsonrpc.Request{ID: initializationID(t, "failure"), Method: "tools/call"})
				if failure == "write_error" {
					require.ErrorIs(t, err, io.ErrClosedPipe)
				} else {
					require.NoError(t, err)
					base.enqueueRead(nil, io.EOF)
					_, err = conn.Read(context.Background())
					require.ErrorIs(t, err, io.EOF)
				}
			}
			initializationRequireRejected(t, transport, base, &jsonrpc.Request{ID: initializationID(t, "next"), Method: "tools/call"})
		})
	}
}

func TestStdioInitializationReinitializeRequiresNewNotification(t *testing.T) {
	t.Parallel()
	transport, base := initializationTransport(false)
	initializationReady(t, transport, base, false)
	id := initializationID(t, "second-initialize")
	initializationRoundTrip(t, transport, base, &jsonrpc.Request{ID: id, Method: "initialize"}, &jsonrpc.Response{ID: id})
	request := &jsonrpc.Request{ID: initializationID(t, "tool"), Method: "tools/call"}
	initializationRequireRejected(t, transport, base, request)
	initializationNotify(t, transport, initializedNotificationMethod)
	initializationRoundTrip(t, transport, base, request, &jsonrpc.Response{ID: request.ID})
}

func TestStdioInitializationCanceledUnwrittenInitializePreservesReadyChild(t *testing.T) {
	t.Parallel()
	transport, base := initializationTransport(false)
	initializationReady(t, transport, base, false)
	conn, err := transport.Connect(context.Background())
	require.NoError(t, err)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	ctx = ContextWithResponseDeadlineEnforcement(ctx)
	_, err = conn.Write(ctx, nil, &jsonrpc.Request{ID: initializationID(t, "unwritten"), Method: "initialize"})
	require.ErrorIs(t, err, context.Canceled)
	require.True(t, conn.(ResponseDeadlineRetiringConnection).RetireResponseDeadline())
	require.NoError(t, conn.Close())
	require.Zero(t, base.closeCalls.Load())
	request := &jsonrpc.Request{ID: initializationID(t, "tool"), Method: "tools/call"}
	initializationRoundTrip(t, transport, base, request, &jsonrpc.Response{ID: request.ID})
	require.Equal(t, []string{"initialize", initializedNotificationMethod, "tools/call"}, base.writtenMethods())
}

func TestStdioInitializationCancellationInsideWritePreservesReadyChild(t *testing.T) {
	t.Parallel()
	for _, shim := range []bool{false, true} {
		t.Run(fmt.Sprint(shim), func(t *testing.T) {
			t.Parallel()
			base := newStubSerializedForwardingConnection()
			canceling := &initializationCancelBeforeWriteConnection{ForwardingConnection: base}
			transport := NewStdioForwardingTransportWithOptions(&stubSerializedForwardingTransport{conn: canceling}, StdioForwardingOptions{
				SendInitializedNotification: shim,
			}).(*serializedForwardingTransport)
			initializationReady(t, transport, base, shim)
			before := base.writeCalls.Load()
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			ctx = ContextWithResponseDeadlineEnforcement(ctx)
			conn, err := transport.Connect(ctx)
			require.NoError(t, err)
			canceling.cancelNext = cancel
			_, err = conn.Write(ctx, nil, &jsonrpc.Request{ID: initializationID(t, "canceled-inside-write"), Method: "initialize"})
			require.ErrorIs(t, err, context.Canceled)
			require.Equal(t, before, base.writeCalls.Load(), "cancellation occurs after the guard checks context but before the base writes bytes")
			require.True(t, conn.(ResponseDeadlineRetiringConnection).RetireResponseDeadline())
			require.NoError(t, conn.Close())
			require.Zero(t, base.closeCalls.Load())
			require.False(t, transport.hasRetiredResponseIDs())
			if shim {
				initializationNotify(t, transport, initializedNotificationMethod)
				require.Equal(t, before, base.writeCalls.Load(), "unwritten initialize must preserve suppression of the prior handshake notification")
			}
			request := &jsonrpc.Request{ID: initializationID(t, "tool"), Method: "tools/call"}
			initializationRoundTrip(t, transport, base, request, &jsonrpc.Response{ID: request.ID})
			require.Equal(t, []string{"initialize", initializedNotificationMethod, "tools/call"}, base.writtenMethods())
		})
	}
}

func TestStdioInitializationSuccessfulResponseAtCancellationOpensGate(t *testing.T) {
	t.Parallel()
	transport, base := initializationTransport(true)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	base.afterRead = cancel
	conn, err := transport.Connect(ctx)
	require.NoError(t, err)
	id := initializationID(t, "initialize")
	_, err = conn.Write(ctx, nil, &jsonrpc.Request{ID: id, Method: "initialize"})
	require.NoError(t, err)
	base.enqueueRead(&jsonrpc.Response{ID: id}, nil)
	_, err = conn.Read(ctx)
	require.NoError(t, err)
	require.ErrorIs(t, ctx.Err(), context.Canceled)
	require.Equal(t, []string{"initialize", initializedNotificationMethod}, base.writtenMethods())
	// The response was received and the detached shim write succeeded. A
	// canceled upstream context must not erase that completed child handshake.
	base.afterRead = nil
	request := &jsonrpc.Request{ID: initializationID(t, "tool"), Method: "tools/call"}
	initializationRoundTrip(t, transport, base, request, &jsonrpc.Response{ID: request.ID})
}

func TestStdioInitializationOptionsEnforceLegacyHandshakeByDefault(t *testing.T) {
	t.Parallel()
	require.Nil(t, NewStdioForwardingTransportWithOptions(nil, StdioForwardingOptions{}))
	for _, shim := range []bool{false, true} {
		transport, base := initializationTransport(shim)
		request := &jsonrpc.Request{ID: initializationID(t, "legacy-call"), Method: "tools/call"}
		initializationRequireRejected(t, transport, base, request)
		require.Empty(t, base.writtenMethods())
	}
}

func initializationTransport(shim bool) (*serializedForwardingTransport, *stubSerializedForwardingConnection) {
	base := newStubSerializedForwardingConnection()
	transport := NewStdioForwardingTransportWithOptions(&stubSerializedForwardingTransport{conn: base}, StdioForwardingOptions{
		SendInitializedNotification: shim,
	})
	return transport.(*serializedForwardingTransport), base
}

type initializationCancelBeforeWriteConnection struct {
	ForwardingConnection
	cancelNext context.CancelFunc
}

func (c *initializationCancelBeforeWriteConnection) Write(ctx context.Context, headers http.Header, message jsonrpc.Message) (ForwardingWriteResult, error) {
	if c.cancelNext != nil {
		cancel := c.cancelNext
		c.cancelNext = nil
		cancel()
	}
	return c.ForwardingConnection.Write(ctx, headers, message)
}

func initializationID(t *testing.T, raw any) jsonrpc.ID {
	t.Helper()
	id, err := jsonrpc.MakeID(raw)
	require.NoError(t, err)
	return id
}

func initializationRequireRejected(t *testing.T, transport *serializedForwardingTransport, base *stubSerializedForwardingConnection, request *jsonrpc.Request) ForwardingConnection {
	t.Helper()
	before := base.writeCalls.Load()
	conn, err := transport.Connect(context.Background())
	require.NoError(t, err)
	result, err := conn.Write(context.Background(), nil, request)
	require.NoError(t, err, "local rejection must not be treated as a broken transport")
	require.Equal(t, http.StatusConflict, result.StatusCode)
	require.NotNil(t, result.PreservedError)
	require.EqualValues(t, -32002, result.PreservedError.Code())
	require.True(t, result.StartedAt.IsZero(), "local guard rejection must not report downstream timing")
	require.True(t, result.CompletedAt.IsZero())
	message, err := jsonrpc.DecodeMessage(result.PreservedError.Payload())
	require.NoError(t, err)
	response, ok := message.(*jsonrpc.Response)
	require.True(t, ok)
	require.Equal(t, request.ID, response.ID)
	var payload struct {
		Error struct {
			Data struct {
				Origin    string `json:"origin"`
				ErrorType string `json:"error_type"`
			} `json:"data"`
		} `json:"error"`
	}
	require.NoError(t, json.Unmarshal(result.PreservedError.Payload(), &payload))
	require.Equal(t, "tunnel-client", payload.Error.Data.Origin)
	require.Equal(t, "mcp_initialization_required", payload.Error.Data.ErrorType)
	require.Equal(t, before, base.writeCalls.Load(), "guard rejection must not write the operational request or a synthetic initialize")
	requireLifecycleLockReleased(t, transport)
	return conn
}

func initializationNotify(t *testing.T, transport ForwardingTransport, method string) {
	t.Helper()
	conn, err := transport.Connect(context.Background())
	require.NoError(t, err)
	result, err := conn.Write(context.Background(), nil, &jsonrpc.Request{Method: method})
	require.NoError(t, err)
	require.Nil(t, result.PreservedError)
	require.Less(t, result.StatusCode, http.StatusBadRequest)
}

func initializationRoundTrip(t *testing.T, transport ForwardingTransport, base *stubSerializedForwardingConnection, request *jsonrpc.Request, response *jsonrpc.Response) {
	t.Helper()
	conn, err := transport.Connect(context.Background())
	require.NoError(t, err)
	result, err := conn.Write(context.Background(), nil, request)
	require.NoError(t, err)
	require.Nil(t, result.PreservedError)
	require.Less(t, result.StatusCode, http.StatusBadRequest)
	base.enqueueRead(response, nil)
	actual, err := conn.Read(context.Background())
	require.NoError(t, err)
	require.Equal(t, response, actual)
}

func initializationReady(t *testing.T, transport ForwardingTransport, base *stubSerializedForwardingConnection, shim bool) {
	t.Helper()
	id := initializationID(t, "initialize")
	initializationRoundTrip(t, transport, base, &jsonrpc.Request{ID: id, Method: "initialize"}, &jsonrpc.Response{ID: id})
	if !shim {
		initializationNotify(t, transport, initializedNotificationMethod)
	}
}
