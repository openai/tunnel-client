package dispatcherinternal

import (
	"encoding/json"
	"net/http"
	"strings"
	"testing"

	"github.com/modelcontextprotocol/go-sdk/jsonrpc"
	"github.com/stretchr/testify/require"

	"github.com/openai/tunnel-client/pkg/types"
)

const traceContextTestParent = "00-4bf92f3577b34da6a3ce929d0e0e4736-00f067aa0ba902b7-01"

func TestForwardTraceContextPreservesCustomerRequest(t *testing.T) {
	t.Parallel()

	for _, testCase := range []struct {
		name    string
		headers http.Header
	}{
		{name: "stdio"},
		{
			name: "streamable_http",
			headers: http.Header{
				"Accept":         {"application/json, text/event-stream"},
				"Mcp-Session-Id": {"customer-session"},
				"Baggage":        {"customer=keep"},
			},
		},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			t.Parallel()

			req := newTraceContextTestRequest(t, `{"name":"customer_tool","arguments":{"large":900719925474099312345678901234567890,"exponent":1e10000,"duplicate":"first","duplicate":"second"},"unknown":{"keep":true},"_meta":{"progressToken":9007199254740993,"customer":{"keep":"value"},"baggage":"tenant=customer","io.modelcontextprotocol/protocolVersion":"2026-07-28","io.modelcontextprotocol/clientCapabilities":{}}}`)
			original := *req
			original.Params = append(json.RawMessage(nil), req.Params...)
			originalHeaders := testCase.headers.Clone()
			carrier := &types.TraceContext{Traceparent: traceContextTestParent, Tracestate: "vendor=one, other=two"}
			originalCarrier := *carrier

			got := forwardTraceContext(req, testCase.headers, carrier)

			require.NotSame(t, req, got)
			require.Equal(t, original, *req, "forwarding must not mutate the original request")
			require.Equal(t, originalHeaders, testCase.headers)
			require.Equal(t, originalCarrier, *carrier)
			require.Equal(t, req.ID, got.ID)
			require.Equal(t, req.Method, got.Method)
			require.Equal(t, req.Extra, got.Extra)

			before := traceContextTestObject(t, original.Params)
			after := traceContextTestObject(t, got.Params)
			require.Len(t, after, len(before))
			for key, value := range before {
				if key != "_meta" {
					require.Equal(t, value, after[key], "customer parameter %q must retain its raw value", key)
				}
			}
			beforeMeta := traceContextTestObject(t, before["_meta"])
			afterMeta := traceContextTestObject(t, after["_meta"])
			require.Len(t, afterMeta, len(beforeMeta)+2)
			for key, value := range beforeMeta {
				require.Equal(t, value, afterMeta[key], "customer metadata %q must be preserved", key)
			}
			require.Equal(t, json.RawMessage(`"`+traceContextTestParent+`"`), afterMeta["traceparent"])
			require.Equal(t, json.RawMessage(`"vendor=one,other=two"`), afterMeta["tracestate"])

			got.Params[0] = '['
			require.Equal(t, original, *req, "the forwarded parameter buffer must not alias the original")
		})
	}
}

func TestForwardTraceContextCreatesMetadata(t *testing.T) {
	t.Parallel()

	for _, testCase := range []struct {
		name   string
		params json.RawMessage
	}{
		{name: "absent_params"},
		{name: "empty_params", params: json.RawMessage{}},
		{name: "empty_object", params: json.RawMessage(`{}`)},
		{name: "empty_meta", params: json.RawMessage(`{"_meta":{}}`)},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			t.Parallel()

			req := newTraceContextTestRequest(t, string(testCase.params))
			req.Params = testCase.params
			original := *req
			got := forwardTraceContext(req, nil, &types.TraceContext{Traceparent: traceContextTestParent})

			require.NotSame(t, req, got)
			require.Equal(t, original, *req)
			params := traceContextTestObject(t, got.Params)
			require.Len(t, params, 1)
			meta := traceContextTestObject(t, params["_meta"])
			require.Equal(t, map[string]json.RawMessage{
				"traceparent": json.RawMessage(`"` + traceContextTestParent + `"`),
			}, meta, "forwarding must introduce no baggage or empty tracestate")
		})
	}
}

