package dispatcherinternal

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"testing"
	"time"

	"github.com/modelcontextprotocol/go-sdk/jsonrpc"
	"github.com/stretchr/testify/require"

	"github.com/openai/tunnel-client/pkg/controlplane"
	"github.com/openai/tunnel-client/pkg/mcpclient"
	"github.com/openai/tunnel-client/pkg/runtimeconfig"
	"github.com/openai/tunnel-client/pkg/types"
)

func TestProcessorTraceContextRequiresLocalOptIn(t *testing.T) {
	t.Parallel()

	for _, tc := range []struct {
		name        string
		optIn       *bool
		kind        runtimeconfig.MCPTransportKind
		legacy      bool
		static      map[string]string
		headers     http.Header
		wantForward bool
	}{
		{name: "default_off", kind: runtimeconfig.MCPTransportHTTPStreamable},
		{name: "explicit_false", optIn: new(false), kind: runtimeconfig.MCPTransportHTTPStreamable},
		{name: "enabled_stdio", optIn: new(true), kind: runtimeconfig.MCPTransportStdio, wantForward: true},
		{name: "enabled_http", optIn: new(true), kind: runtimeconfig.MCPTransportHTTPStreamable, wantForward: true},
		{name: "legacy_command", optIn: new(true), kind: runtimeconfig.MCPTransportHTTPStreamable, legacy: true},
		{name: "in_memory", optIn: new(true), kind: runtimeconfig.MCPTransportInMemory},
		{name: "static_parent", optIn: new(true), kind: runtimeconfig.MCPTransportHTTPStreamable, static: map[string]string{"tRaCePaReNt": "customer"}},
		{name: "static_empty_state", optIn: new(true), kind: runtimeconfig.MCPTransportHTTPStreamable, static: map[string]string{"tracestate": ""}},
		{name: "polled_parent", optIn: new(true), kind: runtimeconfig.MCPTransportHTTPStreamable, headers: http.Header{"traceparent": {"customer"}}},
		{name: "polled_empty_state", optIn: new(true), kind: runtimeconfig.MCPTransportHTTPStreamable, headers: http.Header{"tRaCeStAtE": nil}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			req := newTraceContextTestRequest(t, `{"name":"customer_tool","_meta":{"customer":"keep"}}`)
			originalParams := append(json.RawMessage(nil), req.Params...)
			conn := &processorTraceConnection{stubForwardingConnection: &stubForwardingConnection{
				statusCode: http.StatusOK,
				response:   &jsonrpc.Response{ID: req.ID, Result: json.RawMessage(`{}`)},
			}}
			transport := &stubForwardingTransport{conn: conn}
			bindings := newTestChannelBindings(transport)
			binding := bindings[types.DefaultChannel]
			binding.TransportKind = tc.kind
			bindings[types.DefaultChannel] = binding
			cfg := newTestMCPConfig(t, time.Second)
			cfg.TransportKind = tc.kind
			cfg.ExtraHeaders = tc.static
			if tc.optIn != nil {
				cfg.ForwardTraceContext = *tc.optIn
			}
			responder := newRecordingResponder()
			processor, err := NewProcessor(processorParams{
				Logger:          slog.New(slog.NewTextHandler(io.Discard, nil)),
				ChannelBindings: bindings,
				TunnelResponder: responder,
				MCPConfig:       cfg,
				OAuthHTTPClient: &http.Client{},
				ControlPlaneCfg: newTestControlPlaneConfig(t),
				MeterProvider:   newTestMeterProvider(t),
			})
			require.NoError(t, err)
			base := &fakePolledCommand{
				id: "trace-command", message: req, headers: tc.headers, shardToken: "trace-shard",
			}
			var command controlplane.PolledCommand = base
			if !tc.legacy {
				command = &processorTraceCommand{fakePolledCommand: base}
			}

			require.NoError(t, processor.Process(context.Background(), command))
			require.NotNil(t, conn.written)
			require.Equal(t, originalParams, req.Params)
			require.Equal(t, req.ID, conn.written.ID)
			if tc.wantForward {
				require.NotSame(t, req, conn.written)
				params := traceContextTestObject(t, conn.written.Params)
				meta := traceContextTestObject(t, params["_meta"])
				require.Equal(t, json.RawMessage(`"keep"`), meta["customer"])
				require.Equal(t, json.RawMessage(`"`+traceContextTestParent+`"`), meta["traceparent"])
				require.NotContains(t, meta, "baggage")
			} else {
				require.Same(t, req, conn.written)
				require.Equal(t, originalParams, conn.written.Params)
			}
			got := responder.waitForResponse(t)
			require.Equal(t, base.id, got.requestID)
			require.JSONEq(t, `{"jsonrpc":"2.0","id":"customer-rpc","result":{}}`, string(got.response.Payload()))
		})
	}
}

type processorTraceCommand struct {
	*fakePolledCommand
}

func (*processorTraceCommand) TraceContext() *types.TraceContext {
	return &types.TraceContext{Traceparent: traceContextTestParent}
}

type processorTraceConnection struct {
	*stubForwardingConnection
	written *jsonrpc.Request
}

func (c *processorTraceConnection) Write(ctx context.Context, headers http.Header, message jsonrpc.Message) (mcpclient.ForwardingWriteResult, error) {
	c.written, _ = message.(*jsonrpc.Request)
	return c.stubForwardingConnection.Write(ctx, headers, message)
}
