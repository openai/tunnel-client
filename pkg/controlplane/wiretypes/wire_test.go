package wiretypes

import (
	"encoding/json"
	"net/http"
	"os"
	"strconv"
	"strings"
	"testing"
	"time"
)

func TestRawJSONRPCPolledCommandMarshalFieldNames(t *testing.T) {
	createdAt := time.Date(2024, time.September, 4, 12, 30, 0, 0, time.UTC)
	cmd := RawJSONRPCPolledCommand{
		BaseRawPolledCommand: BaseRawPolledCommand{
			RequestID:   "req-123",
			ShardToken:  "shard-456",
			CommandType: CommandTypeJSONRPC,
			Channel:     "harpoon",
			CreatedAt:   createdAt,
			Headers: http.Header{
				"X-Trace-ID": []string{"trace-789"},
			},
		},
		JSONRPC: json.RawMessage(`{"jsonrpc":"2.0","id":"rpc-99","method":"tools/list","params":{"needle":"hay"}}`),
	}

	payload, err := json.Marshal(cmd)
	if err != nil {
		t.Fatalf("marshal raw jsonrpc polled command: %v", err)
	}

	var got map[string]any
	if err := json.Unmarshal(payload, &got); err != nil {
		t.Fatalf("unmarshal marshaled payload: %v", err)
	}

	if got["request_id"] != "req-123" {
		t.Fatalf("expected request_id to be req-123, got %v", got["request_id"])
	}
	if got["shard_token"] != "shard-456" {
		t.Fatalf("expected shard_token to be shard-456, got %v", got["shard_token"])
	}
	if got["command_type"] != string(CommandTypeJSONRPC) {
		t.Fatalf("expected command_type to be %q, got %v", CommandTypeJSONRPC, got["command_type"])
	}
	if got["channel"] != "harpoon" {
		t.Fatalf("expected channel to be harpoon, got %v", got["channel"])
	}
	if _, ok := got["created_at"]; !ok {
		t.Fatalf("expected created_at to be present")
	}
	if _, ok := got["jsonrpc"]; !ok {
		t.Fatalf("expected jsonrpc to be present")
	}

	headersValue, ok := got["headers"].(map[string]any)
	if !ok {
		t.Fatalf("expected headers to be a map, got %T", got["headers"])
	}
	traceHeaders, ok := headersValue["X-Trace-ID"].([]any)
	if !ok || len(traceHeaders) != 1 || traceHeaders[0] != "trace-789" {
		t.Fatalf("expected headers to include X-Trace-ID trace-789, got %v", headersValue)
	}
}

func TestResponseTimeoutIsOptionalDurationString(t *testing.T) {
	responseTimeout := ResponseTimeoutDuration("30s")
	command := RawJSONRPCPolledCommand{
		BaseRawPolledCommand: BaseRawPolledCommand{
			RequestID:       "req-timeout",
			ShardToken:      "shard-timeout",
			CommandType:     CommandTypeJSONRPC,
			ResponseTimeout: &responseTimeout,
		},
		JSONRPC: json.RawMessage(`{"jsonrpc":"2.0","id":"rpc-1","method":"tools/list"}`),
	}

	payload, err := json.Marshal(command)
	if err != nil {
		t.Fatalf("marshal command: %v", err)
	}
	if !strings.Contains(string(payload), `"response_timeout":"30s"`) {
		t.Fatalf("response timeout must be encoded as a duration string, got %s", payload)
	}

	command.ResponseTimeout = nil
	payload, err = json.Marshal(command)
	if err != nil {
		t.Fatalf("marshal command without response timeout: %v", err)
	}
	if strings.Contains(string(payload), "response_timeout") {
		t.Fatalf("optional response timeout must be omitted when absent, got %s", payload)
	}
}

