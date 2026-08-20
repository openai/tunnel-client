package dispatcherinternal

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net"
	"net/http"
	"strings"
	"syscall"

	"github.com/modelcontextprotocol/go-sdk/jsonrpc"
	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/openai/tunnel-client/pkg/mcpclient"
	"github.com/openai/tunnel-client/pkg/version"
)

type tunnelFailureSource string

const (
	tunnelFailureSourceTargetHTTP      tunnelFailureSource = "target_http"
	tunnelFailureSourceDNS             tunnelFailureSource = "dns"
	tunnelFailureSourceTLS             tunnelFailureSource = "tls"
	tunnelFailureSourceConnect         tunnelFailureSource = "connect"
	tunnelFailureSourceTransportClosed tunnelFailureSource = "transport_closed"
	tunnelFailureSourceTimeout         tunnelFailureSource = "timeout"
	tunnelFailureSourceProtocol        tunnelFailureSource = "protocol"
	tunnelFailureSourceClientInternal  tunnelFailureSource = "client_internal"
)

// transportErrorKind is the bounded diagnostic taxonomy that can be emitted in
// synthesized tunnel failure provenance. Keep the wire values stable: newer
// clients may add kinds, but services must continue to tolerate unknown values.
type transportErrorKind string

const (
	transportErrorKindUnspecified             transportErrorKind = ""
	transportErrorKindClosedPipe              transportErrorKind = "closed_pipe"
	transportErrorKindConnectionAborted       transportErrorKind = "connection_aborted"
	transportErrorKindConnectionClosed        transportErrorKind = "connection_closed"
	transportErrorKindConnectionRefused       transportErrorKind = "connection_refused"
	transportErrorKindConnectionReset         transportErrorKind = "connection_reset"
	transportErrorKindDial                    transportErrorKind = "dial"
	transportErrorKindDNS                     transportErrorKind = "dns"
	transportErrorKindEOF                     transportErrorKind = "eof"
	transportErrorKindHostUnreachable         transportErrorKind = "host_unreachable"
	transportErrorKindHTTPStatus              transportErrorKind = "http_status"
	transportErrorKindInvalidMCPError         transportErrorKind = transportErrorKind(mcpclient.NonProtocolResponseInvalidMCPError)
	transportErrorKindInvalidProtocolResponse transportErrorKind = "invalid_protocol_response"
	transportErrorKindMalformedJSON           transportErrorKind = transportErrorKind(mcpclient.NonProtocolResponseMalformedJSON)
	transportErrorKindNetworkUnreachable      transportErrorKind = "network_unreachable"
	transportErrorKindNonProtocolResponse     transportErrorKind = "non_protocol_response"
	transportErrorKindResponseBodyMissing     transportErrorKind = transportErrorKind(mcpclient.NonProtocolResponseBodyMissing)
	transportErrorKindResponseBodyTooLarge    transportErrorKind = transportErrorKind(mcpclient.NonProtocolResponseBodyTooLarge)
	transportErrorKindResponseBodyUnreadable  transportErrorKind = transportErrorKind(mcpclient.NonProtocolResponseBodyUnreadable)
	transportErrorKindTimeout                 transportErrorKind = "timeout"
	transportErrorKindTLS                     transportErrorKind = "tls"
	transportErrorKindUnexpectedEOF           transportErrorKind = "unexpected_eof"
	transportErrorKindUnknown                 transportErrorKind = "unknown"
	// Cancellation remains local-log-only and is omitted from the public
	// provenance envelope.
	transportErrorKindCanceled transportErrorKind = "canceled"
)

type tunnelFailure struct {
	Version                  int                 `json:"version"`
	Source                   tunnelFailureSource `json:"source"`
	TransportErrorKind       transportErrorKind  `json:"transport_error_kind,omitempty"`
	UpstreamResponseReceived bool                `json:"upstream_response_received"`
	UpstreamStatus           int                 `json:"upstream_status,omitempty"`
}

type tunnelFailureData struct {
	TunnelFailure tunnelFailure `json:"tunnel_failure"`
}

// protocolFailureError marks a response-processing failure as target protocol
// behavior without exposing the underlying payload or exception text.
type protocolFailureError struct {
	cause error
}

func (e *protocolFailureError) Error() string { return "MCP protocol failure" }

