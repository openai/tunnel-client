package internal

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/modelcontextprotocol/go-sdk/jsonrpc"
	"github.com/stretchr/testify/require"

	"github.com/openai/tunnel-client/pkg/controlplane/wiretypes"
	"github.com/openai/tunnel-client/pkg/types"
)

func TestCommandTraceContextIsOptionalAndTolerant(t *testing.T) {
	t.Parallel()
	const parent = "00-4bf92f3577b34da6a3ce929d0e0e4736-00f067aa0ba902b7-01"
	for _, tc := range []struct {
		name  string
		value string
		want  *types.TraceContext
	}{
		{"legacy missing", "", nil},
		{"null", "null", nil},
		{"boolean", "false", nil},
		{"array", "[]", nil},
		{"wrong field type", `{"traceparent":false}`, nil},
		{"missing parent", `{"tracestate":"vendor=value"}`, nil},
		{"unbounded parent", `{"traceparent":"` + strings.Repeat("a", 56) + `"}`, nil},
		{"unbounded state", `{"traceparent":"` + parent + `","tracestate":"` + strings.Repeat("a", 513) + `"}`, nil},
		{"valid", `{"traceparent":"` + parent + `","tracestate":"vendor=value","future":"ignored"}`, &types.TraceContext{Traceparent: parent, Tracestate: "vendor=value"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			payload := `{"request_id":"request","shard_token":"token","command_type":"jsonrpc","channel":"main","jsonrpc":{"jsonrpc":"2.0","id":1,"method":"tools/list"}`
			if tc.value != "" {
				payload += `,"trace_context":` + tc.value
			}
			payload += "}"
			var raw wiretypes.RawJSONRPCPolledCommand
			require.NoError(t, json.Unmarshal([]byte(payload), &raw))
			command, err := convertRawCommand(raw, time.Now())
			require.NoError(t, err)
			require.Equal(t, tc.want, command.TraceContext())
			if carrier := command.TraceContext(); carrier != nil {
				carrier.Traceparent = "changed"
				require.Equal(t, tc.want, command.TraceContext())
			}
			raw.JSONRPC = json.RawMessage(`false`)
			_, err = convertRawCommand(raw, time.Now())
			require.Error(t, err, "optional metadata must not weaken functional validation")
		})
	}
}

func TestCommandTraceContextRespectsRawHeaderOwnership(t *testing.T) {
	t.Parallel()
	const parent = "00-4bf92f3577b34da6a3ce929d0e0e4736-00f067aa0ba902b7-01"
	const params = `{"name":"customer","arguments":{"number":90071992547409931234567890},"_meta":{"customer":"keep"}}`
	for _, tc := range []struct {
		name    string
		headers string
		owned   bool
	}{
		{name: "no owner", headers: `{}`},
		{name: "parent null", headers: `{"TrAcEpArEnT":null}`, owned: true},
		{name: "parent empty", headers: `{"TrAcEpArEnT":[]}`, owned: true},
		{name: "state null", headers: `{"TrAcEsTaTe":null}`, owned: true},
		{name: "state empty", headers: `{"TrAcEsTaTe":[]}`, owned: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			payload := `{"request_id":"request","shard_token":"token","command_type":"jsonrpc","channel":"main","headers":` + tc.headers +
				`,"trace_context":{"traceparent":"` + parent + `"},"jsonrpc":{"jsonrpc":"2.0","id":"rpc-owner","method":"tools/call","params":` + params + `}}`
			var raw wiretypes.RawJSONRPCPolledCommand
			require.NoError(t, json.Unmarshal([]byte(payload), &raw))
			command, err := convertRawCommand(raw, time.Now())
			require.NoError(t, err)
			if tc.owned {
				require.Nil(t, command.TraceContext())
			} else {
				require.Equal(t, &types.TraceContext{Traceparent: parent}, command.TraceContext())
			}
			require.Equal(t, types.RequestID("request"), command.RequestID())
			require.Equal(t, "token", command.ShardToken())
			require.Equal(t, types.DefaultChannel, command.Channel())
			require.Empty(t, command.Headers(), "legacy empty-header normalization must remain unchanged")
			request, ok := command.Message().(*jsonrpc.Request)
			require.True(t, ok)
			require.Equal(t, "rpc-owner", request.ID.Raw())
			require.Equal(t, "tools/call", request.Method)
			require.Equal(t, json.RawMessage(params), request.Params)
		})
	}
}