func TestResponseTimeoutParsesIntegerValueAndSingleUnit(t *testing.T) {
	tests := []struct {
		wire string
		want time.Duration
	}{
		{wire: "30s", want: 30 * time.Second},
		{wire: "4500ms", want: 4500 * time.Millisecond},
		{wire: "1ns", want: time.Nanosecond},
		{wire: "1us", want: time.Microsecond},
		{wire: "1ms", want: time.Millisecond},
		{wire: "2m", want: 2 * time.Minute},
		{wire: "3h", want: 3 * time.Hour},
		{wire: "0s", want: 0},
		{wire: "0h", want: 0},
		{wire: "9223372036854775807ns", want: time.Duration(1<<63 - 1)},
	}
	for _, tc := range tests {
		t.Run(tc.wire, func(t *testing.T) {
			var command RawJSONRPCPolledCommand
			payload := `{
				"request_id":"req-timeout",
				"shard_token":"shard-timeout",
				"command_type":"jsonrpc",
				"response_timeout":` + strconv.Quote(tc.wire) + `,
				"jsonrpc":{"jsonrpc":"2.0","id":"rpc-1","method":"tools/list"}
			}`
			if err := json.Unmarshal([]byte(payload), &command); err != nil {
				t.Fatalf("decode response timeout: %v", err)
			}
			if command.ResponseTimeout == nil {
				t.Fatal("response timeout was not decoded")
			}
			got, ok := command.ResponseTimeout.Value()
			if !ok || got != tc.want {
				t.Fatalf("response timeout = %v, %v; want %v, true", got, ok, tc.want)
			}
		})
	}
}

func TestInvalidResponseTimeoutDoesNotRejectCommand(t *testing.T) {
	tests := []struct {
		name string
		wire string
	}{
		{name: "malformed", wire: `"not-a-duration"`},
		{name: "number", wire: `30`},
		{name: "boolean", wire: `true`},
		{name: "object", wire: `{}`},
		{name: "array", wire: `[]`},
		{name: "negative", wire: `"-1s"`},
		{name: "negative zero", wire: `"-0s"`},
		{name: "positive sign", wire: `"+1s"`},
		{name: "unitless zero", wire: `"0"`},
		{name: "fractional", wire: `"4.5s"`},
		{name: "compound", wire: `"1m30s"`},
		{name: "leading whitespace", wire: `" 1s"`},
		{name: "trailing whitespace", wire: `"1s "`},
		{name: "trailing newline", wire: `"1s\n"`},
		{name: "exponent", wire: `"1e3s"`},
		{name: "unknown unit", wire: `"1d"`},
		{name: "overflow", wire: `"9223372036854775808ns"`},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			var command RawJSONRPCPolledCommand
			payload := `{
				"request_id":"req-invalid-timeout",
				"shard_token":"shard-invalid-timeout",
				"command_type":"jsonrpc",
				"response_timeout":` + tc.wire + `,
				"jsonrpc":{"jsonrpc":"2.0","id":"rpc-1","method":"tools/list"}
			}`
			if err := json.Unmarshal([]byte(payload), &command); err != nil {
				t.Fatalf("optional invalid response timeout must not reject command: %v", err)
			}
			if command.ResponseTimeout == nil {
				t.Fatal("present invalid response timeout should retain an invalid sentinel")
			}
			if got, ok := command.ResponseTimeout.Value(); ok || got != 0 {
				t.Fatalf("invalid response timeout should be ignored, got %v, %v", got, ok)
			}
		})
	}
}

func TestMissingOrNullResponseTimeoutAndUnknownFieldsAreCompatible(t *testing.T) {
	fixture := []byte(`{"commands":[
		{
			"request_id":"req-compatible-rpc",
			"shard_token":"shard-compatible-rpc",
			"command_type":"jsonrpc",
			"future_field":{"nested":true},
			"jsonrpc":{"jsonrpc":"2.0","id":"rpc-1","method":"tools/list"}
		},
		{
			"request_id":"req-compatible-termination",
			"shard_token":"shard-compatible-termination",
			"command_type":"session_termination",
			"response_timeout":null,
			"future_field":["future"],
			"headers":{"Mcp-Session-Id":["session-1"]}
		}
	]}`)

	var envelope PolledCommandEnvelope
	if err := json.Unmarshal(fixture, &envelope); err != nil {
		t.Fatalf("decode compatible command envelope: %v", err)
	}
	if len(envelope.Commands) != 2 {
		t.Fatalf("compatible command count = %d, want 2", len(envelope.Commands))
	}

	var rpc RawJSONRPCPolledCommand
	if err := json.Unmarshal(envelope.Commands[0], &rpc); err != nil {
		t.Fatalf("missing timeout or unknown JSON-RPC field must not reject command: %v", err)
	}
	if rpc.ResponseTimeout != nil {
		t.Fatalf("missing response timeout should remain absent, got %v", rpc.ResponseTimeout)
	}
	if rpc.RequestID != "req-compatible-rpc" || rpc.CommandType != CommandTypeJSONRPC || len(rpc.JSONRPC) == 0 {
		t.Fatalf("unknown JSON-RPC field changed established fields: %#v", rpc)
	}

	var termination RawSessionTerminationPolledCommand
	if err := json.Unmarshal(envelope.Commands[1], &termination); err != nil {
		t.Fatalf("null timeout or unknown session-termination field must not reject command: %v", err)
	}
	if termination.ResponseTimeout != nil {
		t.Fatalf("null response timeout should use legacy behavior, got %v", termination.ResponseTimeout)
	}
	if termination.RequestID != "req-compatible-termination" || termination.CommandType != CommandTypeSessionTermination {
		t.Fatalf("unknown session-termination field changed established fields: %#v", termination)
	}
}

