package types

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
)

// ResponseType enumerates the kinds of responses that can be returned to the
// control plane.
type ResponseType int

const (
	// ResponseTypeJSONRPCResponse indicates the payload carries a JSON-RPC
	// response from the MCP server.
	ResponseTypeJSONRPCResponse ResponseType = iota
	// ResponseTypeJSONRPCNotification indicates the payload carries a JSON-RPC
	// notification emitted by the MCP server.
	ResponseTypeJSONRPCNotification
	// ResponseTypeNotificationAcknowledgment indicates the payload acknowledges a
	// notification that produced no JSON-RPC response body.
	ResponseTypeNotificationAcknowledgment
	// ResponseTypeOAuthDiscovery indicates the payload contains OAuth discovery
	// metadata fetched from the MCP server.
	ResponseTypeOAuthDiscovery
)

// TunnelResponse bundles the MCP response metadata (status code + headers) with
// either a JSON-RPC response message or a notification acknowledgement.
type TunnelResponse struct {
	response     json.RawMessage
	headers      http.Header
	responseCode int
	responseType ResponseType
	channel      Channel
}

// NewTunnelResponse constructs a TunnelResponse, defensively copying the
// provided headers map so callers can mutate their copy without affecting the
// payload delivered to tunnel-service.
func NewTunnelResponse(channel Channel, response json.RawMessage, code int, headers http.Header) *TunnelResponse {
	return &TunnelResponse{
		response:     response,
		headers:      cloneHeaders(headers),
		responseCode: code,
		responseType: ResponseTypeJSONRPCResponse,
		channel:      channel,
	}
}

// NewOAuthDiscoveryResponse constructs a TunnelResponse representing OAuth
// metadata fetched from the MCP server.
func NewOAuthDiscoveryResponse(channel Channel, response json.RawMessage, code int, headers http.Header) *TunnelResponse {
	return &TunnelResponse{
		response:     response,
		headers:      cloneHeaders(headers),
		responseCode: code,
		responseType: ResponseTypeOAuthDiscovery,
		channel:      channel,
	}
}

// NewJSONRPCNotification constructs a TunnelResponse that forwards a JSON-RPC
// notification payload emitted by the MCP server.
func NewJSONRPCNotification(channel Channel, response json.RawMessage, code int, headers http.Header) *TunnelResponse {
	return &TunnelResponse{
		response:     response,
		headers:      cloneHeaders(headers),
		responseCode: code,
		responseType: ResponseTypeJSONRPCNotification,
		channel:      channel,
	}
}

// NewNotificationAck constructs a TunnelResponse representing a successful
// acknowledgement of a JSON-RPC notification (which carries no response body).
func NewNotificationAck(channel Channel, code int, headers http.Header) *TunnelResponse {
	return &TunnelResponse{
		headers:      cloneHeaders(headers),
		responseCode: code,
		responseType: ResponseTypeNotificationAcknowledgment,
		channel:      channel,
	}
}

// Payload returns the raw JSON payload for the response.
func (t *TunnelResponse) Payload() json.RawMessage {
	return t.response
}

// Type returns the response type enum.
func (t *TunnelResponse) Type() ResponseType {
	return t.responseType
}

// ResponseCode returns the HTTP status code observed when forwarding the
// request to the MCP server.
func (t *TunnelResponse) ResponseCode() int {
	return t.responseCode
}

// Headers returns a defensive copy of the response headers map.
func (t *TunnelResponse) Headers() http.Header {
	return cloneHeaders(t.headers)
}

// Channel returns the channel name to report back to the control plane.
func (t *TunnelResponse) Channel() Channel {
	if t == nil {
		return ""
	}
	return t.channel
}

// Validate returns an error if the response is structurally invalid.
func (t *TunnelResponse) Validate() error {
	if t == nil {
		return errors.New("tunnel response is nil")
	}

	switch t.responseType {
	case ResponseTypeNotificationAcknowledgment:
		if len(t.response) > 0 {
			return errors.New("notification acknowledgments must not include a jsonrpc response")
		}
	case ResponseTypeJSONRPCNotification:
		if len(t.response) == 0 {
			return errors.New("jsonrpc notification is required")
		}
	case ResponseTypeOAuthDiscovery:
		if len(t.response) == 0 {
			return errors.New("oauth discovery response is required")
		}
	case ResponseTypeJSONRPCResponse:
		if len(t.response) == 0 {
			return errors.New("jsonrpc response is required")
		}
	default:
		return fmt.Errorf("unknown response type %d", t.responseType)
	}
	if t.channel != "" {
		if _, err := NormalizeChannel(t.channel.String()); err != nil {
			return err
		}
	}
	return nil
}

func cloneHeaders(headers http.Header) http.Header {
	if headers == nil {
		return nil
	}
	return headers.Clone()
}
