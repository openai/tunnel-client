package wiretypes

import (
	"encoding/json"
	"net/http"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/openai/tunnel-client/pkg/types"
)

func TestTunnelResponsePayloadToleratesOptionalTiming(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name   string
		timing string
		want   *types.ResponseTiming
	}{
		{name: "absent"},
		{name: "null", timing: `null`},
		{name: "boolean", timing: `true`},
		{name: "string", timing: `"timing"`},
		{name: "array", timing: `[]`},
		{name: "empty object", timing: `{}`},
		{name: "missing version", timing: `{"transport":"stdio","target_elapsed_us":0}`},
		{name: "missing transport", timing: `{"version":1,"target_elapsed_us":0}`},
		{name: "missing elapsed", timing: `{"version":1,"transport":"stdio"}`},
		{name: "null version", timing: `{"version":null,"transport":"stdio","target_elapsed_us":0}`},
		{name: "null transport", timing: `{"version":1,"transport":null,"target_elapsed_us":0}`},
		{name: "null elapsed", timing: `{"version":1,"transport":"stdio","target_elapsed_us":null}`},
		{name: "boolean version", timing: `{"version":true,"transport":"stdio","target_elapsed_us":0}`},
		{name: "numeric transport", timing: `{"version":1,"transport":123,"target_elapsed_us":0}`},
		{name: "string elapsed", timing: `{"version":1,"transport":"stdio","target_elapsed_us":"0"}`},
		{name: "boolean elapsed", timing: `{"version":1,"transport":"stdio","target_elapsed_us":false}`},
		{name: "fractional elapsed", timing: `{"version":1,"transport":"stdio","target_elapsed_us":0.5}`},
		{name: "overflow elapsed", timing: `{"version":1,"transport":"stdio","target_elapsed_us":9223372036854775808}`},
		{name: "float overflow elapsed", timing: `{"version":1,"transport":"stdio","target_elapsed_us":1e10000}`},
		{name: "negative elapsed", timing: `{"version":1,"transport":"stdio","target_elapsed_us":-1}`},
		{name: "over maximum", timing: `{"version":1,"transport":"stdio","target_elapsed_us":86400000001}`},
		{name: "unsupported version", timing: `{"version":2,"transport":"stdio","target_elapsed_us":0}`},
		{name: "unknown transport", timing: `{"version":1,"transport":"inmemory","target_elapsed_us":0}`},
		{name: "zero", timing: `{"version":1,"transport":"stdio","target_elapsed_us":0}`, want: &types.ResponseTiming{Version: 1, Transport: "stdio"}},
		{name: "maximum", timing: `{"version":1,"transport":"streamable_http","target_elapsed_us":86400000000}`, want: &types.ResponseTiming{Version: 1, Transport: "streamable_http", TargetElapsedUS: types.MaxTargetElapsedUS}},
		{name: "unknown overflow field", timing: `{"version":1,"transport":"stdio","target_elapsed_us":7,"extra":1e10000}`, want: &types.ResponseTiming{Version: 1, Transport: "stdio", TargetElapsedUS: 7}},
		{name: "unknown nested field", timing: `{"version":1,"transport":"stdio","target_elapsed_us":7,"extra":` + strings.Repeat(`[`, 128) + `1e10000` + strings.Repeat(`]`, 128) + `}`, want: &types.ResponseTiming{Version: 1, Transport: "stdio", TargetElapsedUS: 7}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			const response = `{"jsonrpc":"2.0","id":"rpc-1","result":{"ok":true}}`
			data := `{"request_id":"req-1","channel":"main","resp_json":` + response + `,"resp_headers":{"X-Original":["keep"]},"resp_code":200,"resp_type":"jsonrpc_response"`
			if tc.timing != "" {
				data += `,"resp_timing":` + tc.timing
			}
			data += `}`
			payload := TunnelResponsePayload{ResponseTiming: &types.ResponseTiming{Version: 1, Transport: "stdio", TargetElapsedUS: 99}}
			require.NoError(t, json.Unmarshal([]byte(data), &payload))
			require.Equal(t, "req-1", payload.RequestID)
			require.Equal(t, "main", payload.Channel)
			require.Equal(t, response, string(payload.JSONResponse))
			require.Equal(t, http.Header{"X-Original": {"keep"}}, payload.ResponseHeaders)
			require.Equal(t, http.StatusOK, payload.ResponseCode)
			require.Equal(t, ResponsePayloadJSONRPC, payload.ResponseType)
			require.Equal(t, tc.want, payload.ResponseTiming)
			encoded, err := json.Marshal(payload)
			require.NoError(t, err)
			if tc.want == nil {
				require.NotContains(t, string(encoded), `"resp_timing"`)
			} else {
				require.Contains(t, string(encoded), `"resp_timing"`)
			}
		})
	}
}

func TestTunnelResponsePayloadStillRejectsInvalidResponses(t *testing.T) {
	t.Parallel()
	for _, data := range []string{
		`{"request_id":"req-1","resp_timing":`,
		`{"request_id":"req-1","resp_timing":{"version":1,}}`,
		`{"request_id":"req-1"} trailing`,
		`{"request_id":true,"resp_timing":false}`,
		`{"request_id":"req-1","channel":[],"resp_timing":false}`,
		`{"request_id":"req-1","resp_headers":false,"resp_timing":false}`,
		`{"request_id":"req-1","resp_code":"200","resp_timing":false}`,
		`{"request_id":"req-1","resp_type":true,"resp_timing":false}`,
	} {
		t.Run(data, func(t *testing.T) {
			t.Parallel()
			var payload TunnelResponsePayload
			require.Error(t, json.Unmarshal([]byte(data), &payload))
		})
	}
}