func TestLegacyOfficialDecoderIgnoresResponseTimeout(t *testing.T) {
	// These types freeze the wire shape used by legacy official Go tunnel-clients.
	// Keep them independent of current wire types so the test continues to exercise
	// the legacy decoder.
	type legacyCommandType string
	const (
		legacyJSONRPC            legacyCommandType = "jsonrpc"
		legacySessionTermination legacyCommandType = "session_termination"
	)
	type legacyBaseRawPolledCommand struct {
		RequestID   string            `json:"request_id"`
		ShardToken  string            `json:"shard_token"`
		CommandType legacyCommandType `json:"command_type"`
		Channel     string            `json:"channel,omitempty"`
		CreatedAt   time.Time         `json:"created_at"`
		Headers     http.Header       `json:"headers"`
	}
	type legacyRawJSONRPCPolledCommand struct {
		legacyBaseRawPolledCommand
		JSONRPC json.RawMessage `json:"jsonrpc"`
	}
	type legacyRawSessionTerminationPolledCommand struct {
		legacyBaseRawPolledCommand
	}
	type legacyPolledCommandEnvelope struct {
		Commands []json.RawMessage `json:"commands"`
	}

	fixture := []byte(`{"commands":[
		{
			"request_id":"req-new-service-old-client-rpc",
			"shard_token":"shard-new-service-old-client-rpc",
			"command_type":"jsonrpc",
			"channel":"main",
			"created_at":"2026-07-10T12:00:00Z",
			"headers":{"X-Trace-Id":["trace-rpc"]},
			"response_timeout":"30s",
			"jsonrpc":{"jsonrpc":"2.0","id":"rpc-1","method":"tools/list"}
		},
		{
			"request_id":"req-new-service-old-client-termination",
			"shard_token":"shard-new-service-old-client-termination",
			"command_type":"session_termination",
			"channel":"main",
			"created_at":"2026-07-10T12:00:01Z",
			"headers":{"Mcp-Session-Id":["session-legacy"]},
			"response_timeout":"30s"
		}
	]}`)

	var envelope legacyPolledCommandEnvelope
	if err := json.Unmarshal(fixture, &envelope); err != nil {
		t.Fatalf("legacy official envelope decoder failed: %v", err)
	}
	if len(envelope.Commands) != 2 {
		t.Fatalf("legacy official command count = %d, want 2", len(envelope.Commands))
	}

	seen := map[legacyCommandType]bool{}
	for _, raw := range envelope.Commands {
		var probe struct {
			CommandType legacyCommandType `json:"command_type"`
		}
		if err := json.Unmarshal(raw, &probe); err != nil {
			t.Fatalf("legacy official discriminator decoder failed: %v", err)
		}

		switch probe.CommandType {
		case legacyJSONRPC:
			var command legacyRawJSONRPCPolledCommand
			if err := json.Unmarshal(raw, &command); err != nil {
				t.Fatalf("legacy official JSON-RPC decoder must ignore response_timeout: %v", err)
			}
			if command.RequestID != "req-new-service-old-client-rpc" {
				t.Fatalf("legacy official JSON-RPC request_id = %q", command.RequestID)
			}
			if command.ShardToken != "shard-new-service-old-client-rpc" {
				t.Fatalf("legacy official JSON-RPC shard_token = %q", command.ShardToken)
			}
			if command.CommandType != legacyJSONRPC || command.Channel != "main" {
				t.Fatalf("legacy official JSON-RPC discriminator/channel = %q/%q", command.CommandType, command.Channel)
			}
			if command.CreatedAt.IsZero() || command.Headers.Get("X-Trace-Id") != "trace-rpc" || len(command.JSONRPC) == 0 {
				t.Fatalf("legacy official JSON-RPC decoder dropped payload metadata: %#v", command)
			}
		case legacySessionTermination:
			var command legacyRawSessionTerminationPolledCommand
			if err := json.Unmarshal(raw, &command); err != nil {
				t.Fatalf("legacy official session-termination decoder must ignore response_timeout: %v", err)
			}
			if command.RequestID != "req-new-service-old-client-termination" {
				t.Fatalf("legacy official session-termination request_id = %q", command.RequestID)
			}
			if command.ShardToken != "shard-new-service-old-client-termination" {
				t.Fatalf("legacy official session-termination shard_token = %q", command.ShardToken)
			}
			if command.CommandType != legacySessionTermination || command.Channel != "main" {
				t.Fatalf("legacy official session-termination discriminator/channel = %q/%q", command.CommandType, command.Channel)
			}
			if command.CreatedAt.IsZero() || command.Headers.Get("Mcp-Session-Id") != "session-legacy" {
				t.Fatalf("legacy official session-termination decoder dropped payload metadata: %#v", command)
			}
		default:
			t.Fatalf("unexpected legacy official command type %q", probe.CommandType)
		}
		seen[probe.CommandType] = true
	}
	if !seen[legacyJSONRPC] || !seen[legacySessionTermination] {
		t.Fatalf("legacy official decoder did not cover both command types: %v", seen)
	}
}