func (e *protocolFailureError) Unwrap() error {
	if e == nil {
		return nil
	}
	return e.cause
}

func newProtocolFailureError(cause error) error {
	return &protocolFailureError{cause: cause}
}

func classifyTunnelFailure(statusCode int, err error) tunnelFailure {
	transportErrorKind := classifyTransportErrorKind(statusCode, err)
	// Cancellation is a local lifecycle outcome, not a target transport
	// failure. Current cancellation paths drop without posting a synthesized
	// response; keep the log-only classifier out of the public envelope if a
	// future caller reaches this path.
	if transportErrorKind == transportErrorKindCanceled {
		transportErrorKind = transportErrorKindUnspecified
	}
	failure := tunnelFailure{
		Version:                  1,
		Source:                   tunnelFailureSourceClientInternal,
		TransportErrorKind:       transportErrorKind,
		UpstreamResponseReceived: false,
	}

	// A target-owned HTTP status is stronger evidence than any transport error
	// the MCP SDK may synthesize while interpreting its response body.
	if statusCode >= http.StatusBadRequest && statusCode <= 599 {
		failure.Source = tunnelFailureSourceTargetHTTP
		failure.UpstreamResponseReceived = true
		failure.UpstreamStatus = statusCode
		return failure
	}

	var nonProtocolResponse *mcpclient.NonProtocolResponseError
	if errors.As(err, &nonProtocolResponse) {
		failure.Source = tunnelFailureSourceProtocol
		return failure
	}

	var protocolFailure *protocolFailureError
	if errors.As(err, &protocolFailure) {
		failure.Source = tunnelFailureSourceProtocol
		return failure
	}

	if errors.Is(err, io.ErrClosedPipe) ||
		errors.Is(err, io.EOF) ||
		errors.Is(err, io.ErrUnexpectedEOF) ||
		errors.Is(err, net.ErrClosed) ||
		errors.Is(err, mcp.ErrConnectionClosed) ||
		errors.Is(err, syscall.EPIPE) ||
		errors.Is(err, syscall.ECONNRESET) ||
		errors.Is(err, syscall.ECONNABORTED) {
		failure.Source = tunnelFailureSourceTransportClosed
		return failure
	}

	var dnsErr *net.DNSError
	if errors.As(err, &dnsErr) {
		failure.Source = tunnelFailureSourceDNS
		return failure
	}

	if isTLSFailure(err) {
		failure.Source = tunnelFailureSourceTLS
		return failure
	}

	if errors.Is(err, context.DeadlineExceeded) {
		failure.Source = tunnelFailureSourceTimeout
		return failure
	}
	var timeoutErr interface{ Timeout() bool }
	if errors.As(err, &timeoutErr) && timeoutErr.Timeout() {
		failure.Source = tunnelFailureSourceTimeout
		return failure
	}

	var opErr *net.OpError
	if errors.As(err, &opErr) && strings.EqualFold(opErr.Op, "dial") {
		failure.Source = tunnelFailureSourceConnect
		return failure
	}
	if errors.Is(err, syscall.ECONNREFUSED) ||
		errors.Is(err, syscall.ENETUNREACH) ||
		errors.Is(err, syscall.EHOSTUNREACH) {
		failure.Source = tunnelFailureSourceConnect
		return failure
	}

	return failure
}