func TestForwardTraceContextCanonicalizesCarrier(t *testing.T) {
	t.Parallel()

	maxTraceState := "a=" + strings.Repeat("x", 253) + ",b=" + strings.Repeat("y", 254)
	require.Len(t, maxTraceState, 512)
	for _, testCase := range []struct {
		name            string
		traceparent     string
		tracestate      string
		wantTraceparent string
		wantTracestate  string
	}{
		{
			name:            "canonical_tracestate",
			traceparent:     traceContextTestParent,
			tracestate:      "vendor=one \t, other=two",
			wantTraceparent: traceContextTestParent,
			wantTracestate:  "vendor=one,other=two",
		},
		{
			name:            "maximum_tracestate",
			traceparent:     traceContextTestParent,
			tracestate:      maxTraceState,
			wantTraceparent: traceContextTestParent,
			wantTracestate:  maxTraceState,
		},
		{
			name:            "unsampled",
			traceparent:     strings.TrimSuffix(traceContextTestParent, "01") + "00",
			wantTraceparent: strings.TrimSuffix(traceContextTestParent, "01") + "00",
		},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			t.Parallel()

			req := newTraceContextTestRequest(t, `{}`)
			got := forwardTraceContext(req, nil, &types.TraceContext{
				Traceparent: testCase.traceparent,
				Tracestate:  testCase.tracestate,
			})

			require.NotSame(t, req, got)
			params := traceContextTestObject(t, got.Params)
			meta := traceContextTestObject(t, params["_meta"])
			encodedParent, err := json.Marshal(testCase.wantTraceparent)
			require.NoError(t, err)
			require.Equal(t, json.RawMessage(encodedParent), meta["traceparent"])
			if testCase.wantTracestate == "" {
				require.Len(t, meta, 1)
			} else {
				encodedState, err := json.Marshal(testCase.wantTracestate)
				require.NoError(t, err)
				require.Equal(t, json.RawMessage(encodedState), meta["tracestate"])
				require.Len(t, meta, 2)
			}
		})
	}
}

func TestForwardTraceContextSkipsInvalidCarrier(t *testing.T) {
	t.Parallel()

	oversizeState := "a=" + strings.Repeat("x", 253) + ",b=" + strings.Repeat("y", 255)
	require.Len(t, oversizeState, 513)
	for _, testCase := range []struct {
		name    string
		carrier *types.TraceContext
	}{
		{name: "nil_carrier"},
		{name: "missing_parent", carrier: &types.TraceContext{Tracestate: "vendor=one"}},
		{name: "empty_carrier", carrier: &types.TraceContext{}},
		{name: "short_parent", carrier: &types.TraceContext{Traceparent: traceContextTestParent[:54]}},
		{name: "oversize_parent", carrier: &types.TraceContext{Traceparent: traceContextTestParent + "0"}},
		{name: "oversize_parent_with_extension", carrier: &types.TraceContext{Traceparent: traceContextTestParent + "-extra"}},
		{name: "future_version", carrier: &types.TraceContext{Traceparent: "01" + traceContextTestParent[2:]}},
		{name: "invalid_hex", carrier: &types.TraceContext{Traceparent: strings.Replace(traceContextTestParent, "4bf", "gbf", 1)}},
		{name: "uppercase_parent", carrier: &types.TraceContext{Traceparent: strings.ToUpper(traceContextTestParent)}},
		{name: "invalid_flags", carrier: &types.TraceContext{Traceparent: strings.TrimSuffix(traceContextTestParent, "01") + "ff"}},
		{name: "zero_trace_id", carrier: &types.TraceContext{Traceparent: "00-" + strings.Repeat("0", 32) + "-00f067aa0ba902b7-01"}},
		{name: "zero_span_id", carrier: &types.TraceContext{Traceparent: "00-4bf92f3577b34da6a3ce929d0e0e4736-" + strings.Repeat("0", 16) + "-01"}},
		{name: "oversize_tracestate", carrier: &types.TraceContext{Traceparent: traceContextTestParent, Tracestate: oversizeState}},
		{name: "malformed_tracestate", carrier: &types.TraceContext{Traceparent: traceContextTestParent, Tracestate: "vendor"}},
		{name: "duplicate_tracestate_member", carrier: &types.TraceContext{Traceparent: traceContextTestParent, Tracestate: "vendor=one,vendor=two"}},
		{name: "invalid_tracestate_key", carrier: &types.TraceContext{Traceparent: traceContextTestParent, Tracestate: "Vendor=one"}},
		{name: "invalid_tracestate_value", carrier: &types.TraceContext{Traceparent: traceContextTestParent, Tracestate: "vendor=one=two"}},
		{name: "oversize_tracestate_value", carrier: &types.TraceContext{Traceparent: traceContextTestParent, Tracestate: "vendor=" + strings.Repeat("x", 257)}},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			t.Parallel()

			req := newTraceContextTestRequest(t, `{"name":"customer_tool","_meta":{"customer":"keep"}}`)
			original := *req
			original.Params = append(json.RawMessage(nil), req.Params...)
			got := forwardTraceContext(req, nil, testCase.carrier)

			require.Same(t, req, got, "invalid telemetry must leave the operation untouched")
			require.Equal(t, original, *req)
		})
	}

	t.Run("nil_request", func(t *testing.T) {
		t.Parallel()
		require.Nil(t, forwardTraceContext(nil, nil, &types.TraceContext{Traceparent: traceContextTestParent}))
	})
}