func TestTunnelResponsePayloadOmitemptyAndHeaders(t *testing.T) {
	payload := TunnelResponsePayload{RequestID: "req-omit"}
	marshaled, err := json.Marshal(payload)
	if err != nil {
		t.Fatalf("marshal payload: %v", err)
	}

	var got map[string]any
	if err := json.Unmarshal(marshaled, &got); err != nil {
		t.Fatalf("unmarshal payload: %v", err)
	}

	if len(got) != 1 || got["request_id"] != "req-omit" {
		t.Fatalf("expected only request_id to be present, got %v", got)
	}

	payloadWithHeaders := TunnelResponsePayload{
		RequestID: "req-headers",
		ResponseHeaders: http.Header{
			"X-Resp-ID": []string{"resp-abc"},
		},
	}
	marshaledWithHeaders, err := json.Marshal(payloadWithHeaders)
	if err != nil {
		t.Fatalf("marshal payload with headers: %v", err)
	}

	var gotWithHeaders map[string]any
	if err := json.Unmarshal(marshaledWithHeaders, &gotWithHeaders); err != nil {
		t.Fatalf("unmarshal payload with headers: %v", err)
	}

	headersValue, ok := gotWithHeaders["resp_headers"].(map[string]any)
	if !ok {
		t.Fatalf("expected resp_headers to be a map, got %T", gotWithHeaders["resp_headers"])
	}
	respHeaders, ok := headersValue["X-Resp-ID"].([]any)
	if !ok || len(respHeaders) != 1 || respHeaders[0] != "resp-abc" {
		t.Fatalf("expected resp_headers to include X-Resp-ID resp-abc, got %v", headersValue)
	}
}

func TestTunnelResponsePayloadJSONRPCNotifyType(t *testing.T) {
	payload := TunnelResponsePayload{
		RequestID:    "req-notify",
		JSONResponse: json.RawMessage(`{"jsonrpc":"2.0","method":"notify","params":{"state":"ready"}}`),
		ResponseCode: http.StatusOK,
		ResponseType: ResponsePayloadJSONRPCNotify,
	}

	marshaled, err := json.Marshal(payload)
	if err != nil {
		t.Fatalf("marshal payload: %v", err)
	}

	var got map[string]any
	if err := json.Unmarshal(marshaled, &got); err != nil {
		t.Fatalf("unmarshal payload: %v", err)
	}

	if got["resp_type"] != string(ResponsePayloadJSONRPCNotify) {
		t.Fatalf("expected resp_type to be %q, got %v", ResponsePayloadJSONRPCNotify, got["resp_type"])
	}
	if got["resp_code"] != float64(http.StatusOK) {
		t.Fatalf("expected resp_code %d, got %v", http.StatusOK, got["resp_code"])
	}
	if _, ok := got["resp_json"]; !ok {
		t.Fatalf("expected resp_json to be present")
	}
}

