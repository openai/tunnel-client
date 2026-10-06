package internal

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strconv"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/openai/tunnel-client/pkg/types"
)

func TestPostResponseTimingRemainsFrozenAcrossRetries(t *testing.T) {
	t.Parallel()
	for _, elapsed := range []int64{0, 123456} {
		t.Run(strconv.FormatInt(elapsed, 10), func(t *testing.T) {
			t.Parallel()
			client := newResponseRetryTestClient(t)
			timing := types.ResponseTiming{Version: 1, Transport: "streamable_http", TargetElapsedUS: elapsed}
			response := types.NewTunnelResponse(types.DefaultChannel,
				json.RawMessage(`{"jsonrpc":"2.0","id":1,"result":{}}`), http.StatusOK, nil).WithTiming(timing)
			require.NotNil(t, response.Timing())
			var attempts []capturedRequest
			client.client.Transport = roundTripFunc(func(req *http.Request) (*http.Response, error) {
				attempts = append(attempts, captureRequest(t, req))
				switch len(attempts) {
				case 1:
					return nil, errors.New("temporary transport failure")
				case 2:
					return testHTTPResponse(req, http.StatusServiceUnavailable, nil), nil
				default:
					return testHTTPResponse(req, http.StatusOK, nil), nil
				}
			})
			client.retrySleep = func(context.Context, time.Duration) bool {
				// Mutating a caller's copy cannot change subsequent attempts.
				response.Timing().TargetElapsedUS++
				return true
			}
			_, err := client.PostResponse(responseContext(context.Background(), "shard-timing", ""), types.RequestID("request-timing"), response)
			require.NoError(t, err)
			require.Len(t, attempts, 3)
			for _, attempt := range attempts {
				require.Equal(t, attempts[0].body, attempt.body)
				var payload struct {
					Timing       *types.ResponseTiming `json:"resp_timing"`
					JSONResponse json.RawMessage       `json:"resp_json"`
				}
				require.NoError(t, json.Unmarshal([]byte(attempt.body), &payload))
				require.Equal(t, &timing, payload.Timing)
				require.JSONEq(t, string(response.Payload()), string(payload.JSONResponse))
			}
		})
	}
}

func TestPostLegacyResponseOmitsTiming(t *testing.T) {
	t.Parallel()
	client := newResponseRetryTestClient(t)
	client.client.Transport = roundTripFunc(func(req *http.Request) (*http.Response, error) {
		attempt := captureRequest(t, req)
		var payload map[string]json.RawMessage
		require.NoError(t, json.Unmarshal([]byte(attempt.body), &payload))
		require.NotContains(t, payload, "resp_timing")
		return testHTTPResponse(req, http.StatusOK, nil), nil
	})
	response := types.NewTunnelResponse(types.DefaultChannel,
		json.RawMessage(`{"jsonrpc":"2.0","id":1,"result":{}}`), http.StatusOK, nil)
	_, err := client.PostResponse(responseContext(context.Background(), "shard-legacy", ""), types.RequestID("request-legacy"), response)
	require.NoError(t, err)
}
