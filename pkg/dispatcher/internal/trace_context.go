package dispatcherinternal

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"strings"

	"github.com/modelcontextprotocol/go-sdk/jsonrpc"
	"go.opentelemetry.io/otel/propagation"
	"go.opentelemetry.io/otel/trace"

	"github.com/openai/tunnel-client/pkg/types"
)

// forwardTraceContext copies the MCP request and adds only the standard trace
// fields to params._meta. Callers must first check the local forwarding opt-in.
// Optional tracing never replaces customer metadata or rejects the operation.
func forwardTraceContext(req *jsonrpc.Request, headers http.Header, traceContext *types.TraceContext) *jsonrpc.Request {
	if req == nil || traceContext == nil || len(traceContext.Traceparent) != 55 ||
		!strings.HasPrefix(traceContext.Traceparent, "00-") || len(traceContext.Tracestate) > 512 {
		return req
	}
	for name := range headers {
		if strings.EqualFold(name, "traceparent") || strings.EqualFold(name, "tracestate") {
			return req
		}
	}
	if _, err := trace.ParseTraceState(traceContext.Tracestate); err != nil {
		return req
	}
	propagator := propagation.TraceContext{}
	ctx := propagator.Extract(context.Background(), propagation.MapCarrier{
		"traceparent": traceContext.Traceparent,
		"tracestate":  traceContext.Tracestate,
	})
	if !trace.SpanContextFromContext(ctx).IsValid() {
		return req
	}
	carrier := propagation.MapCarrier{}
	propagator.Inject(ctx, carrier)

	params := map[string]json.RawMessage{}
	if len(req.Params) != 0 {
		var ok bool
		params, ok = decodeTraceObject(req.Params)
		if !ok {
			return req
		}
	}
	meta := map[string]json.RawMessage{}
	if raw, exists := params["_meta"]; exists {
		var ok bool
		meta, ok = decodeTraceObject(raw)
		if !ok {
			return req
		}
	}
	if _, exists := meta["traceparent"]; exists {
		return req
	}
	if _, exists := meta["tracestate"]; exists {
		return req
	}
	for name, value := range carrier {
		encoded, err := json.Marshal(value)
		if err != nil {
			return req
		}
		meta[name] = encoded
	}
	encodedMeta, err := json.Marshal(meta)
	if err != nil {
		return req
	}
	params["_meta"] = encodedMeta
	encodedParams, err := json.Marshal(params)
	if err != nil {
		return req
	}
	copied := *req
	copied.Params = encodedParams
	return &copied
}

// Keep values raw so optional metadata cannot change customer argument numbers.
// Duplicate keys make rewriting ambiguous; leave such requests untouched.
func decodeTraceObject(raw json.RawMessage) (map[string]json.RawMessage, bool) {
	decoder := json.NewDecoder(bytes.NewReader(raw))
	first, err := decoder.Token()
	if err != nil || first != json.Delim('{') {
		return nil, false
	}
	object := map[string]json.RawMessage{}
	for decoder.More() {
		token, err := decoder.Token()
		if err != nil {
			return nil, false
		}
		name, ok := token.(string)
		if !ok {
			return nil, false
		}
		if _, exists := object[name]; exists {
			return nil, false
		}
		var value json.RawMessage
		if err := decoder.Decode(&value); err != nil {
			return nil, false
		}
		object[name] = value
	}
	last, err := decoder.Token()
	if err != nil || last != json.Delim('}') {
		return nil, false
	}
	if _, err := decoder.Token(); err != io.EOF {
		return nil, false
	}
	return object, true
}