func TestTunnelResponsePayloadSessionTerminationType(t *testing.T) {
	payload := TunnelResponsePayload{
		RequestID:    "req-terminate",
		ResponseCode: http.StatusNoContent,
		ResponseType: ResponsePayloadSessionTermination,
	}

	marshaled, err := json.Marshal(payload)
	if err != nil {
		t.Fatalf("marshal payload: %v", err)
	}

	var got map[string]any
	if err := json.Unmarshal(marshaled, &got); err != nil {
		t.Fatalf("unmarshal payload: %v", err)
	}
	if got["resp_type"] != string(ResponsePayloadSessionTermination) {
		t.Fatalf("expected resp_type %q, got %v", ResponsePayloadSessionTermination, got["resp_type"])
	}
}

func TestPolledCommandEnvelopeUnmarshalCommands(t *testing.T) {
	fixture := []byte(`{"commands":[{"request_id":"req-777","shard_token":"shard-888","command_type":"jsonrpc","channel":"harpoon","created_at":"2024-10-11T12:13:14Z","headers":{"X-Test":["alpha"]},"jsonrpc":{"jsonrpc":"2.0","id":"rpc-1","method":"tools/list","params":{"needle":"hay"}}},{"request_id":"req-888","shard_token":"shard-999","command_type":"oauth_discovery","channel":"main","created_at":"2024-10-11T12:14:15Z","headers":{}}]}`)

	var envelope PolledCommandEnvelope
	if err := json.Unmarshal(fixture, &envelope); err != nil {
		t.Fatalf("unmarshal envelope: %v", err)
	}

	if len(envelope.Commands) != 2 {
		t.Fatalf("expected 2 commands, got %d", len(envelope.Commands))
	}

	var first map[string]any
	if err := json.Unmarshal(envelope.Commands[0], &first); err != nil {
		t.Fatalf("unmarshal first command: %v", err)
	}
	if first["request_id"] != "req-777" {
		t.Fatalf("expected first request_id to be req-777, got %v", first["request_id"])
	}
	if first["command_type"] != string(CommandTypeJSONRPC) {
		t.Fatalf("expected first command_type to be %q, got %v", CommandTypeJSONRPC, first["command_type"])
	}

	var second map[string]any
	if err := json.Unmarshal(envelope.Commands[1], &second); err != nil {
		t.Fatalf("unmarshal second command: %v", err)
	}
	if second["request_id"] != "req-888" {
		t.Fatalf("expected second request_id to be req-888, got %v", second["request_id"])
	}
	if second["command_type"] != string(CommandTypeOAuthDiscovery) {
		t.Fatalf("expected second command_type to be %q, got %v", CommandTypeOAuthDiscovery, second["command_type"])
	}
}

func TestSharedPollCommandFixtureMatchesGoWireTypes(t *testing.T) {
	fixture := readWireFixture(t, "poll_command_envelope.json")

	var envelope PolledCommandEnvelope
	if err := json.Unmarshal(fixture, &envelope); err != nil {
		t.Fatalf("unmarshal envelope fixture: %v", err)
	}
	if len(envelope.Commands) != 2 {
		t.Fatalf("expected 2 commands, got %d", len(envelope.Commands))
	}

	var second map[string]any
	if err := json.Unmarshal(envelope.Commands[1], &second); err != nil {
		t.Fatalf("unmarshal session termination command: %v", err)
	}
	if second["command_type"] != string(CommandTypeSessionTermination) {
		t.Fatalf("expected session command_type %q, got %v", CommandTypeSessionTermination, second["command_type"])
	}
}

func readWireFixture(t *testing.T, name string) []byte {
	t.Helper()
	paths := []string{
		"api/tunnel-client/pkg/controlplane/wiretypes/testdata/wire/" + name,
		"testdata/wire/" + name,
	}
	for _, path := range paths {
		fixture, err := os.ReadFile(path)
		if err == nil {
			return fixture
		}
	}
	t.Fatalf("wire fixture %s not found in %v", name, paths)
	return nil
}
