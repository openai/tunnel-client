package dispatcherinternal

import (
	"context"
	"crypto/tls"
	"encoding/json"
	"errors"
	"io"
	"net"
	"net/http"
	"syscall"
	"testing"

	"github.com/modelcontextprotocol/go-sdk/jsonrpc"
	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/stretchr/testify/require"

	"github.com/openai/tunnel-client/pkg/mcpclient"
)

func TestClassifyTunnelFailure(t *testing.T) {
	t.Parallel()

	testCases := []struct {
		name       string
		statusCode int
		err        error
		want       tunnelFailure
		wantKind   transportErrorKind
	}{
		{
			name:       "target HTTP response wins over body parse failure",
			statusCode: http.StatusMethodNotAllowed,
			err:        errors.New("target body contained a secret"),
			want: tunnelFailure{
				Version:                  1,
				Source:                   tunnelFailureSourceTargetHTTP,
				TransportErrorKind:       transportErrorKindHTTPStatus,
				UpstreamResponseReceived: true,
				UpstreamStatus:           http.StatusMethodNotAllowed,
			},
			wantKind: transportErrorKindHTTPStatus,
		},
		{
			name:       "non-protocol response kind wins over target HTTP fallback",
			statusCode: http.StatusBadGateway,
			err:        &mcpclient.NonProtocolResponseError{},
			want: tunnelFailure{
				Version:                  1,
				Source:                   tunnelFailureSourceTargetHTTP,
				TransportErrorKind:       transportErrorKindNonProtocolResponse,
				UpstreamResponseReceived: true,
				UpstreamStatus:           http.StatusBadGateway,
			},
			wantKind: transportErrorKindNonProtocolResponse,
		},
		{name: "DNS", err: &net.DNSError{Err: "no such host", Name: "private.example"}, want: tunnelFailure{Version: 1, Source: tunnelFailureSourceDNS, TransportErrorKind: transportErrorKindDNS}, wantKind: transportErrorKindDNS},
		{name: "TLS", err: tls.RecordHeaderError{}, want: tunnelFailure{Version: 1, Source: tunnelFailureSourceTLS, TransportErrorKind: transportErrorKindTLS}, wantKind: transportErrorKindTLS},
		{name: "connect", err: &net.OpError{Op: "dial", Net: "tcp", Err: syscall.ECONNREFUSED}, want: tunnelFailure{Version: 1, Source: tunnelFailureSourceConnect, TransportErrorKind: transportErrorKindConnectionRefused}, wantKind: transportErrorKindConnectionRefused},
		{name: "closed pipe", err: io.ErrClosedPipe, want: tunnelFailure{Version: 1, Source: tunnelFailureSourceTransportClosed, TransportErrorKind: transportErrorKindClosedPipe}, wantKind: transportErrorKindClosedPipe},
		{name: "MCP connection closed", err: mcp.ErrConnectionClosed, want: tunnelFailure{Version: 1, Source: tunnelFailureSourceTransportClosed, TransportErrorKind: transportErrorKindConnectionClosed}, wantKind: transportErrorKindConnectionClosed},
		{name: "timeout", err: context.DeadlineExceeded, want: tunnelFailure{Version: 1, Source: tunnelFailureSourceTimeout, TransportErrorKind: transportErrorKindTimeout}, wantKind: transportErrorKindTimeout},
		{name: "non-protocol response", err: &mcpclient.NonProtocolResponseError{}, want: tunnelFailure{Version: 1, Source: tunnelFailureSourceProtocol, TransportErrorKind: transportErrorKindNonProtocolResponse}, wantKind: transportErrorKindNonProtocolResponse},
		{name: "protocol", err: newProtocolFailureError(errors.New("private target payload")), want: tunnelFailure{Version: 1, Source: tunnelFailureSourceProtocol, TransportErrorKind: transportErrorKindInvalidProtocolResponse}, wantKind: transportErrorKindInvalidProtocolResponse},
		{name: "unknown", err: errors.New("private target URL and token"), want: tunnelFailure{Version: 1, Source: tunnelFailureSourceClientInternal, TransportErrorKind: transportErrorKindUnknown}, wantKind: transportErrorKindUnknown},
		{name: "canceled", err: context.Canceled, want: tunnelFailure{Version: 1, Source: tunnelFailureSourceClientInternal}, wantKind: transportErrorKindCanceled},
		{name: "nil", want: tunnelFailure{Version: 1, Source: tunnelFailureSourceClientInternal, TransportErrorKind: transportErrorKindUnknown}, wantKind: transportErrorKindUnknown},
	}

	for _, tc := range testCases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			require.Equal(t, tc.want, classifyTunnelFailure(tc.statusCode, tc.err))
			require.Equal(t, tc.wantKind, classifyTransportErrorKind(tc.statusCode, tc.err))
		})
	}
}