func TestForwardTraceContextPreservesOwnedOrUnrewritableRequests(t *testing.T) {
	t.Parallel()

	for _, testCase := range []struct {
		name    string
		params  string
		headers http.Header
	}{
		{name: "malformed_params", params: `{"name":`},
		{name: "trailing_params", params: `{} {}`},
		{name: "null_params", params: `null`},
		{name: "array_params", params: `[]`},
		{name: "string_params", params: `"customer"`},
		{name: "scalar_params", params: `7`},
		{name: "null_meta", params: `{"_meta":null}`},
		{name: "array_meta", params: `{"_meta":[]}`},
		{name: "string_meta", params: `{"_meta":"customer"}`},
		{name: "scalar_meta", params: `{"_meta":false}`},
		{name: "duplicate_parameter", params: `{"name":"first","name":"second"}`},
		{name: "duplicate_meta_parameter", params: `{"_meta":{"one":1},"_meta":{"two":2}}`},
		{name: "duplicate_metadata", params: `{"_meta":{"customer":"first","customer":"second"}}`},
		{name: "existing_parent", params: `{"_meta":{"traceparent":"customer"}}`},
		{name: "null_parent", params: `{"_meta":{"traceparent":null}}`},
		{name: "invalid_parent", params: `{"_meta":{"traceparent":false}}`},
		{name: "existing_state", params: `{"_meta":{"tracestate":"customer=keep"}}`},
		{name: "null_state", params: `{"_meta":{"tracestate":null}}`},
		{name: "invalid_state", params: `{"_meta":{"tracestate":42}}`},
		{name: "parent_header", params: `{}`, headers: http.Header{"Traceparent": {"customer"}}},
		{name: "state_header", params: `{}`, headers: http.Header{"Tracestate": {"customer=keep"}}},
		{name: "lowercase_header", params: `{}`, headers: http.Header{"traceparent": {"customer"}}},
		{name: "mixed_case_header", params: `{}`, headers: http.Header{"tRaCeStAtE": {"customer=keep"}}},
		{name: "nil_parent_header", params: `{}`, headers: http.Header{"traceparent": nil}},
		{name: "nil_state_header", params: `{}`, headers: http.Header{"tracestate": nil}},
		{name: "empty_parent_header", params: `{}`, headers: http.Header{"TRACEPARENT": {}}},
		{name: "empty_state_header", params: `{}`, headers: http.Header{"TRACESTATE": {""}}},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			t.Parallel()

			req := newTraceContextTestRequest(t, testCase.params)
			original := *req
			original.Params = append(json.RawMessage(nil), req.Params...)
			originalHeaders := testCase.headers.Clone()
			got := forwardTraceContext(req, testCase.headers, &types.TraceContext{
				Traceparent: traceContextTestParent,
				Tracestate:  "vendor=one",
			})

			require.Same(t, req, got, "optional tracing must preserve caller ownership and malformed payloads")
			require.Equal(t, original, *req)
			require.Equal(t, originalHeaders, testCase.headers)
		})
	}
}

func newTraceContextTestRequest(t *testing.T, params string) *jsonrpc.Request {
	t.Helper()
	id, err := jsonrpc.MakeID("customer-rpc")
	require.NoError(t, err)
	return &jsonrpc.Request{
		ID:     id,
		Method: "tools/call",
		Params: json.RawMessage(params),
		Extra:  "customer transport data",
	}
}

func traceContextTestObject(t *testing.T, raw json.RawMessage) map[string]json.RawMessage {
	t.Helper()
	var object map[string]json.RawMessage
	require.NoError(t, json.Unmarshal(raw, &object))
	require.NotNil(t, object)
	return object
}
