package localproxy

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/openai/tunnel-client/pkg/controlplane/wiretypes"
	"github.com/openai/tunnel-client/pkg/types"
)

func TestHandleResponseIgnoresInvalidOptionalTiming(t *testing.T) {
	t.Parallel()

	const functionalResponse = `{"jsonrpc":"2.0","id":"tool-1","result":{"customer":900719925474099312345678901234567890}}`
	for _, testCase := range []struct {
		name       string
		timingJSON string
		wantTiming *types.ResponseTiming
	}{
		{name: "omitted"},
		{name: "null", timingJSON: `null`},
		{name: "boolean", timingJSON: `true`},
		{name: "string", timingJSON: `"invalid"`},
		{name: "array", timingJSON: `[]`},
		{name: "empty_object", timingJSON: `{}`},
		{name: "wrong_field_type", timingJSON: `{"version":1,"transport":"stdio","target_elapsed_us":"100"}`},
		{name: "integer_overflow", timingJSON: `{"version":1,"transport":"stdio","target_elapsed_us":18446744073709551616}`},
		{name: "unsupported_version", timingJSON: `{"version":2,"transport":"stdio","target_elapsed_us":100}`},
		{name: "unsupported_transport", timingJSON: `{"version":1,"transport":"future","target_elapsed_us":100}`},
		{name: "negative_duration", timingJSON: `{"version":1,"transport":"stdio","target_elapsed_us":-1}`},
		{name: "over_limit_duration", timingJSON: `{"version":1,"transport":"stdio","target_elapsed_us":86400000001}`},
		{
			name:       "valid",
			timingJSON: `{"version":1,"transport":"streamable_http","target_elapsed_us":100}`,
			wantTiming: &types.ResponseTiming{Version: 1, Transport: "streamable_http", TargetElapsedUS: 100},
		},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			t.Parallel()

			pending := &localRequest{
				id:         "local-timing-1",
				responseCh: make(chan localResponse, 1),
			}
			server := &localServer{
				stateCh:  make(chan struct{}),
				inFlight: map[string]*localRequest{pending.id: pending},
			}
			body := `{"request_id":"local-timing-1","channel":"main","resp_json":` + functionalResponse +
				`,"resp_headers":{"Content-Type":["application/json"],"X-Customer":["first","second"]},"resp_code":200,"resp_type":"jsonrpc_response"`
			if testCase.timingJSON != "" {
				body += `,"resp_timing":` + testCase.timingJSON
			}
			body += `}`
			request := httptest.NewRequest(http.MethodPost, "/v1/tunnels/local/response", strings.NewReader(body))
			request.Header.Set("X-Tunnel-Shard-Token", pending.id)
			ack := httptest.NewRecorder()

			server.handleResponse(ack, request)

			require.Equal(t, http.StatusOK, ack.Code, ack.Body.String())
			require.JSONEq(t, `{"status":"ok"}`, ack.Body.String())
			require.Empty(t, server.inFlight, "the matching terminal response must consume the in-flight request")
			select {
			case delivered := <-pending.responseCh:
				require.Equal(t, pending.id, delivered.payload.RequestID)
				require.Equal(t, "main", delivered.payload.Channel)
				require.Equal(t, wiretypes.ResponsePayloadJSONRPC, delivered.payload.ResponseType)
				require.Equal(t, http.StatusOK, delivered.payload.ResponseCode)
				require.Equal(t, json.RawMessage(functionalResponse), delivered.payload.JSONResponse)
				require.Equal(t, testCase.wantTiming, delivered.payload.ResponseTiming)

				caller := httptest.NewRecorder()
				renderMCPResponse(caller, delivered.payload, "http://localhost/v1/mcp/local")
				require.Equal(t, http.StatusOK, caller.Code)
				require.Equal(t, "application/json", caller.Header().Get("Content-Type"))
				require.Equal(t, []string{"first", "second"}, caller.Header().Values("X-Customer"))
				require.Equal(t, functionalResponse, caller.Body.String(), "optional timing must not alter the functional response bytes")
			default:
				t.Fatal("terminal response was dropped because of optional timing")
			}
		})
	}
}