func TestTransportErrorKindFromNonProtocolResponse(t *testing.T) {
	t.Parallel()

	testCases := []struct {
		name string
		kind mcpclient.NonProtocolResponseKind
		want transportErrorKind
	}{
		{name: "body missing", kind: mcpclient.NonProtocolResponseBodyMissing, want: transportErrorKindResponseBodyMissing},
		{name: "body unreadable", kind: mcpclient.NonProtocolResponseBodyUnreadable, want: transportErrorKindResponseBodyUnreadable},
		{name: "body too large", kind: mcpclient.NonProtocolResponseBodyTooLarge, want: transportErrorKindResponseBodyTooLarge},
		{name: "malformed JSON", kind: mcpclient.NonProtocolResponseMalformedJSON, want: transportErrorKindMalformedJSON},
		{name: "invalid MCP error", kind: mcpclient.NonProtocolResponseInvalidMCPError, want: transportErrorKindInvalidMCPError},
		{name: "unset", want: transportErrorKindNonProtocolResponse},
		{name: "future kind", kind: mcpclient.NonProtocolResponseKind("future_kind"), want: transportErrorKindNonProtocolResponse},
	}

	for _, tc := range testCases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			require.Equal(t, tc.want, transportErrorKindFromNonProtocolResponse(tc.kind))
		})
	}
}

func TestBuildTunnelFailureJSONRPCErrorResponse(t *testing.T) {
	t.Parallel()

	id, err := jsonrpc.MakeID("rpc-request")
	require.NoError(t, err)
	payload, err := buildTunnelFailureJSONRPCErrorResponse(
		&jsonrpc.Request{ID: id, Method: "tools/list"},
		http.StatusBadGateway,
		classifyTunnelFailure(0, io.ErrClosedPipe),
	)
	require.NoError(t, err)
	require.NotContains(t, string(payload), io.ErrClosedPipe.Error())
	require.NotContains(t, string(payload), "upstream_status")

	response := decodeJSONRPCResponse(t, payload)
	wireError, ok := response.Error.(*jsonrpc.Error)
	require.True(t, ok)
	require.Equal(t, int64(jsonrpc.CodeInternalError), wireError.Code)
	require.Equal(t, http.StatusText(http.StatusBadGateway), wireError.Message)

	var data tunnelFailureData
	require.NoError(t, json.Unmarshal(wireError.Data, &data))
	require.Equal(t, tunnelFailure{
		Version:                  1,
		Source:                   tunnelFailureSourceTransportClosed,
		TransportErrorKind:       transportErrorKindClosedPipe,
		UpstreamResponseReceived: false,
	}, data.TunnelFailure)
}

func TestBuildTunnelFailureJSONRPCErrorResponseOmitsEmptyTransportErrorKind(t *testing.T) {
	t.Parallel()

	id, err := jsonrpc.MakeID("rpc-request")
	require.NoError(t, err)
	payload, err := buildTunnelFailureJSONRPCErrorResponse(
		&jsonrpc.Request{ID: id, Method: "tools/list"},
		http.StatusBadGateway,
		tunnelFailure{
			Version:                  1,
			Source:                   tunnelFailureSourceTransportClosed,
			UpstreamResponseReceived: false,
		},
	)
	require.NoError(t, err)
	require.NotContains(t, string(payload), "transport_error_kind")
}

func TestBuildTunnelFailureJSONRPCErrorResponseKeepsCanceledKindLogOnly(t *testing.T) {
	t.Parallel()

	id, err := jsonrpc.MakeID("rpc-request")
	require.NoError(t, err)
	payload, err := buildTunnelFailureJSONRPCErrorResponse(
		&jsonrpc.Request{ID: id, Method: "tools/list"},
		http.StatusBadGateway,
		classifyTunnelFailure(0, context.Canceled),
	)
	require.NoError(t, err)
	require.NotContains(t, string(payload), "transport_error_kind")
}

func TestBuildTunnelFailureJSONRPCErrorResponseRejectsNilRequest(t *testing.T) {
	t.Parallel()

	_, err := buildTunnelFailureJSONRPCErrorResponse(nil, http.StatusBadGateway, tunnelFailure{})
	require.Error(t, err)
}
