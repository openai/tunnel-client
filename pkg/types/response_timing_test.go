package types

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestTunnelResponseTiming(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name   string
		timing ResponseTiming
		valid  bool
	}{
		{"zero", ResponseTiming{1, "stdio", 0}, true},
		{"http", ResponseTiming{1, "streamable_http", 12345}, true},
		{"maximum", ResponseTiming{1, "stdio", MaxTargetElapsedUS}, true},
		{"negative", ResponseTiming{1, "stdio", -1}, false},
		{"outlier", ResponseTiming{1, "stdio", MaxTargetElapsedUS + 1}, false},
		{"unsupported version", ResponseTiming{2, "stdio", 1}, false},
		{"unknown transport", ResponseTiming{1, "inmemory", 1}, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			original := NewTunnelResponse(DefaultChannel, json.RawMessage(`{"jsonrpc":"2.0","id":1,"result":{}}`), 200, nil)
			response := original.WithTiming(tc.timing)
			require.NoError(t, response.Validate())
			require.Nil(t, original.Timing())
			require.Equal(t, original.Payload(), response.Payload())
			if !tc.valid {
				require.Nil(t, response.Timing())
				return
			}
			require.Equal(t, &tc.timing, response.Timing())
			copy := response.Timing()
			copy.TargetElapsedUS++
			require.Equal(t, &tc.timing, response.Timing(), "callers cannot mutate frozen timing")
		})
	}
}

func TestNonTerminalResponsesOmitTiming(t *testing.T) {
	t.Parallel()
	for _, response := range []*TunnelResponse{
		NewJSONRPCNotification(DefaultChannel, json.RawMessage(`{"jsonrpc":"2.0","method":"notifications/progress"}`), 200, nil),
		NewNotificationAck(DefaultChannel, 202, nil),
		NewSessionTerminationResponse(DefaultChannel, 204, nil),
		NewOAuthDiscoveryResponse(DefaultChannel, json.RawMessage(`{}`), 200, nil),
	} {
		require.Nil(t, response.WithTiming(ResponseTiming{1, "stdio", 123}).Timing())
		require.NoError(t, response.Validate())
	}
}
