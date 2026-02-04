package types

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestTunnelResponseValidateJSONRPC(t *testing.T) {
	t.Run("valid response", func(t *testing.T) {
		tr := NewTunnelResponse(DefaultChannel, json.RawMessage(`{"jsonrpc":"2.0","id":"1","result":{}}`), 200, nil)
		require.NoError(t, tr.Validate())
	})

	t.Run("missing payload", func(t *testing.T) {
		tr := &TunnelResponse{
			responseType: ResponseTypeJSONRPCResponse,
		}
		err := tr.Validate()
		require.Error(t, err)
		require.Contains(t, err.Error(), "jsonrpc response is required")
	})
}

func TestTunnelResponseValidateNotificationAck(t *testing.T) {
	t.Run("valid ack", func(t *testing.T) {
		require.NoError(t, NewNotificationAck(DefaultChannel, 204, nil).Validate())
	})

	t.Run("ack with payload", func(t *testing.T) {
		tr := &TunnelResponse{
			responseType: ResponseTypeNotificationAcknowledgment,
			response:     json.RawMessage(`{}`),
		}
		err := tr.Validate()
		require.Error(t, err)
		require.Contains(t, err.Error(), "must not include a jsonrpc response")
	})
}

func TestTunnelResponseValidateJSONRPCNotification(t *testing.T) {
	t.Run("valid notification", func(t *testing.T) {
		tr := NewJSONRPCNotification(DefaultChannel, json.RawMessage(`{"jsonrpc":"2.0","method":"notifications/initialized"}`), 200, nil)
		require.NoError(t, tr.Validate())
	})

	t.Run("missing payload", func(t *testing.T) {
		tr := &TunnelResponse{responseType: ResponseTypeJSONRPCNotification}
		err := tr.Validate()
		require.Error(t, err)
		require.Contains(t, err.Error(), "jsonrpc notification is required")
	})
}

func TestTunnelResponseValidateOAuthDiscovery(t *testing.T) {
	t.Run("valid discovery response", func(t *testing.T) {
		tr := NewOAuthDiscoveryResponse(DefaultChannel, json.RawMessage(`{"resource":"https://example.com"}`), 200, nil)
		require.NoError(t, tr.Validate())
	})

	t.Run("missing payload", func(t *testing.T) {
		tr := &TunnelResponse{responseType: ResponseTypeOAuthDiscovery}
		err := tr.Validate()
		require.Error(t, err)
		require.Contains(t, err.Error(), "oauth discovery response is required")
	})
}

func TestTunnelResponseValidateChannel(t *testing.T) {
	tr := NewTunnelResponse(DefaultChannel, json.RawMessage(`{"jsonrpc":"2.0","id":"1","result":{}}`), 200, nil)
	require.NoError(t, tr.Validate())

	tr = NewTunnelResponse(Channel("bad channel"), json.RawMessage(`{"jsonrpc":"2.0","id":"1","result":{}}`), 200, nil)
	err := tr.Validate()
	require.Error(t, err)
	require.Contains(t, err.Error(), "invalid channel")
}