func classifyTransportErrorKind(statusCode int, err error) transportErrorKind {
	var nonProtocolResponse *mcpclient.NonProtocolResponseError
	if errors.As(err, &nonProtocolResponse) {
		return transportErrorKindFromNonProtocolResponse(nonProtocolResponse.Kind())
	}
	if statusCode >= http.StatusBadRequest && statusCode <= 599 {
		return transportErrorKindHTTPStatus
	}
	var protocolFailure *protocolFailureError
	if errors.As(err, &protocolFailure) {
		return transportErrorKindInvalidProtocolResponse
	}

	switch {
	case errors.Is(err, io.ErrClosedPipe), errors.Is(err, syscall.EPIPE):
		return transportErrorKindClosedPipe
	case errors.Is(err, io.EOF):
		return transportErrorKindEOF
	case errors.Is(err, io.ErrUnexpectedEOF):
		return transportErrorKindUnexpectedEOF
	case errors.Is(err, net.ErrClosed), errors.Is(err, mcp.ErrConnectionClosed):
		return transportErrorKindConnectionClosed
	case errors.Is(err, syscall.ECONNRESET):
		return transportErrorKindConnectionReset
	case errors.Is(err, syscall.ECONNABORTED):
		return transportErrorKindConnectionAborted
	}

	var dnsErr *net.DNSError
	if errors.As(err, &dnsErr) {
		return transportErrorKindDNS
	}
	if isTLSFailure(err) {
		return transportErrorKindTLS
	}
	if errors.Is(err, context.DeadlineExceeded) {
		return transportErrorKindTimeout
	}
	var timeoutErr interface{ Timeout() bool }
	if errors.As(err, &timeoutErr) && timeoutErr.Timeout() {
		return transportErrorKindTimeout
	}

	switch {
	case errors.Is(err, syscall.ECONNREFUSED):
		return transportErrorKindConnectionRefused
	case errors.Is(err, syscall.ENETUNREACH):
		return transportErrorKindNetworkUnreachable
	case errors.Is(err, syscall.EHOSTUNREACH):
		return transportErrorKindHostUnreachable
	}
	var opErr *net.OpError
	if errors.As(err, &opErr) && strings.EqualFold(opErr.Op, "dial") {
		return transportErrorKindDial
	}
	if errors.Is(err, context.Canceled) {
		return transportErrorKindCanceled
	}
	return transportErrorKindUnknown
}

func transportErrorKindFromNonProtocolResponse(kind mcpclient.NonProtocolResponseKind) transportErrorKind {
	switch kind {
	case mcpclient.NonProtocolResponseBodyMissing:
		return transportErrorKindResponseBodyMissing
	case mcpclient.NonProtocolResponseBodyUnreadable:
		return transportErrorKindResponseBodyUnreadable
	case mcpclient.NonProtocolResponseBodyTooLarge:
		return transportErrorKindResponseBodyTooLarge
	case mcpclient.NonProtocolResponseMalformedJSON:
		return transportErrorKindMalformedJSON
	case mcpclient.NonProtocolResponseInvalidMCPError:
		return transportErrorKindInvalidMCPError
	default:
		return transportErrorKindNonProtocolResponse
	}
}

func isTLSFailure(err error) bool {
	var verificationErr *tls.CertificateVerificationError
	var recordHeaderErr tls.RecordHeaderError
	var alertErr tls.AlertError
	var unknownAuthorityErr x509.UnknownAuthorityError
	var hostnameErr x509.HostnameError
	var certificateInvalidErr x509.CertificateInvalidError
	var systemRootsErr x509.SystemRootsError
	return errors.As(err, &verificationErr) ||
		errors.As(err, &recordHeaderErr) ||
		errors.As(err, &alertErr) ||
		errors.As(err, &unknownAuthorityErr) ||
		errors.As(err, &hostnameErr) ||
		errors.As(err, &certificateInvalidErr) ||
		errors.As(err, &systemRootsErr)
}

func buildTunnelFailureJSONRPCErrorResponse(req *jsonrpc.Request, statusCode int, failure tunnelFailure) ([]byte, error) {
	if req == nil {
		return nil, errors.New("nil request provided to build tunnel failure response")
	}
	if statusCode == 0 {
		statusCode = http.StatusInternalServerError
	}
	message := http.StatusText(statusCode)
	if message == "" {
		message = "MCP transport error"
	}
	data, err := json.Marshal(tunnelFailureData{TunnelFailure: failure})
	if err != nil {
		return nil, err
	}
	return jsonrpc.EncodeMessage(&jsonrpc.Response{
		ID: req.ID,
		Error: &jsonrpc.Error{
			Code:    jsonrpc.CodeInternalError,
			Message: message,
			Data:    data,
		},
	})
}

func tunnelFailureLogAttrs(failure tunnelFailure, transportErrorKind transportErrorKind) []any {
	attrs := []any{
		slog.String("failure_source", string(failure.Source)),
		slog.String("transport_error_kind", string(transportErrorKind)),
		slog.Bool("upstream_response_received", failure.UpstreamResponseReceived),
		slog.String("tunnel_client_version", version.Version),
	}
	if failure.UpstreamStatus != 0 {
		attrs = append(attrs, slog.Int("upstream_status", failure.UpstreamStatus))
	}
	return attrs
}
