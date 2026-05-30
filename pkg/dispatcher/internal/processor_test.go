package dispatcherinternal

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/url"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/modelcontextprotocol/go-sdk/jsonrpc"
	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/stretchr/testify/require"
	"go.opentelemetry.io/otel/attribute"
	sdkmetric "go.opentelemetry.io/otel/sdk/metric"
	"go.opentelemetry.io/otel/sdk/metric/metricdata"

	"go.openai.org/api/tunnel-client/pkg/config"
	"go.openai.org/api/tunnel-client/pkg/harpoon"
	"go.openai.org/api/tunnel-client/pkg/harpoon/hostbus"
	"go.openai.org/api/tunnel-client/pkg/mcpclient"
	"go.openai.org/api/tunnel-client/pkg/tunnelctx"
	"go.openai.org/api/tunnel-client/pkg/types"
)

func decodeJSONRPCResponse(t *testing.T, raw json.RawMessage) *jsonrpc.Response {
	t.Helper()
	if len(raw) == 0 {
		return nil
	}

	msg, err := jsonrpc.DecodeMessage(raw)
	require.NoError(t, err, "decode jsonrpc response")

	resp, ok := msg.(*jsonrpc.Response)
	require.True(t, ok, "expected JSON-RPC response message")

	return resp
}

func assertTerminalJSONRPCErrorResponse(t *testing.T, responder *recordingResponder, cmd *fakePolledCommand, wantStatus int, wantSubstrings ...string) *jsonrpc.Response {
	t.Helper()

	got := responder.waitForResponse(t)
	require.Equal(t, cmd.id, got.requestID)
	require.Equal(t, types.ResponseTypeJSONRPCResponse, got.response.Type())
	require.Equal(t, wantStatus, got.response.ResponseCode())
	require.Equal(t, "application/json", got.response.Headers().Get("Content-Type"))

	resp := decodeJSONRPCResponse(t, got.response.Payload())
	require.NotNil(t, resp)
	require.NotNil(t, resp.Error)
	for _, want := range wantSubstrings {
		require.Contains(t, resp.Error.Error(), want)
	}

	return resp
}

func TestProcessorForwardResponses(t *testing.T) {
	t.Parallel()

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)

	serverLogger := slog.New(slog.NewTextHandler(io.Discard, &slog.HandlerOptions{Level: slog.LevelDebug}))
	server := mcp.NewServer(&mcp.Implementation{Name: "test-server", Version: "1.0.0"}, &mcp.ServerOptions{
		Logger: serverLogger,
	})

	serverTransport, clientTransport := mcp.NewInMemoryTransports()

	serverDone := make(chan error, 1)
	go func() {
		serverDone <- server.Run(ctx, serverTransport)
	}()

	t.Cleanup(func() {
		cancel()
		select {
		case err := <-serverDone:
			if err != nil && !errors.Is(err, context.Canceled) {
				t.Errorf("server run returned error: %v", err)
			}
		case <-time.After(time.Second):
			t.Errorf("server did not exit before timeout")
		}
	})

	responder := newRecordingResponder()
	processorLogger := slog.New(slog.NewTextHandler(io.Discard, &slog.HandlerOptions{Level: slog.LevelDebug}))
	forwardingTransport := mcpclient.NewForwardingTransport(clientTransport)
	meterProvider := newTestMeterProvider(t)
	processor, err := NewProcessor(processorParams{
		Logger:          processorLogger,
		ChannelBindings: newTestChannelBindings(forwardingTransport),
		TunnelResponder: responder,
		MCPConfig:       newTestMCPConfig(t, 2*time.Second),
		OAuthHTTPClient: &http.Client{},
		ControlPlaneCfg: newTestControlPlaneConfig(t),
		MeterProvider:   meterProvider,
	})
	require.NoError(t, err)

	params := mcp.InitializeParams{
		ClientInfo:      &mcp.Implementation{Name: "test-client", Version: "1.0.0"},
		ProtocolVersion: "2024-06-01",
	}
	paramsJSON, err := json.Marshal(&params)
	require.NoError(t, err)

	id, err := jsonrpc.MakeID("req-1")
	require.NoError(t, err)

	req := &jsonrpc.Request{
		ID:     id,
		Method: "initialize",
		Params: paramsJSON,
	}

	command := &fakePolledCommand{
		id:         types.RequestID("request-id"),
		message:    req,
		enqueuedAt: time.Now(),
		polledAt:   time.Now(),
		headers: http.Header{
			"foo": []string{"bar"},
		},
		shardToken: "shard-request-id",
	}

	err = processor.Process(ctx, command)
	require.NoError(t, err)

	got := responder.waitForResponse(t)
	require.Equal(t, command.id, got.requestID)
	resp := decodeJSONRPCResponse(t, got.response.Payload())
	require.NotNil(t, resp, "expected JSON-RPC response payload")
	require.Nil(t, resp.Error)

	var result mcp.InitializeResult
	require.NoError(t, json.Unmarshal(resp.Result, &result))
	require.NotNil(t, result.ServerInfo)
	require.Equal(t, "test-server", result.ServerInfo.Name)
	require.Equal(t, "1.0.0", result.ServerInfo.Version)
}

func TestProcessorAddsDefaultAcceptHeaderWhenMissing(t *testing.T) {
	t.Parallel()

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	t.Cleanup(cancel)

	responder := newRecordingResponder()
	processorLogger := slog.New(slog.NewTextHandler(io.Discard, &slog.HandlerOptions{Level: slog.LevelDebug}))
	conn := &stubForwardingConnection{
		statusCode: http.StatusOK,
		responseHeaders: http.Header{
			"Content-Type": []string{"application/json"},
		},
	}
	id, err := jsonrpc.MakeID("accept-req")
	require.NoError(t, err)
	conn.response = &jsonrpc.Response{
		ID:     id,
		Result: json.RawMessage(`{"ok":true}`),
	}
	transport := &stubForwardingTransport{conn: conn}
	meterProvider := newTestMeterProvider(t)
	processor, err := NewProcessor(processorParams{
		Logger:          processorLogger,
		ChannelBindings: newTestChannelBindings(transport),
		TunnelResponder: responder,
		MCPConfig:       newTestMCPConfig(t, time.Second),
		OAuthHTTPClient: &http.Client{},
		ControlPlaneCfg: newTestControlPlaneConfig(t),
		MeterProvider:   meterProvider,
	})
	require.NoError(t, err)

	command := &fakePolledCommand{
		id:         types.RequestID("request-id"),
		message:    &jsonrpc.Request{ID: id, Method: "initialize"},
		enqueuedAt: time.Now(),
		polledAt:   time.Now(),
		headers: http.Header{
			"x-test": []string{"value"},
		},
		shardToken: "shard-request-id",
	}

	err = processor.Process(ctx, command)
	require.NoError(t, err)

	_ = responder.waitForResponse(t)
	require.NotNil(t, conn.writeHeaders)
	require.Equal(t, defaultAcceptHeaderValue, conn.writeHeaders.Get("Accept"))
}

func TestProcessorForwardsCustomMCPHeaders(t *testing.T) {
	t.Parallel()

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	t.Cleanup(cancel)

	responder := newRecordingResponder()
	processorLogger := slog.New(slog.NewTextHandler(io.Discard, &slog.HandlerOptions{Level: slog.LevelDebug}))
	conn := &stubForwardingConnection{
		statusCode: http.StatusOK,
		responseHeaders: http.Header{
			"Content-Type": []string{"application/json"},
		},
	}
	id, err := jsonrpc.MakeID("custom-header-req")
	require.NoError(t, err)
	conn.response = &jsonrpc.Response{
		ID:     id,
		Result: json.RawMessage(`{"ok":true}`),
	}
	transport := &stubForwardingTransport{conn: conn}
	meterProvider := newTestMeterProvider(t)
	processor, err := NewProcessor(processorParams{
		Logger:          processorLogger,
		ChannelBindings: newTestChannelBindings(transport),
		TunnelResponder: responder,
		MCPConfig:       newTestMCPConfig(t, time.Second),
		OAuthHTTPClient: &http.Client{},
		ControlPlaneCfg: newTestControlPlaneConfig(t),
		MeterProvider:   meterProvider,
	})
	require.NoError(t, err)

	command := &fakePolledCommand{
		id:         types.RequestID("request-id"),
		message:    &jsonrpc.Request{ID: id, Method: "initialize"},
		enqueuedAt: time.Now(),
		polledAt:   time.Now(),
		headers: http.Header{
			"Authorization":    []string{"Bearer customer-token"},
			"X-Custom-Mcp":     []string{"custom-value"},
			"X-Openai-Session": []string{"session-456"},
			"X-Openai-Subject": []string{"subject-123"},
		},
		shardToken: "shard-request-id",
	}

	err = processor.Process(ctx, command)
	require.NoError(t, err)

	_ = responder.waitForResponse(t)
	require.NotNil(t, conn.writeHeaders)
	require.Equal(t, []string{"Bearer customer-token"}, conn.writeHeaders["Authorization"])
	require.Equal(t, []string{"custom-value"}, conn.writeHeaders["X-Custom-Mcp"])
	require.Equal(t, []string{"session-456"}, conn.writeHeaders["X-Openai-Session"])
	require.Equal(t, []string{"subject-123"}, conn.writeHeaders["X-Openai-Subject"])
	require.Equal(t, []string{defaultAcceptHeaderValue}, conn.writeHeaders["Accept"])
}

func TestProcessorClonesForwardedHeadersBeforeTransportWrite(t *testing.T) {
	t.Parallel()

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	t.Cleanup(cancel)

	responder := newRecordingResponder()
	processorLogger := slog.New(slog.NewTextHandler(io.Discard, &slog.HandlerOptions{Level: slog.LevelDebug}))
	conn := &stubForwardingConnection{
		statusCode: http.StatusOK,
		responseHeaders: http.Header{
			"Content-Type": []string{"application/json"},
		},
		mutateWriteHeaders: func(headers http.Header) {
			headers.Set("Authorization", "Bearer mutated-by-transport")
			headers.Set("Accept", "text/plain")
			headers.Set("X-Transport-Only", "set-by-transport")
		},
	}
	id, err := jsonrpc.MakeID("clone-header-req")
	require.NoError(t, err)
	conn.response = &jsonrpc.Response{
		ID:     id,
		Result: json.RawMessage(`{"ok":true}`),
	}
	transport := &stubForwardingTransport{conn: conn}
	meterProvider := newTestMeterProvider(t)
	processor, err := NewProcessor(processorParams{
		Logger:          processorLogger,
		ChannelBindings: newTestChannelBindings(transport),
		TunnelResponder: responder,
		MCPConfig:       newTestMCPConfig(t, time.Second),
		OAuthHTTPClient: &http.Client{},
		ControlPlaneCfg: newTestControlPlaneConfig(t),
		MeterProvider:   meterProvider,
	})
	require.NoError(t, err)

	originalHeaders := http.Header{
		"Accept":        []string{"application/json"},
		"Authorization": []string{"Bearer connector-token"},
		"X-Custom-Mcp":  []string{"custom-value"},
	}
	command := &fakePolledCommand{
		id:         types.RequestID("request-id"),
		message:    &jsonrpc.Request{ID: id, Method: "initialize"},
		enqueuedAt: time.Now(),
		polledAt:   time.Now(),
		headers:    originalHeaders,
		shardToken: "shard-request-id",
	}

	err = processor.Process(ctx, command)
	require.NoError(t, err)

	_ = responder.waitForResponse(t)
	require.NotNil(t, conn.writeHeaders)
	require.Equal(t, []string{"Bearer connector-token"}, conn.writeHeaders["Authorization"])
	require.Equal(t, []string{"application/json"}, conn.writeHeaders["Accept"])
	require.Equal(t, []string{"Bearer connector-token"}, originalHeaders["Authorization"])
	require.Equal(t, []string{"application/json"}, originalHeaders["Accept"])
	require.Empty(t, originalHeaders.Values("X-Transport-Only"))
}

func TestProcessorRejectsUnsupportedChannel(t *testing.T) {
	t.Parallel()

	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	responder := newRecordingResponder()
	reader := sdkmetric.NewManualReader()
	meterProvider := sdkmetric.NewMeterProvider(sdkmetric.WithReader(reader))
	t.Cleanup(func() {
		_ = meterProvider.Shutdown(context.Background())
	})

	processor, err := NewProcessor(processorParams{
		Logger:          logger,
		ChannelBindings: newTestChannelBindings(&stubForwardingTransport{}),
		TunnelResponder: responder,
		MCPConfig:       newTestMCPConfig(t, time.Second),
		OAuthHTTPClient: &http.Client{},
		ControlPlaneCfg: newTestControlPlaneConfig(t),
		MeterProvider:   meterProvider,
	})
	require.NoError(t, err)

	id, err := jsonrpc.MakeID("unsupported-channel")
	require.NoError(t, err)

	cmd := &fakePolledCommand{
		id:         types.RequestID("unsupported-channel"),
		message:    &jsonrpc.Request{ID: id, Method: "ping"},
		enqueuedAt: time.Now(),
		polledAt:   time.Now(),
		headers:    http.Header{},
		shardToken: "shard-unsupported",
		channel:    types.Channel("unknown"),
	}

	err = processor.Process(context.Background(), cmd)
	require.Error(t, err)

	got := responder.waitForResponse(t)
	require.Equal(t, cmd.id, got.requestID)
	resp := decodeJSONRPCResponse(t, got.response.Payload())
	require.NotNil(t, resp)
	require.NotNil(t, resp.Error)

	var rm metricdata.ResourceMetrics
	require.NoError(t, reader.Collect(context.Background(), &rm))
	sum, ok := findCounter(rm, metricNameUnsupportedChannel)
	require.True(t, ok, "unsupported channel metric not found")

	var found bool
	for _, dp := range sum.DataPoints {
		if dp.Value != 1 {
			continue
		}
		if val, ok := dp.Attributes.Value(attribute.Key("channel")); ok && val.AsString() == "unknown" {
			found = true
			break
		}
	}
	require.True(t, found, "unsupported channel metric missing channel attribute")
}

func TestProcessorRoutesHarpoonChannel(t *testing.T) {
	t.Parallel()

	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	responder := newRecordingResponder()

	registry := newTestHarpoonRegistry(t)

	id, err := jsonrpc.MakeID("harpoon-route")
	require.NoError(t, err)

	mainTransport := &countingForwardingTransport{}
	harpoonConn := &stubMCPConnection{
		readMsg: &jsonrpc.Response{
			ID:     id,
			Result: json.RawMessage(`{"ok":true}`),
		},
	}
	harpoonTransport := &countingMCPTransport{conn: harpoonConn}
	bindings := newTestChannelBindings(mainTransport)
	bindings[types.ChannelHarpoon] = ChannelBinding{
		Transport:     mcpclient.NewForwardingTransport(harpoonTransport),
		Priority:      0,
		Routable:      func() bool { return registry.Count() > 0 },
		SupportsMCP:   true,
		SupportsOAuth: false,
	}

	processor, err := NewProcessor(processorParams{
		Logger:          logger,
		ChannelBindings: bindings,
		TunnelResponder: responder,
		MCPConfig:       newTestMCPConfig(t, time.Second),
		OAuthHTTPClient: &http.Client{},
		ControlPlaneCfg: newTestControlPlaneConfig(t),
		MeterProvider:   newTestMeterProvider(t),
	})
	require.NoError(t, err)

	cmd := &fakePolledCommand{
		id:         types.RequestID("harpoon-request"),
		message:    &jsonrpc.Request{ID: id, Method: "ping"},
		enqueuedAt: time.Now(),
		polledAt:   time.Now(),
		headers:    http.Header{},
		shardToken: "shard-harpoon",
		channel:    types.ChannelHarpoon,
	}

	require.NoError(t, processor.Process(context.Background(), cmd))
	_ = responder.waitForResponse(t)

	require.EqualValues(t, 1, harpoonTransport.calls.Load())
	require.EqualValues(t, 0, mainTransport.calls.Load())
}

func TestProcessorRejectsHarpoonChannelWithoutTargets(t *testing.T) {
	t.Parallel()

	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	responder := newRecordingResponder()

	emptyRegistry, err := harpoon.NewRegistry(logger, false, nil)
	require.NoError(t, err)

	harpoonTransport := &countingMCPTransport{}
	bindings := newTestChannelBindings(&stubForwardingTransport{})
	bindings[types.ChannelHarpoon] = ChannelBinding{
		Transport:     mcpclient.NewForwardingTransport(harpoonTransport),
		Priority:      0,
		Routable:      func() bool { return emptyRegistry.Count() > 0 },
		SupportsMCP:   true,
		SupportsOAuth: false,
	}

	processor, err := NewProcessor(processorParams{
		Logger:          logger,
		ChannelBindings: bindings,
		TunnelResponder: responder,
		MCPConfig:       newTestMCPConfig(t, time.Second),
		OAuthHTTPClient: &http.Client{},
		ControlPlaneCfg: newTestControlPlaneConfig(t),
		MeterProvider:   newTestMeterProvider(t),
	})
	require.NoError(t, err)

	id, err := jsonrpc.MakeID("harpoon-disabled")
	require.NoError(t, err)

	cmd := &fakePolledCommand{
		id:         types.RequestID("harpoon-disabled"),
		message:    &jsonrpc.Request{ID: id, Method: "ping"},
		enqueuedAt: time.Now(),
		polledAt:   time.Now(),
		headers:    http.Header{},
		shardToken: "shard-harpoon-disabled",
		channel:    types.ChannelHarpoon,
	}

	err = processor.Process(context.Background(), cmd)
	require.Error(t, err)

	_ = responder.waitForResponse(t)
	require.EqualValues(t, 0, harpoonTransport.calls.Load())
}

func TestProcessorRejectsHarpoonOAuthDiscovery(t *testing.T) {
	t.Parallel()

	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	responder := newRecordingResponder()

	registry := newTestHarpoonRegistry(t)
	harpoonTransport := &countingMCPTransport{}
	bindings := newTestChannelBindings(&stubForwardingTransport{})
	bindings[types.ChannelHarpoon] = ChannelBinding{
		Transport:     mcpclient.NewForwardingTransport(harpoonTransport),
		Priority:      0,
		Routable:      func() bool { return registry.Count() > 0 },
		SupportsMCP:   true,
		SupportsOAuth: false,
	}

	processor, err := NewProcessor(processorParams{
		Logger:          logger,
		ChannelBindings: bindings,
		TunnelResponder: responder,
		MCPConfig:       newTestMCPConfig(t, time.Second),
		OAuthHTTPClient: &http.Client{},
		ControlPlaneCfg: newTestControlPlaneConfig(t),
		MeterProvider:   newTestMeterProvider(t),
	})
	require.NoError(t, err)

	cmd := &fakeOauthDiscoveryCommand{
		id:         types.RequestID("harpoon-oauth"),
		enqueuedAt: time.Now(),
		polledAt:   time.Now(),
		headers:    http.Header{},
		shardToken: "shard-harpoon-oauth",
		channel:    types.ChannelHarpoon,
	}

	err = processor.Process(context.Background(), cmd)
	require.Error(t, err)

	_ = responder.waitForResponse(t)
	require.EqualValues(t, 0, harpoonTransport.calls.Load())
}

func TestProcessorStreamableNotificationsBeforeResponse(t *testing.T) {
	t.Parallel()

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)

	serverLogger := slog.New(slog.NewTextHandler(io.Discard, &slog.HandlerOptions{Level: slog.LevelDebug}))
	server := mcp.NewServer(&mcp.Implementation{Name: "test-server", Version: "1.0.0"}, &mcp.ServerOptions{
		Logger: serverLogger,
	})

	var notificationCount atomic.Int32
	server.AddReceivingMiddleware(func(next mcp.MethodHandler) mcp.MethodHandler {
		return func(ctx context.Context, method string, req mcp.Request) (mcp.Result, error) {
			if method == "initialize" {
				session, ok := req.GetSession().(*mcp.ServerSession)
				if !ok {
					return nil, fmt.Errorf("unexpected session type %T", req.GetSession())
				}
				for i := 1; i <= 3; i++ {
					if err := session.NotifyProgress(ctx, &mcp.ProgressNotificationParams{
						Message:  fmt.Sprintf("initializing %d/3", i),
						Progress: float64(i),
						Total:    3,
					}); err != nil {
						return nil, err
					}
					notificationCount.Add(1)
					time.Sleep(10 * time.Millisecond)
				}
			}
			return next(ctx, method, req)
		}
	})

	serverTransport, clientTransport := mcp.NewInMemoryTransports()

	serverDone := make(chan error, 1)
	go func() {
		serverDone <- server.Run(ctx, serverTransport)
	}()

	t.Cleanup(func() {
		cancel()

		select {
		case err := <-serverDone:
			if err != nil && !errors.Is(err, context.Canceled) {
				t.Errorf("server run returned error: %v", err)
			}
		case <-time.After(time.Second):
			t.Errorf("server did not exit before timeout")
		}
	})

	responder := newRecordingResponder()
	processorLogger := slog.New(slog.NewTextHandler(io.Discard, &slog.HandlerOptions{Level: slog.LevelDebug}))
	forwardingTransport := mcpclient.NewForwardingTransport(clientTransport)
	meterProvider := newTestMeterProvider(t)
	processor, err := NewProcessor(processorParams{
		Logger:          processorLogger,
		ChannelBindings: newTestChannelBindings(forwardingTransport),
		TunnelResponder: responder,
		MCPConfig:       newTestMCPConfig(t, 5*time.Second),
		OAuthHTTPClient: &http.Client{},
		ControlPlaneCfg: newTestControlPlaneConfig(t),
		MeterProvider:   meterProvider,
	})
	require.NoError(t, err)

	params := mcp.InitializeParams{
		ClientInfo:      &mcp.Implementation{Name: "test-client", Version: "1.0.0"},
		ProtocolVersion: "2025-03-26",
	}
	paramsJSON, err := json.Marshal(&params)
	require.NoError(t, err)

	id, err := jsonrpc.MakeID("streamable-req-1")
	require.NoError(t, err)

	req := &jsonrpc.Request{
		ID:     id,
		Method: "initialize",
		Params: paramsJSON,
	}

	command := &fakePolledCommand{
		id:         types.RequestID("streamable-request-id"),
		message:    req,
		enqueuedAt: time.Now(),
		polledAt:   time.Now(),
		headers: http.Header{
			"scenario": []string{"streamable"},
		},
		shardToken: "shard-streamable",
	}

	err = processor.Process(ctx, command)
	require.NoError(t, err)

	got := responder.waitForResponses(t, 4)
	require.Len(t, got, 4)

	for i := 0; i < 3; i++ {
		notif := got[i]
		require.Equal(t, command.id, notif.requestID)
		require.Equal(t, types.ResponseTypeJSONRPCNotification, notif.response.Type())
		require.Equal(t, "text/event-stream", notif.response.Headers().Get("Content-Type"))

		msg, err := jsonrpc.DecodeMessage(notif.response.Payload())
		require.NoError(t, err)
		reqMsg, ok := msg.(*jsonrpc.Request)
		require.True(t, ok)
		require.False(t, reqMsg.ID.IsValid())
		require.Equal(t, "notifications/progress", reqMsg.Method)
	}

	final := got[3]
	require.Equal(t, command.id, final.requestID)
	resp := decodeJSONRPCResponse(t, final.response.Payload())
	require.NotNil(t, resp, "expected JSON-RPC response payload")
	require.Nil(t, resp.Error)

	var result mcp.InitializeResult
	require.NoError(t, json.Unmarshal(resp.Result, &result))
	require.NotNil(t, result.ServerInfo)
	require.Equal(t, "test-server", result.ServerInfo.Name)
	require.Equal(t, "1.0.0", result.ServerInfo.Version)
	require.EqualValues(t, 3, notificationCount.Load())
}

func TestProcessorAcknowledgesNotifications(t *testing.T) {
	t.Parallel()

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	serverLogger := slog.New(slog.NewTextHandler(io.Discard, &slog.HandlerOptions{Level: slog.LevelDebug}))
	server := mcp.NewServer(&mcp.Implementation{Name: "test-server", Version: "1.0.0"}, &mcp.ServerOptions{
		Logger: serverLogger,
	})

	serverTransport, clientTransport := mcp.NewInMemoryTransports()

	serverDone := make(chan error, 1)
	go func() {
		serverDone <- server.Run(ctx, serverTransport)
	}()
	t.Cleanup(func() {
		select {
		case <-serverDone:
		case <-time.After(time.Second):
			t.Errorf("server did not exit before timeout")
		}
	})

	responder := newRecordingResponder()
	processorLogger := slog.New(slog.NewTextHandler(io.Discard, &slog.HandlerOptions{Level: slog.LevelDebug}))
	forwardingTransport := mcpclient.NewForwardingTransport(clientTransport)
	meterProvider := newTestMeterProvider(t)
	processor, err := NewProcessor(processorParams{
		Logger:          processorLogger,
		ChannelBindings: newTestChannelBindings(forwardingTransport),
		TunnelResponder: responder,
		MCPConfig:       newTestMCPConfig(t, 2*time.Second),
		OAuthHTTPClient: &http.Client{},
		ControlPlaneCfg: newTestControlPlaneConfig(t),
		MeterProvider:   meterProvider,
	})
	require.NoError(t, err)

	req := &jsonrpc.Request{
		Method: "notifications/initialized",
	}

	command := &fakePolledCommand{
		id:         types.RequestID("notif-request-id"),
		message:    req,
		enqueuedAt: time.Now(),
		polledAt:   time.Now(),
		shardToken: "shard-notification",
	}

	err = processor.Process(ctx, command)
	require.NoError(t, err)

	got := responder.waitForResponse(t)
	require.Equal(t, command.id, got.requestID)
	require.Empty(t, got.response.Payload(), "notification acknowledgements must not carry JSON-RPC payloads")
	require.Equal(t, types.ResponseTypeNotificationAcknowledgment, got.response.Type(), "notification acknowledgements must set the ack response type")
}

func TestProcessorLogsIncludeRequestAndSessionID(t *testing.T) {
	t.Parallel()

	testCases := []struct {
		name           string
		commandSession *string
		headerSession  *string
		wantSession    string
	}{
		{
			name:        "from_command",
			wantSession: "cmd-session",
			commandSession: func() *string {
				s := "cmd-session"
				return &s
			}(),
		},
		{
			name:        "from_header",
			wantSession: "header-session",
			headerSession: func() *string {
				s := "header-session"
				return &s
			}(),
		},
	}

	for _, tc := range testCases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			var buf bytes.Buffer
			logger := slog.New(slog.NewTextHandler(&buf, &slog.HandlerOptions{Level: slog.LevelDebug}))
			responder := newRecordingResponder()
			transport := &stubForwardingTransport{
				conn: &stubForwardingConnection{
					responseHeaders: func() http.Header {
						headers := make(http.Header)
						if tc.headerSession != nil {
							headers.Set(mcpclient.HeaderSessionID, *tc.headerSession)
						}
						return headers
					}(),
				},
			}
			meterProvider := newTestMeterProvider(t)
			processor, err := NewProcessor(processorParams{
				Logger:          logger,
				ChannelBindings: newTestChannelBindings(transport),
				TunnelResponder: responder,
				MCPConfig:       newTestMCPConfig(t, time.Second),
				OAuthHTTPClient: &http.Client{},
				ControlPlaneCfg: newTestControlPlaneConfig(t),
				MeterProvider:   meterProvider,
			})
			require.NoError(t, err)

			id, err := jsonrpc.MakeID("session-log")
			require.NoError(t, err)

			cmd := &fakePolledCommand{
				id:         types.RequestID("session-log-request"),
				message:    &jsonrpc.Request{ID: id, Method: "ping"},
				enqueuedAt: time.Now(),
				polledAt:   time.Now(),
				sessionID:  tc.commandSession,
				shardToken: "shard-session-log",
			}

			transport.conn.(*stubForwardingConnection).response = &jsonrpc.Response{
				ID:     id,
				Result: json.RawMessage(`{"ok":true}`),
			}

			require.NoError(t, processor.Process(context.Background(), cmd))
			_ = responder.waitForResponse(t)

			require.Contains(t, buf.String(), "session_id="+tc.wantSession)
			require.Contains(t, buf.String(), "request_id=session-log-request")
		})
	}
}

func TestProcessorPreservesSSEContentTypeHeader(t *testing.T) {
	t.Parallel()

	logger := slog.New(slog.NewTextHandler(io.Discard, &slog.HandlerOptions{Level: slog.LevelDebug}))
	responder := newRecordingResponder()

	id, err := jsonrpc.MakeID("content-type-override")
	require.NoError(t, err)

	transport := &stubForwardingTransport{
		conn: &stubForwardingConnection{
			responseHeaders: http.Header{
				http.CanonicalHeaderKey("Content-Type"): []string{"text/event-stream"},
			},
			response: &jsonrpc.Response{
				ID:     id,
				Result: json.RawMessage(`{"ok":true}`),
			},
		},
	}

	meterProvider := newTestMeterProvider(t)
	processor, err := NewProcessor(processorParams{
		Logger:          logger,
		ChannelBindings: newTestChannelBindings(transport),
		TunnelResponder: responder,
		MCPConfig:       newTestMCPConfig(t, time.Second),
		OAuthHTTPClient: &http.Client{},
		ControlPlaneCfg: newTestControlPlaneConfig(t),
		MeterProvider:   meterProvider,
	})
	require.NoError(t, err)

	cmd := &fakePolledCommand{
		id:         types.RequestID("content-type-request"),
		message:    &jsonrpc.Request{ID: id, Method: "ping"},
		enqueuedAt: time.Now(),
		polledAt:   time.Now(),
		shardToken: "shard-content-type",
	}

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	require.NoError(t, processor.Process(ctx, cmd))
	resp := responder.waitForResponse(t)
	require.Equal(t, cmd.id, resp.requestID)
	require.Equal(t, "text/event-stream", resp.response.Headers().Get("Content-Type"))

	jsonResp := decodeJSONRPCResponse(t, resp.response.Payload())
	require.NotNil(t, jsonResp)
	require.Equal(t, id, jsonResp.ID)
}

func TestProcessorOverridesNonStreamingContentTypeHeader(t *testing.T) {
	t.Parallel()

	logger := slog.New(slog.NewTextHandler(io.Discard, &slog.HandlerOptions{Level: slog.LevelDebug}))
	responder := newRecordingResponder()

	id, err := jsonrpc.MakeID("content-type-override")
	require.NoError(t, err)

	transport := &stubForwardingTransport{
		conn: &stubForwardingConnection{
			responseHeaders: http.Header{
				http.CanonicalHeaderKey("Content-Type"): []string{"text/plain"},
			},
			response: &jsonrpc.Response{
				ID:     id,
				Result: json.RawMessage(`{"ok":true}`),
			},
		},
	}

	meterProvider := newTestMeterProvider(t)
	processor, err := NewProcessor(processorParams{
		Logger:          logger,
		ChannelBindings: newTestChannelBindings(transport),
		TunnelResponder: responder,
		MCPConfig:       newTestMCPConfig(t, time.Second),
		OAuthHTTPClient: &http.Client{},
		ControlPlaneCfg: newTestControlPlaneConfig(t),
		MeterProvider:   meterProvider,
	})
	require.NoError(t, err)

	cmd := &fakePolledCommand{
		id:         types.RequestID("content-type-request"),
		message:    &jsonrpc.Request{ID: id, Method: "ping"},
		enqueuedAt: time.Now(),
		polledAt:   time.Now(),
		shardToken: "shard-content-type",
	}

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	require.NoError(t, processor.Process(ctx, cmd))
	resp := responder.waitForResponse(t)
	require.Equal(t, cmd.id, resp.requestID)
	require.Equal(t, "application/json", resp.response.Headers().Get("Content-Type"))

	jsonResp := decodeJSONRPCResponse(t, resp.response.Payload())
	require.NotNil(t, jsonResp)
	require.Equal(t, id, jsonResp.ID)
}

func TestProcessorForwardsNotificationsWithJSONContentType(t *testing.T) {
	t.Parallel()

	logger := slog.New(slog.NewTextHandler(io.Discard, &slog.HandlerOptions{Level: slog.LevelDebug}))
	responder := newRecordingResponder()

	callID, err := jsonrpc.MakeID("notif-content-type")
	require.NoError(t, err)

	transport := &stubForwardingTransport{conn: &scriptedForwardingConnection{
		statusCode: http.StatusOK,
		headers: http.Header{
			http.CanonicalHeaderKey("Content-Type"): []string{"text/event-stream"},
		},
		readSteps: []readStep{
			{msg: &jsonrpc.Request{Method: "notifications/progress"}, err: nil},
			{msg: &jsonrpc.Response{ID: callID, Result: json.RawMessage(`{"ok":true}`)}, err: nil},
		},
	}}

	meterProvider := newTestMeterProvider(t)
	processor, err := NewProcessor(processorParams{
		Logger:          logger,
		ChannelBindings: newTestChannelBindings(transport),
		TunnelResponder: responder,
		MCPConfig:       newTestMCPConfig(t, time.Second),
		OAuthHTTPClient: &http.Client{},
		ControlPlaneCfg: newTestControlPlaneConfig(t),
		MeterProvider:   meterProvider,
	})
	require.NoError(t, err)

	cmd := &fakePolledCommand{
		id:         types.RequestID("notif-content-type-request"),
		message:    &jsonrpc.Request{ID: callID, Method: "ping"},
		enqueuedAt: time.Now(),
		polledAt:   time.Now(),
		shardToken: "shard-notif-content-type",
	}

	require.NoError(t, processor.Process(context.Background(), cmd))

	responses := responder.waitForResponses(t, 2)
	require.Len(t, responses, 2)

	notif := responses[0]
	require.Equal(t, types.ResponseTypeJSONRPCNotification, notif.response.Type())
	require.Equal(t, "text/event-stream", notif.response.Headers().Get("Content-Type"))
	msg, err := jsonrpc.DecodeMessage(notif.response.Payload())
	require.NoError(t, err)
	reqMsg, ok := msg.(*jsonrpc.Request)
	require.True(t, ok)
	require.False(t, reqMsg.ID.IsValid())
	require.Equal(t, "notifications/progress", reqMsg.Method)

	final := responses[1]
	require.Equal(t, types.ResponseTypeJSONRPCResponse, final.response.Type())
	require.Equal(t, "text/event-stream", final.response.Headers().Get("Content-Type"))
	jsonResp := decodeJSONRPCResponse(t, final.response.Payload())
	require.NotNil(t, jsonResp)
	require.Equal(t, callID, jsonResp.ID)
}

func TestProcessorReturnsErrorResponseOnWriteFailure(t *testing.T) {
	t.Parallel()

	logger := slog.New(slog.NewTextHandler(io.Discard, &slog.HandlerOptions{Level: slog.LevelDebug}))
	responder := newRecordingResponder()

	id, err := jsonrpc.MakeID("unauthorized-call")
	require.NoError(t, err)

	transport := &stubForwardingTransport{
		conn: &stubForwardingConnection{
			statusCode: http.StatusUnauthorized,
			writeErr:   fmt.Errorf("unauthorized"),
		},
	}

	meterProvider := newTestMeterProvider(t)
	processor, err := NewProcessor(processorParams{
		Logger:          logger,
		ChannelBindings: newTestChannelBindings(transport),
		TunnelResponder: responder,
		MCPConfig:       newTestMCPConfig(t, time.Second),
		OAuthHTTPClient: &http.Client{},
		ControlPlaneCfg: newTestControlPlaneConfig(t),
		MeterProvider:   meterProvider,
	})
	require.NoError(t, err)

	cmd := &fakePolledCommand{
		id:         types.RequestID("unauthorized-request"),
		message:    &jsonrpc.Request{ID: id, Method: "initialize"},
		enqueuedAt: time.Now(),
		polledAt:   time.Now(),
		shardToken: "shard-unauthorized",
	}

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	require.NoError(t, processor.Process(ctx, cmd))

	resp := responder.waitForResponse(t)
	require.Equal(t, cmd.id, resp.requestID)
	require.Equal(t, http.StatusUnauthorized, resp.response.ResponseCode())
	require.Equal(t, "application/json", resp.response.Headers().Get("Content-Type"))

	rpcResp := decodeJSONRPCResponse(t, resp.response.Payload())
	require.NotNil(t, rpcResp)
	require.NotNil(t, rpcResp.Error)
	require.Contains(t, rpcResp.Error.Error(), "unauthorized")
}

func TestProcessorPropagatesControlPlaneCommandRequestID(t *testing.T) {
	t.Parallel()

	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	responder := newRecordingResponder()

	id, err := jsonrpc.MakeID("control-plane-context")
	require.NoError(t, err)

	transport := &stubForwardingTransport{conn: &stubForwardingConnection{
		response: &jsonrpc.Response{
			ID:     id,
			Result: json.RawMessage(`{"ok":true}`),
		},
	}}

	meterProvider := newTestMeterProvider(t)
	processor, err := NewProcessor(processorParams{
		Logger:          logger,
		ChannelBindings: newTestChannelBindings(transport),
		TunnelResponder: responder,
		MCPConfig:       newTestMCPConfig(t, time.Second),
		OAuthHTTPClient: &http.Client{},
		ControlPlaneCfg: newTestControlPlaneConfig(t),
		MeterProvider:   meterProvider,
	})
	require.NoError(t, err)

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	cmd := &fakePolledCommand{
		id:         types.RequestID("control-plane-forward"),
		message:    &jsonrpc.Request{ID: id, Method: "ping"},
		enqueuedAt: time.Now(),
		polledAt:   time.Now(),
		headers: http.Header{
			"X-Request-Id": []string{"cp-command-id"},
		},
		shardToken: "shard-control-plane",
	}

	require.NoError(t, processor.Process(ctx, cmd))

	resp := responder.waitForResponse(t)
	require.Equal(t, "cp-command-id", resp.controlPlaneCommandRequestID)
	require.Equal(t, cmd.id, resp.requestID)
}

func TestProcessorRecordsEndToEndLatency(t *testing.T) {
	t.Parallel()

	reader := sdkmetric.NewManualReader()
	meterProvider := sdkmetric.NewMeterProvider(sdkmetric.WithReader(reader))
	controlPlaneCfg := newTestControlPlaneConfig(t)
	t.Cleanup(func() {
		_ = meterProvider.Shutdown(context.Background())
	})

	logger := slog.New(slog.NewTextHandler(io.Discard, &slog.HandlerOptions{Level: slog.LevelDebug}))
	responder := newRecordingResponder()

	id, err := jsonrpc.MakeID("latency-request")
	require.NoError(t, err)

	transport := &stubForwardingTransport{
		conn: &stubForwardingConnection{
			response: &jsonrpc.Response{
				ID:     id,
				Result: json.RawMessage(`{"ok":true}`),
			},
		},
	}

	processor, err := NewProcessor(processorParams{
		Logger:          logger,
		ChannelBindings: newTestChannelBindings(transport),
		TunnelResponder: responder,
		MCPConfig:       newTestMCPConfig(t, time.Second),
		OAuthHTTPClient: &http.Client{},
		ControlPlaneCfg: controlPlaneCfg,
		MeterProvider:   meterProvider,
	})
	require.NoError(t, err)

	enqueuedAt := time.Now().Add(-750 * time.Millisecond)

	cmd := &fakePolledCommand{
		id:         types.RequestID("latency-request-id"),
		message:    &jsonrpc.Request{ID: id, Method: "ping"},
		enqueuedAt: enqueuedAt,
		polledAt:   enqueuedAt,
		shardToken: "shard-latency",
	}

	require.NoError(t, processor.Process(context.Background(), cmd))
	_ = responder.waitForResponse(t)

	var rm metricdata.ResourceMetrics
	require.NoError(t, reader.Collect(context.Background(), &rm))

	histogram, ok := findHistogram(rm, metricNameCommandEndToEndLatency)
	require.True(t, ok, "command_end_to_end_latency_milliseconds metric not found")
	require.Len(t, histogram.DataPoints, 2)

	dpByType := dataPointsByLatencyType(t, histogram.DataPoints)

	enqueuedDP := dpByType["enqueue_to_response"]
	require.EqualValues(t, 1, enqueuedDP.Count)
	require.InDelta(t, float64(time.Since(enqueuedAt)/time.Millisecond), enqueuedDP.Sum, 250)

	pollDP := dpByType["poll_to_response"]
	require.EqualValues(t, 1, pollDP.Count)
	require.Greater(t, pollDP.Sum, 0.0)

	for _, dp := range []metricdata.HistogramDataPoint[float64]{enqueuedDP, pollDP} {
		requestKind, ok := dp.Attributes.Value(attribute.Key("request_kind"))
		require.True(t, ok)
		require.Equal(t, "call", requestKind.AsString())

		tunnelID, ok := dp.Attributes.Value(attribute.Key("tunnel_id"))
		require.True(t, ok)
		require.Equal(t, "test-tunnel", tunnelID.AsString())

		status, ok := dp.Attributes.Value(attribute.Key("tunnel_service_status"))
		require.True(t, ok)
		require.EqualValues(t, http.StatusOK, status.AsInt64())
	}
}

func TestProcessorRecordsNotificationLatency(t *testing.T) {
	t.Parallel()

	reader := sdkmetric.NewManualReader()
	meterProvider := sdkmetric.NewMeterProvider(sdkmetric.WithReader(reader))
	controlPlaneCfg := newTestControlPlaneConfig(t)
	t.Cleanup(func() {
		_ = meterProvider.Shutdown(context.Background())
	})

	logger := slog.New(slog.NewTextHandler(io.Discard, &slog.HandlerOptions{Level: slog.LevelDebug}))
	responder := newRecordingResponder()

	transport := &stubForwardingTransport{
		conn: &stubForwardingConnection{
			responseHeaders: http.Header{},
		},
	}

	processor, err := NewProcessor(processorParams{
		Logger:          logger,
		ChannelBindings: newTestChannelBindings(transport),
		TunnelResponder: responder,
		MCPConfig:       newTestMCPConfig(t, time.Second),
		OAuthHTTPClient: &http.Client{},
		ControlPlaneCfg: controlPlaneCfg,
		MeterProvider:   meterProvider,
	})
	require.NoError(t, err)

	enqueuedAt := time.Now().Add(-500 * time.Millisecond)

	cmd := &fakePolledCommand{
		id:         types.RequestID("notification-latency"),
		message:    &jsonrpc.Request{Method: "notifications/initialized"},
		enqueuedAt: enqueuedAt,
		polledAt:   enqueuedAt,
		shardToken: "shard-notification-latency",
	}

	require.NoError(t, processor.Process(context.Background(), cmd))
	_ = responder.waitForResponse(t)

	var rm metricdata.ResourceMetrics
	require.NoError(t, reader.Collect(context.Background(), &rm))

	histogram, ok := findHistogram(rm, metricNameCommandEndToEndLatency)
	require.True(t, ok, "command_end_to_end_latency_milliseconds metric not found")
	require.Len(t, histogram.DataPoints, 2)
	dpByType := dataPointsByLatencyType(t, histogram.DataPoints)

	enqueuedDP := dpByType["enqueue_to_response"]
	require.EqualValues(t, 1, enqueuedDP.Count)
	require.InDelta(t, float64(time.Since(enqueuedAt)/time.Millisecond), enqueuedDP.Sum, 250)

	pollDP := dpByType["poll_to_response"]
	require.EqualValues(t, 1, pollDP.Count)
	require.Greater(t, pollDP.Sum, 0.0)

	for _, dp := range []metricdata.HistogramDataPoint[float64]{enqueuedDP, pollDP} {
		requestKind, ok := dp.Attributes.Value(attribute.Key("request_kind"))
		require.True(t, ok)
		require.Equal(t, "notification", requestKind.AsString())

		tunnelID, ok := dp.Attributes.Value(attribute.Key("tunnel_id"))
		require.True(t, ok)
		require.Equal(t, "test-tunnel", tunnelID.AsString())

		status, ok := dp.Attributes.Value(attribute.Key("tunnel_service_status"))
		require.True(t, ok)
		require.EqualValues(t, http.StatusOK, status.AsInt64())
	}
}

func TestProcessorConnectFailureReturnsTerminalErrorResponseAndRecordsLatency(t *testing.T) {
	t.Parallel()

	reader := sdkmetric.NewManualReader()
	meterProvider := sdkmetric.NewMeterProvider(sdkmetric.WithReader(reader))
	t.Cleanup(func() {
		_ = meterProvider.Shutdown(context.Background())
	})

	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	responder := newRecordingResponder()
	transport := &failingForwardingTransport{err: errors.New("connect failed")}
	callID, err := jsonrpc.MakeID("connect-failure-call")
	require.NoError(t, err)

	processor, err := NewProcessor(processorParams{
		Logger:          logger,
		ChannelBindings: newTestChannelBindings(transport),
		TunnelResponder: responder,
		MCPConfig:       newTestMCPConfig(t, time.Second),
		OAuthHTTPClient: &http.Client{},
		ControlPlaneCfg: newTestControlPlaneConfig(t),
		MeterProvider:   meterProvider,
	})
	require.NoError(t, err)

	enqueuedAt := time.Now().Add(-500 * time.Millisecond)

	cmd := &fakePolledCommand{
		id:         types.RequestID("connect-failure"),
		message:    &jsonrpc.Request{ID: callID, Method: "initialize"},
		enqueuedAt: enqueuedAt,
		polledAt:   enqueuedAt,
		shardToken: "shard-connect-failure",
	}

	require.NoError(t, processor.Process(context.Background(), cmd))

	resp := responder.waitForResponse(t)
	elapsedAtResponseMS := float64(time.Since(enqueuedAt) / time.Millisecond)
	require.Equal(t, cmd.id, resp.requestID)
	require.Equal(t, types.ResponseTypeJSONRPCResponse, resp.response.Type())
	require.Equal(t, http.StatusBadGateway, resp.response.ResponseCode())
	require.Equal(t, "application/json", resp.response.Headers().Get("Content-Type"))

	rpcResp := decodeJSONRPCResponse(t, resp.response.Payload())
	require.NotNil(t, rpcResp)
	require.Equal(t, callID, rpcResp.ID)
	require.NotNil(t, rpcResp.Error)
	require.Contains(t, rpcResp.Error.Error(), "Bad Gateway")
	require.Contains(t, rpcResp.Error.Error(), "connect failed")

	var rm metricdata.ResourceMetrics
	require.NoError(t, reader.Collect(context.Background(), &rm))

	histogram, ok := findHistogram(rm, metricNameCommandEndToEndLatency)
	require.True(t, ok, "command_end_to_end_latency_milliseconds metric not found")
	require.Len(t, histogram.DataPoints, 2)

	dpByType := dataPointsByLatencyType(t, histogram.DataPoints)

	enqueuedDP := dpByType["enqueue_to_response"]
	require.EqualValues(t, 1, enqueuedDP.Count)
	require.InDelta(t, elapsedAtResponseMS, enqueuedDP.Sum, 250)

	pollDP := dpByType["poll_to_response"]
	require.EqualValues(t, 1, pollDP.Count)
	require.Greater(t, pollDP.Sum, 0.0)

	for _, dp := range []metricdata.HistogramDataPoint[float64]{enqueuedDP, pollDP} {
		requestKind, ok := dp.Attributes.Value(attribute.Key("request_kind"))
		require.True(t, ok)
		require.Equal(t, "call", requestKind.AsString())

		requestMethod, ok := dp.Attributes.Value(attribute.Key("request_method"))
		require.True(t, ok)
		require.Equal(t, "initialize", requestMethod.AsString())

		tunnelID, ok := dp.Attributes.Value(attribute.Key("tunnel_id"))
		require.True(t, ok)
		require.Equal(t, "test-tunnel", tunnelID.AsString())

		status, ok := dp.Attributes.Value(attribute.Key("tunnel_service_status"))
		require.True(t, ok)
		require.EqualValues(t, http.StatusBadGateway, status.AsInt64())
	}
}

func TestProcessorRejectsNonRequestJSONRPCMessage(t *testing.T) {
	t.Parallel()

	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	responder := newRecordingResponder()
	transport := &stubForwardingTransport{conn: &stubForwardingConnection{}}

	meterProvider := newTestMeterProvider(t)
	processor, err := NewProcessor(processorParams{
		Logger:          logger,
		ChannelBindings: newTestChannelBindings(transport),
		TunnelResponder: responder,
		MCPConfig:       newTestMCPConfig(t, time.Second),
		OAuthHTTPClient: &http.Client{},
		ControlPlaneCfg: newTestControlPlaneConfig(t),
		MeterProvider:   meterProvider,
	})
	require.NoError(t, err)

	cmd := &fakePolledCommand{
		id:         types.RequestID("non-request-message"),
		message:    &jsonrpc.Response{}, // not a *jsonrpc.Request
		enqueuedAt: time.Now(),
		polledAt:   time.Now(),
		shardToken: "shard-non-request",
	}

	err = processor.Process(context.Background(), cmd)
	require.Error(t, err)
	require.Contains(t, err.Error(), "unexpected command type")

	select {
	case resp := <-responder.responses:
		t.Fatalf("unexpected response posted: %+v", resp)
	default:
	}
}

func TestProcessorRejectsNilCommand(t *testing.T) {
	t.Parallel()

	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	responder := newRecordingResponder()
	transport := &stubForwardingTransport{conn: &stubForwardingConnection{}}

	meterProvider := newTestMeterProvider(t)
	processor, err := NewProcessor(processorParams{
		Logger:          logger,
		ChannelBindings: newTestChannelBindings(transport),
		TunnelResponder: responder,
		MCPConfig:       newTestMCPConfig(t, time.Second),
		OAuthHTTPClient: &http.Client{},
		ControlPlaneCfg: newTestControlPlaneConfig(t),
		MeterProvider:   meterProvider,
	})
	require.NoError(t, err)

	err = processor.Process(context.Background(), nil)
	require.Error(t, err)
	require.Contains(t, err.Error(), "nil command")

	select {
	case resp := <-responder.responses:
		t.Fatalf("unexpected response posted: %+v", resp)
	default:
	}
}

func TestProcessorRejectsUnknownPolledCommandType(t *testing.T) {
	t.Parallel()

	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	responder := newRecordingResponder()
	transport := &stubForwardingTransport{conn: &stubForwardingConnection{}}

	meterProvider := newTestMeterProvider(t)
	processor, err := NewProcessor(processorParams{
		Logger:          logger,
		ChannelBindings: newTestChannelBindings(transport),
		TunnelResponder: responder,
		MCPConfig:       newTestMCPConfig(t, time.Second),
		OAuthHTTPClient: &http.Client{},
		ControlPlaneCfg: newTestControlPlaneConfig(t),
		MeterProvider:   meterProvider,
	})
	require.NoError(t, err)

	cmd := &unknownPolledCommand{
		id:         types.RequestID("unknown-polled-command"),
		enqueuedAt: time.Now(),
		polledAt:   time.Now(),
		shardToken: "shard-unknown-polled-command",
	}

	err = processor.Process(context.Background(), cmd)
	require.Error(t, err)
	require.Contains(t, err.Error(), "unexpected command type")

	select {
	case resp := <-responder.responses:
		t.Fatalf("unexpected response posted: %+v", resp)
	default:
	}
}

func TestNewProcessorValidationErrors(t *testing.T) {
	t.Parallel()

	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	responder := newRecordingResponder()
	transport := &stubForwardingTransport{conn: &stubForwardingConnection{}}
	meterProvider := newTestMeterProvider(t)

	validMCP := newTestMCPConfig(t, time.Second)
	validControlPlane := newTestControlPlaneConfig(t)
	validOAuthClient := &http.Client{}

	tests := []struct {
		name   string
		params processorParams
	}{
		{
			name:   "nil_logger",
			params: processorParams{Logger: nil, ChannelBindings: newTestChannelBindings(transport), TunnelResponder: responder, MCPConfig: validMCP, OAuthHTTPClient: validOAuthClient, ControlPlaneCfg: validControlPlane, MeterProvider: meterProvider},
		},
		{
			name:   "nil_channel_bindings",
			params: processorParams{Logger: logger, ChannelBindings: nil, TunnelResponder: responder, MCPConfig: validMCP, OAuthHTTPClient: validOAuthClient, ControlPlaneCfg: validControlPlane, MeterProvider: meterProvider},
		},
		{
			name:   "nil_responder",
			params: processorParams{Logger: logger, ChannelBindings: newTestChannelBindings(transport), TunnelResponder: nil, MCPConfig: validMCP, OAuthHTTPClient: validOAuthClient, ControlPlaneCfg: validControlPlane, MeterProvider: meterProvider},
		},
		{
			name:   "nil_mcp_config",
			params: processorParams{Logger: logger, ChannelBindings: newTestChannelBindings(transport), TunnelResponder: responder, MCPConfig: nil, OAuthHTTPClient: validOAuthClient, ControlPlaneCfg: validControlPlane, MeterProvider: meterProvider},
		},
		{
			name: "non_positive_ttl",
			params: processorParams{
				Logger:          logger,
				ChannelBindings: newTestChannelBindings(transport),
				TunnelResponder: responder,
				MCPConfig: func() *config.MCPConfig {
					cfg := *validMCP
					cfg.ConnectionMaxTTL = 0
					return &cfg
				}(),
				OAuthHTTPClient: validOAuthClient,
				ControlPlaneCfg: validControlPlane,
				MeterProvider:   meterProvider,
			},
		},
		{
			name:   "nil_control_plane_cfg",
			params: processorParams{Logger: logger, ChannelBindings: newTestChannelBindings(transport), TunnelResponder: responder, MCPConfig: validMCP, OAuthHTTPClient: validOAuthClient, ControlPlaneCfg: nil, MeterProvider: meterProvider},
		},
		{
			name:   "nil_meter_provider",
			params: processorParams{Logger: logger, ChannelBindings: newTestChannelBindings(transport), TunnelResponder: responder, MCPConfig: validMCP, OAuthHTTPClient: validOAuthClient, ControlPlaneCfg: validControlPlane, MeterProvider: nil},
		},
		{
			name:   "nil_oauth_http_client",
			params: processorParams{Logger: logger, ChannelBindings: newTestChannelBindings(transport), TunnelResponder: responder, MCPConfig: validMCP, OAuthHTTPClient: nil, ControlPlaneCfg: validControlPlane, MeterProvider: meterProvider},
		},
		{
			name: "missing_mcp_server_url",
			params: processorParams{
				Logger:          logger,
				ChannelBindings: newTestChannelBindings(transport),
				TunnelResponder: responder,
				MCPConfig: func() *config.MCPConfig {
					cfg := *validMCP
					cfg.ServerURL = nil
					return &cfg
				}(),
				OAuthHTTPClient: validOAuthClient,
				ControlPlaneCfg: validControlPlane,
				MeterProvider:   meterProvider,
			},
		},
		{
			name: "duplicate_default_channel",
			params: processorParams{
				Logger: logger,
				ChannelBindings: map[types.Channel]ChannelBinding{
					types.DefaultChannel: {
						Transport:     transport,
						Priority:      0,
						SupportsMCP:   true,
						SupportsOAuth: true,
					},
					types.Channel(" MAIN "): {
						Transport:     &stubForwardingTransport{},
						Priority:      0,
						SupportsMCP:   true,
						SupportsOAuth: false,
					},
					types.ChannelHarpoon: {
						Transport:     &stubForwardingTransport{},
						Priority:      0,
						SupportsMCP:   true,
						SupportsOAuth: false,
					},
				},
				TunnelResponder: responder,
				MCPConfig:       validMCP,
				OAuthHTTPClient: validOAuthClient,
				ControlPlaneCfg: validControlPlane,
				MeterProvider:   meterProvider,
			},
		},
		{
			name: "nil_named_channel_transport",
			params: processorParams{
				Logger: logger,
				ChannelBindings: map[types.Channel]ChannelBinding{
					types.DefaultChannel: {
						Transport:     transport,
						Priority:      0,
						SupportsMCP:   true,
						SupportsOAuth: true,
					},
					types.ChannelHarpoon: {
						Priority:      0,
						SupportsMCP:   true,
						SupportsOAuth: false,
					},
				},
				TunnelResponder: responder,
				MCPConfig:       validMCP,
				OAuthHTTPClient: validOAuthClient,
				ControlPlaneCfg: validControlPlane,
				MeterProvider:   meterProvider,
			},
		},
		{
			name: "non_main_supports_oauth",
			params: processorParams{
				Logger: logger,
				ChannelBindings: map[types.Channel]ChannelBinding{
					types.DefaultChannel: {
						Transport:     transport,
						Priority:      0,
						SupportsMCP:   true,
						SupportsOAuth: true,
					},
					types.ChannelHarpoon: {
						Transport:     &stubForwardingTransport{},
						Priority:      0,
						SupportsMCP:   true,
						SupportsOAuth: true,
					},
				},
				TunnelResponder: responder,
				MCPConfig:       validMCP,
				OAuthHTTPClient: validOAuthClient,
				ControlPlaneCfg: validControlPlane,
				MeterProvider:   meterProvider,
			},
		},
	}

	for _, tc := range tests {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			_, err := NewProcessor(tc.params)
			require.Error(t, err)
		})
	}
}

func TestProcessorReturnsBadGatewayOnWriteErrorWithoutStatusCode(t *testing.T) {
	t.Parallel()

	logger := slog.New(slog.NewTextHandler(io.Discard, &slog.HandlerOptions{Level: slog.LevelDebug}))
	responder := newRecordingResponder()

	callID, err := jsonrpc.MakeID("no-status")
	require.NoError(t, err)

	transport := &stubForwardingTransport{conn: &scriptedForwardingConnection{
		statusCode: 0,
		writeErr:   errors.New("write failed"),
	}}

	meterProvider := newTestMeterProvider(t)
	processor, err := NewProcessor(processorParams{
		Logger:          logger,
		ChannelBindings: newTestChannelBindings(transport),
		TunnelResponder: responder,
		MCPConfig:       newTestMCPConfig(t, time.Second),
		OAuthHTTPClient: &http.Client{},
		ControlPlaneCfg: newTestControlPlaneConfig(t),
		MeterProvider:   meterProvider,
	})
	require.NoError(t, err)

	cmd := &fakePolledCommand{
		id:         types.RequestID("no-status-request"),
		message:    &jsonrpc.Request{ID: callID, Method: "ping"},
		enqueuedAt: time.Now(),
		polledAt:   time.Now(),
		shardToken: "shard-no-status",
	}

	require.NoError(t, processor.Process(context.Background(), cmd))

	got := responder.waitForResponse(t)
	require.Equal(t, http.StatusBadGateway, got.response.ResponseCode())
}

func TestProcessorLogsMCPUpstreamErrorDetails(t *testing.T) {
	t.Parallel()

	var buf bytes.Buffer
	logger := slog.New(slog.NewTextHandler(&buf, &slog.HandlerOptions{Level: slog.LevelDebug}))
	responder := newRecordingResponder()
	responder.tunnelServiceRequestID = types.TunnelServiceRequestID("req_ts_post")

	callID, err := jsonrpc.MakeID("upstream-status")
	require.NoError(t, err)

	transport := &stubForwardingTransport{conn: &scriptedForwardingConnection{
		statusCode: http.StatusMethodNotAllowed,
		headers: http.Header{
			"Content-Type": []string{"application/json"},
		},
	}}

	mcpConfig := newTestMCPConfig(t, time.Second)
	serverURL, err := url.Parse("https://internal.example.com/mcp?token=secret")
	require.NoError(t, err)
	mcpConfig.ServerURL = serverURL
	meterProvider := newTestMeterProvider(t)
	processor, err := NewProcessor(processorParams{
		Logger:          logger,
		ChannelBindings: newTestChannelBindings(transport),
		TunnelResponder: responder,
		MCPConfig:       mcpConfig,
		OAuthHTTPClient: &http.Client{},
		ControlPlaneCfg: newTestControlPlaneConfig(t),
		MeterProvider:   meterProvider,
	})
	require.NoError(t, err)

	cmd := &fakePolledCommand{
		id:         types.RequestID("upstream-status-request"),
		message:    &jsonrpc.Request{ID: callID, Method: "initialize"},
		enqueuedAt: time.Now(),
		polledAt:   time.Now(),
		shardToken: "shard-upstream-status",
	}

	require.NoError(t, processor.Process(context.Background(), cmd))
	got := responder.waitForResponse(t)
	require.Equal(t, http.StatusMethodNotAllowed, got.response.ResponseCode())

	logOutput := buf.String()
	require.Contains(t, logOutput, "dispatcher received MCP upstream error; posted error response to control plane")
	require.Contains(t, logOutput, "status_code=405")
	require.Contains(t, logOutput, "request_id=upstream-status-request")
	require.Contains(t, logOutput, "rpc_method=initialize")
	require.Contains(t, logOutput, "mcp_server_host=internal.example.com")
	require.Contains(t, logOutput, "mcp_server_path=/mcp")
	require.Contains(t, logOutput, "mcp_server_query_redacted=true")
	require.Contains(t, logOutput, "tunnel_request_id=req_ts_post")
	require.NotContains(t, logOutput, "secret")
}

func TestProcessorLogsJSONRPCErrorResponseCorrelation(t *testing.T) {
	t.Parallel()

	var buf bytes.Buffer
	logger := slog.New(slog.NewTextHandler(&buf, &slog.HandlerOptions{Level: slog.LevelDebug}))
	responder := newRecordingResponder()
	responder.tunnelServiceRequestID = types.TunnelServiceRequestID("post_req_tools_list")

	callID, err := jsonrpc.MakeID("tools-list-rpc")
	require.NoError(t, err)

	transport := &stubForwardingTransport{conn: &scriptedForwardingConnection{
		statusCode: http.StatusOK,
		headers: http.Header{
			"Content-Type": []string{"application/json"},
		},
		readSteps: []readStep{{
			msg: &jsonrpc.Response{
				ID: callID,
				Error: &jsonrpc.Error{
					Code:    jsonrpc.CodeMethodNotFound,
					Message: "method-not-found",
					Data:    json.RawMessage(`{"access_token":"secret-token"}`),
				},
			},
		}},
	}}

	meterProvider := newTestMeterProvider(t)
	processor, err := NewProcessor(processorParams{
		Logger:          logger,
		ChannelBindings: newTestChannelBindings(transport),
		TunnelResponder: responder,
		MCPConfig:       newTestMCPConfig(t, time.Second),
		OAuthHTTPClient: &http.Client{},
		ControlPlaneCfg: newTestControlPlaneConfig(t),
		MeterProvider:   meterProvider,
	})
	require.NoError(t, err)

	cmd := &fakePolledCommand{
		id:         types.RequestID("tools-list-request"),
		message:    &jsonrpc.Request{ID: callID, Method: "tools/list"},
		enqueuedAt: time.Now(),
		polledAt:   time.Now(),
		headers: http.Header{
			"X-Request-Id": []string{"cmd_req_tools_list"},
		},
		shardToken: "shard-tools-list",
	}

	require.NoError(t, processor.Process(context.Background(), cmd))
	got := responder.waitForResponse(t)
	require.Equal(t, cmd.id, got.requestID)
	require.Equal(t, "cmd_req_tools_list", got.controlPlaneCommandRequestID)

	logOutput := buf.String()
	require.Contains(t, logOutput, "dispatcher delivered response to control plane")
	require.Contains(t, logOutput, "request_id=tools-list-request")
	require.Contains(t, logOutput, "cmd_request_id=cmd_req_tools_list")
	require.Contains(t, logOutput, "tunnel_request_id=post_req_tools_list")
	require.Contains(t, logOutput, "rpc_request_id=tools-list-rpc")
	require.Contains(t, logOutput, "rpc_method=tools/list")
	require.Contains(t, logOutput, "rpc_response_id=tools-list-rpc")
	require.Contains(t, logOutput, "has_error=true")
	require.Contains(t, logOutput, "rpc_error_code=-32601")
	require.Contains(t, logOutput, "rpc_error_message=method-not-found")
	require.NotContains(t, logOutput, "access_token")
	require.NotContains(t, logOutput, "secret-token")
}

func TestProcessorNotificationAckPostFailureIsReturned(t *testing.T) {
	t.Parallel()

	logger := slog.New(slog.NewTextHandler(io.Discard, &slog.HandlerOptions{Level: slog.LevelDebug}))
	responder := &failingResponder{err: errors.New("post failed")}

	transport := &stubForwardingTransport{conn: &scriptedForwardingConnection{statusCode: http.StatusOK}}

	meterProvider := newTestMeterProvider(t)
	processor, err := NewProcessor(processorParams{
		Logger:          logger,
		ChannelBindings: newTestChannelBindings(transport),
		TunnelResponder: responder,
		MCPConfig:       newTestMCPConfig(t, time.Second),
		OAuthHTTPClient: &http.Client{},
		ControlPlaneCfg: newTestControlPlaneConfig(t),
		MeterProvider:   meterProvider,
	})
	require.NoError(t, err)

	// Notification (no ID) should be acknowledged; this tests the error path when posting that ack fails.
	cmd := &fakePolledCommand{
		id:         types.RequestID("notif-ack-post-failure"),
		message:    &jsonrpc.Request{Method: "notifications/initialized"},
		enqueuedAt: time.Now(),
		polledAt:   time.Now(),
		shardToken: "shard-notif-ack",
	}

	err = processor.Process(context.Background(), cmd)
	require.Error(t, err)
	require.Contains(t, err.Error(), "post failed")
}

func TestProcessorForwardResponsesStopsOnEOF(t *testing.T) {
	t.Parallel()

	logger := slog.New(slog.NewTextHandler(io.Discard, &slog.HandlerOptions{Level: slog.LevelDebug}))
	responder := newRecordingResponder()

	callID, err := jsonrpc.MakeID("eof-before-response")
	require.NoError(t, err)

	transport := &stubForwardingTransport{conn: &scriptedForwardingConnection{
		statusCode: http.StatusOK,
		// No read steps => io.EOF on first read.
	}}

	meterProvider := newTestMeterProvider(t)
	processor, err := NewProcessor(processorParams{
		Logger:          logger,
		ChannelBindings: newTestChannelBindings(transport),
		TunnelResponder: responder,
		MCPConfig:       newTestMCPConfig(t, time.Second),
		OAuthHTTPClient: &http.Client{},
		ControlPlaneCfg: newTestControlPlaneConfig(t),
		MeterProvider:   meterProvider,
	})
	require.NoError(t, err)

	cmd := &fakePolledCommand{
		id:         types.RequestID("eof-before-response-request"),
		message:    &jsonrpc.Request{ID: callID, Method: "ping"},
		enqueuedAt: time.Now(),
		polledAt:   time.Now(),
		shardToken: "shard-eof",
	}

	require.NoError(t, processor.Process(context.Background(), cmd))
	assertTerminalJSONRPCErrorResponse(t, responder, cmd, http.StatusBadGateway, "Bad Gateway", io.EOF.Error())
}

func TestProcessorForwardResponsesStopsOnNilMessage(t *testing.T) {
	t.Parallel()

	logger := slog.New(slog.NewTextHandler(io.Discard, &slog.HandlerOptions{Level: slog.LevelDebug}))
	responder := newRecordingResponder()

	callID, err := jsonrpc.MakeID("nil-msg")
	require.NoError(t, err)

	transport := &stubForwardingTransport{conn: &scriptedForwardingConnection{
		statusCode: http.StatusOK,
		readSteps: []readStep{
			{msg: nil, err: nil},
		},
	}}

	meterProvider := newTestMeterProvider(t)
	processor, err := NewProcessor(processorParams{
		Logger:          logger,
		ChannelBindings: newTestChannelBindings(transport),
		TunnelResponder: responder,
		MCPConfig:       newTestMCPConfig(t, time.Second),
		OAuthHTTPClient: &http.Client{},
		ControlPlaneCfg: newTestControlPlaneConfig(t),
		MeterProvider:   meterProvider,
	})
	require.NoError(t, err)

	cmd := &fakePolledCommand{
		id:         types.RequestID("nil-msg-request"),
		message:    &jsonrpc.Request{ID: callID, Method: "ping"},
		enqueuedAt: time.Now(),
		polledAt:   time.Now(),
		shardToken: "shard-nil-msg",
	}

	require.NoError(t, processor.Process(context.Background(), cmd))
	assertTerminalJSONRPCErrorResponse(t, responder, cmd, http.StatusBadGateway, "Bad Gateway", "received nil message from MCP server without error")
}

func TestProcessorForwardResponsesStopsOnConnectionClosed(t *testing.T) {
	t.Parallel()

	logger := slog.New(slog.NewTextHandler(io.Discard, &slog.HandlerOptions{Level: slog.LevelDebug}))
	responder := newRecordingResponder()

	callID, err := jsonrpc.MakeID("conn-closed")
	require.NoError(t, err)

	transport := &stubForwardingTransport{conn: &scriptedForwardingConnection{
		statusCode: http.StatusOK,
		readSteps:  []readStep{{err: mcp.ErrConnectionClosed}},
	}}

	meterProvider := newTestMeterProvider(t)
	processor, err := NewProcessor(processorParams{
		Logger:          logger,
		ChannelBindings: newTestChannelBindings(transport),
		TunnelResponder: responder,
		MCPConfig:       newTestMCPConfig(t, time.Second),
		OAuthHTTPClient: &http.Client{},
		ControlPlaneCfg: newTestControlPlaneConfig(t),
		MeterProvider:   meterProvider,
	})
	require.NoError(t, err)

	cmd := &fakePolledCommand{
		id:         types.RequestID("conn-closed-request"),
		message:    &jsonrpc.Request{ID: callID, Method: "ping"},
		enqueuedAt: time.Now(),
		polledAt:   time.Now(),
		shardToken: "shard-conn-closed",
	}

	require.NoError(t, processor.Process(context.Background(), cmd))
	assertTerminalJSONRPCErrorResponse(t, responder, cmd, http.StatusBadGateway, "Bad Gateway", mcp.ErrConnectionClosed.Error())
}

func TestProcessorForwardResponsesStopsOnEncodeError(t *testing.T) {
	t.Parallel()

	logger := slog.New(slog.NewTextHandler(io.Discard, &slog.HandlerOptions{Level: slog.LevelDebug}))
	responder := newRecordingResponder()

	callID, err := jsonrpc.MakeID("encode-error")
	require.NoError(t, err)

	transport := &stubForwardingTransport{conn: &scriptedForwardingConnection{
		statusCode: http.StatusOK,
		readSteps: []readStep{
			// Invalid JSON payload should cause jsonrpc.EncodeMessage to fail.
			{msg: &jsonrpc.Response{ID: callID, Result: json.RawMessage(`not-json`)}, err: nil},
		},
	}}

	meterProvider := newTestMeterProvider(t)
	processor, err := NewProcessor(processorParams{
		Logger:          logger,
		ChannelBindings: newTestChannelBindings(transport),
		TunnelResponder: responder,
		MCPConfig:       newTestMCPConfig(t, time.Second),
		OAuthHTTPClient: &http.Client{},
		ControlPlaneCfg: newTestControlPlaneConfig(t),
		MeterProvider:   meterProvider,
	})
	require.NoError(t, err)

	cmd := &fakePolledCommand{
		id:         types.RequestID("encode-error-request"),
		message:    &jsonrpc.Request{ID: callID, Method: "ping"},
		enqueuedAt: time.Now(),
		polledAt:   time.Now(),
		shardToken: "shard-encode-error",
	}

	require.NoError(t, processor.Process(context.Background(), cmd))
	assertTerminalJSONRPCErrorResponse(t, responder, cmd, http.StatusBadGateway, "Bad Gateway", "invalid character")
}

func TestProcessorForwardResponsesStopsOnReadError(t *testing.T) {
	t.Parallel()

	logger := slog.New(slog.NewTextHandler(io.Discard, &slog.HandlerOptions{Level: slog.LevelDebug}))
	responder := newRecordingResponder()

	callID, err := jsonrpc.MakeID("read-error")
	require.NoError(t, err)

	transport := &stubForwardingTransport{conn: &scriptedForwardingConnection{
		statusCode: http.StatusOK,
		readSteps:  []readStep{{err: errors.New("read failed")}},
	}}

	meterProvider := newTestMeterProvider(t)
	processor, err := NewProcessor(processorParams{
		Logger:          logger,
		ChannelBindings: newTestChannelBindings(transport),
		TunnelResponder: responder,
		MCPConfig:       newTestMCPConfig(t, time.Second),
		OAuthHTTPClient: &http.Client{},
		ControlPlaneCfg: newTestControlPlaneConfig(t),
		MeterProvider:   meterProvider,
	})
	require.NoError(t, err)

	cmd := &fakePolledCommand{
		id:         types.RequestID("read-error-request"),
		message:    &jsonrpc.Request{ID: callID, Method: "ping"},
		enqueuedAt: time.Now(),
		polledAt:   time.Now(),
		shardToken: "shard-read-error",
	}

	require.NoError(t, processor.Process(context.Background(), cmd))
	assertTerminalJSONRPCErrorResponse(t, responder, cmd, http.StatusBadGateway, "Bad Gateway", "read failed")
}

func TestProcessorForwardResponsesPostFailureStopsForwarding(t *testing.T) {
	t.Parallel()

	logger := slog.New(slog.NewTextHandler(io.Discard, &slog.HandlerOptions{Level: slog.LevelDebug}))
	responder := &countingResponder{err: errors.New("post failed")}

	callID, err := jsonrpc.MakeID("post-failure-forward")
	require.NoError(t, err)

	transport := &stubForwardingTransport{conn: &scriptedForwardingConnection{
		statusCode: http.StatusOK,
		readSteps: []readStep{
			{msg: &jsonrpc.Response{ID: callID, Result: json.RawMessage(`{"ok":true}`)}, err: nil},
		},
	}}

	meterProvider := newTestMeterProvider(t)
	processor, err := NewProcessor(processorParams{
		Logger:          logger,
		ChannelBindings: newTestChannelBindings(transport),
		TunnelResponder: responder,
		MCPConfig:       newTestMCPConfig(t, time.Second),
		OAuthHTTPClient: &http.Client{},
		ControlPlaneCfg: newTestControlPlaneConfig(t),
		MeterProvider:   meterProvider,
	})
	require.NoError(t, err)

	cmd := &fakePolledCommand{
		id:         types.RequestID("post-failure-forward-request"),
		message:    &jsonrpc.Request{ID: callID, Method: "ping"},
		enqueuedAt: time.Now(),
		polledAt:   time.Now(),
		shardToken: "shard-post-failure-forward",
	}

	require.NoError(t, processor.Process(context.Background(), cmd))
	require.EqualValues(t, 1, responder.calls.Load())
}

func TestBuildJSONRPCErrorResponseAndRequestKindAttributes(t *testing.T) {
	t.Parallel()

	_, err := buildJSONRPCErrorResponse(nil, http.StatusBadRequest, errors.New("boom"))
	require.Error(t, err)

	id, err := jsonrpc.MakeID("req-1")
	require.NoError(t, err)
	req := &jsonrpc.Request{ID: id, Method: "ping"}

	payload, err := buildJSONRPCErrorResponse(req, 999, errors.New("boom"))
	require.NoError(t, err)
	require.NotEmpty(t, payload)

	callAttrs := requestKindAttributes(req)
	require.NotEmpty(t, callAttrs)

	notifAttrs := requestKindAttributes(&jsonrpc.Request{Method: "notifications/initialized"})
	require.NotEmpty(t, notifAttrs)
	require.Nil(t, requestKindAttributes(nil))
}

func TestProcessorForwardResponsesForwardsUpstreamNotificationRequests(t *testing.T) {
	t.Parallel()

	logger := slog.New(slog.NewTextHandler(io.Discard, &slog.HandlerOptions{Level: slog.LevelDebug}))
	responder := newRecordingResponder()

	callID, err := jsonrpc.MakeID("ignore-upstream-notif")
	require.NoError(t, err)

	transport := &stubForwardingTransport{conn: &scriptedForwardingConnection{
		statusCode: http.StatusOK,
		readSteps: []readStep{
			// Notification from the MCP server (no ID) should be ignored.
			{msg: &jsonrpc.Request{Method: "notifications/progress"}, err: nil},
			{msg: &jsonrpc.Response{ID: callID, Result: json.RawMessage(`{"ok":true}`)}, err: nil},
		},
	}}

	meterProvider := newTestMeterProvider(t)
	processor, err := NewProcessor(processorParams{
		Logger:          logger,
		ChannelBindings: newTestChannelBindings(transport),
		TunnelResponder: responder,
		MCPConfig:       newTestMCPConfig(t, time.Second),
		OAuthHTTPClient: &http.Client{},
		ControlPlaneCfg: newTestControlPlaneConfig(t),
		MeterProvider:   meterProvider,
	})
	require.NoError(t, err)

	cmd := &fakePolledCommand{
		id:         types.RequestID("ignore-upstream-notif-request"),
		message:    &jsonrpc.Request{ID: callID, Method: "ping"},
		enqueuedAt: time.Now(),
		polledAt:   time.Now(),
		shardToken: "shard-ignore-upstream-notif",
	}

	require.NoError(t, processor.Process(context.Background(), cmd))
	got := responder.waitForResponses(t, 2)
	require.Len(t, got, 2)

	notif := got[0]
	require.Equal(t, cmd.id, notif.requestID)
	require.Equal(t, types.ResponseTypeJSONRPCNotification, notif.response.Type())
	require.Equal(t, "text/event-stream", notif.response.Headers().Get("Content-Type"))

	notifMsg, err := jsonrpc.DecodeMessage(notif.response.Payload())
	require.NoError(t, err)
	notifReq, ok := notifMsg.(*jsonrpc.Request)
	require.True(t, ok)
	require.False(t, notifReq.ID.IsValid())
	require.Equal(t, "notifications/progress", notifReq.Method)

	final := got[1]
	require.Equal(t, cmd.id, final.requestID)
	require.Equal(t, types.ResponseTypeJSONRPCResponse, final.response.Type())
}

func TestProcessorForwardResponsesClosesConnectionWhenNotificationForwardingFails(t *testing.T) {
	t.Parallel()

	logger := slog.New(slog.NewTextHandler(io.Discard, &slog.HandlerOptions{Level: slog.LevelDebug}))

	callID, err := jsonrpc.MakeID("notification-forward-failure")
	require.NoError(t, err)

	conn := &scriptedForwardingConnection{
		statusCode: http.StatusOK,
		readSteps: []readStep{
			{msg: &jsonrpc.Request{Method: "notifications/progress"}, err: nil},
			{msg: &jsonrpc.Response{ID: callID, Result: json.RawMessage(`{"ok":true}`)}, err: nil},
		},
	}
	transport := &stubForwardingTransport{conn: conn}

	meterProvider := newTestMeterProvider(t)
	processor, err := NewProcessor(processorParams{
		Logger:          logger,
		ChannelBindings: newTestChannelBindings(transport),
		TunnelResponder: &failingResponder{err: errors.New("notification post failed")},
		MCPConfig:       newTestMCPConfig(t, time.Second),
		OAuthHTTPClient: &http.Client{},
		ControlPlaneCfg: newTestControlPlaneConfig(t),
		MeterProvider:   meterProvider,
	})
	require.NoError(t, err)

	cmd := &fakePolledCommand{
		id:         types.RequestID("notification-forward-failure-request"),
		message:    &jsonrpc.Request{ID: callID, Method: "ping"},
		enqueuedAt: time.Now(),
		polledAt:   time.Now(),
		shardToken: "shard-notification-forward-failure",
	}

	require.NoError(t, processor.Process(context.Background(), cmd))

	conn.mu.Lock()
	defer conn.mu.Unlock()
	require.True(t, conn.closed)
}

func TestProcessorForwardResponsesStopsOnNonResponseMessage(t *testing.T) {
	t.Parallel()

	logger := slog.New(slog.NewTextHandler(io.Discard, &slog.HandlerOptions{Level: slog.LevelDebug}))
	responder := newRecordingResponder()

	callID, err := jsonrpc.MakeID("non-response")
	require.NoError(t, err)

	transport := &stubForwardingTransport{conn: &scriptedForwardingConnection{
		statusCode: http.StatusOK,
		readSteps: []readStep{
			// A request with an ID is not a notification; forwardResponses should treat it as an error.
			{msg: &jsonrpc.Request{ID: callID, Method: "unexpected"}, err: nil},
		},
	}}

	meterProvider := newTestMeterProvider(t)
	processor, err := NewProcessor(processorParams{
		Logger:          logger,
		ChannelBindings: newTestChannelBindings(transport),
		TunnelResponder: responder,
		MCPConfig:       newTestMCPConfig(t, time.Second),
		OAuthHTTPClient: &http.Client{},
		ControlPlaneCfg: newTestControlPlaneConfig(t),
		MeterProvider:   meterProvider,
	})
	require.NoError(t, err)

	cmd := &fakePolledCommand{
		id:         types.RequestID("non-response-request"),
		message:    &jsonrpc.Request{ID: callID, Method: "ping"},
		enqueuedAt: time.Now(),
		polledAt:   time.Now(),
		shardToken: "shard-non-response",
	}

	require.NoError(t, processor.Process(context.Background(), cmd))

	select {
	case resp := <-responder.responses:
		t.Fatalf("unexpected response posted: %+v", resp)
	default:
	}
}

func TestProcessorForwardResponsesStopsOnIDMismatch(t *testing.T) {
	t.Parallel()

	logger := slog.New(slog.NewTextHandler(io.Discard, &slog.HandlerOptions{Level: slog.LevelDebug}))
	responder := newRecordingResponder()

	callID, err := jsonrpc.MakeID("id-mismatch-call")
	require.NoError(t, err)
	otherID, err := jsonrpc.MakeID("id-mismatch-other")
	require.NoError(t, err)

	transport := &stubForwardingTransport{conn: &scriptedForwardingConnection{
		statusCode: http.StatusOK,
		readSteps: []readStep{
			{msg: &jsonrpc.Response{ID: otherID, Result: json.RawMessage(`{"ok":true}`)}, err: nil},
		},
	}}

	meterProvider := newTestMeterProvider(t)
	processor, err := NewProcessor(processorParams{
		Logger:          logger,
		ChannelBindings: newTestChannelBindings(transport),
		TunnelResponder: responder,
		MCPConfig:       newTestMCPConfig(t, time.Second),
		OAuthHTTPClient: &http.Client{},
		ControlPlaneCfg: newTestControlPlaneConfig(t),
		MeterProvider:   meterProvider,
	})
	require.NoError(t, err)

	cmd := &fakePolledCommand{
		id:         types.RequestID("id-mismatch-request"),
		message:    &jsonrpc.Request{ID: callID, Method: "ping"},
		enqueuedAt: time.Now(),
		polledAt:   time.Now(),
		shardToken: "shard-id-mismatch",
	}

	require.NoError(t, processor.Process(context.Background(), cmd))

	select {
	case resp := <-responder.responses:
		t.Fatalf("unexpected response posted: %+v", resp)
	default:
	}
}

func TestProcessorForwardResponsesStopsWhenConnectionTTLReached(t *testing.T) {
	t.Parallel()

	logger := slog.New(slog.NewTextHandler(io.Discard, &slog.HandlerOptions{Level: slog.LevelDebug}))
	responder := newRecordingResponder()

	callID, err := jsonrpc.MakeID("ttl-reached")
	require.NoError(t, err)

	transport := &stubForwardingTransport{conn: &scriptedForwardingConnection{
		statusCode: http.StatusOK,
		readSteps: []readStep{
			{blockUntilDone: true},
		},
	}}

	meterProvider := newTestMeterProvider(t)
	processor, err := NewProcessor(processorParams{
		Logger:          logger,
		ChannelBindings: newTestChannelBindings(transport),
		TunnelResponder: responder,
		MCPConfig:       newTestMCPConfig(t, 15*time.Millisecond),
		OAuthHTTPClient: &http.Client{},
		ControlPlaneCfg: newTestControlPlaneConfig(t),
		MeterProvider:   meterProvider,
	})
	require.NoError(t, err)

	cmd := &fakePolledCommand{
		id:         types.RequestID("ttl-reached-request"),
		message:    &jsonrpc.Request{ID: callID, Method: "ping"},
		enqueuedAt: time.Now(),
		polledAt:   time.Now(),
		shardToken: "shard-ttl",
	}

	require.NoError(t, processor.Process(context.Background(), cmd))

	select {
	case resp := <-responder.responses:
		t.Fatalf("unexpected response posted: %+v", resp)
	default:
	}
}

func TestProcessorErrorResponsePostFailureIsReturned(t *testing.T) {
	t.Parallel()

	logger := slog.New(slog.NewTextHandler(io.Discard, &slog.HandlerOptions{Level: slog.LevelDebug}))
	responder := &failingResponder{err: errors.New("post failed")}

	callID, err := jsonrpc.MakeID("post-error")
	require.NoError(t, err)

	transport := &stubForwardingTransport{conn: &scriptedForwardingConnection{
		statusCode: http.StatusUnauthorized,
		writeErr:   errors.New("unauthorized"),
	}}

	meterProvider := newTestMeterProvider(t)
	processor, err := NewProcessor(processorParams{
		Logger:          logger,
		ChannelBindings: newTestChannelBindings(transport),
		TunnelResponder: responder,
		MCPConfig:       newTestMCPConfig(t, time.Second),
		OAuthHTTPClient: &http.Client{},
		ControlPlaneCfg: newTestControlPlaneConfig(t),
		MeterProvider:   meterProvider,
	})
	require.NoError(t, err)

	cmd := &fakePolledCommand{
		id:         types.RequestID("post-error-request"),
		message:    &jsonrpc.Request{ID: callID, Method: "initialize"},
		enqueuedAt: time.Now(),
		polledAt:   time.Now(),
		shardToken: "shard-post-error",
	}

	err = processor.Process(context.Background(), cmd)
	require.Error(t, err)
	require.Contains(t, err.Error(), "post failed")
}

func TestProcessorOAuthDiscoveryPostFailureIsReturned(t *testing.T) {
	t.Parallel()

	var expectedResource string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = fmt.Fprintf(w, `{"resource":"%s","scopes_supported":["read"]}`, expectedResource)
	}))
	t.Cleanup(server.Close)

	serverURL, err := url.Parse(server.URL)
	require.NoError(t, err)
	expectedResource = serverURL.String()

	cfg := &config.MCPConfig{
		ServerURL:             serverURL,
		ConnectionMaxTTL:      time.Second,
		MaxConcurrentRequests: 1,
	}
	logger := slog.New(slog.NewTextHandler(io.Discard, &slog.HandlerOptions{Level: slog.LevelDebug}))
	responder := &failingResponder{err: context.Canceled}
	transport := &stubForwardingTransport{conn: &stubForwardingConnection{}}
	meterProvider := newTestMeterProvider(t)

	processor, err := NewProcessor(processorParams{
		Logger:          logger,
		ChannelBindings: newTestChannelBindings(transport),
		TunnelResponder: responder,
		MCPConfig:       cfg,
		OAuthHTTPClient: &http.Client{},
		ControlPlaneCfg: newTestControlPlaneConfig(t),
		MeterProvider:   meterProvider,
	})
	require.NoError(t, err)

	cmd := &fakeOauthDiscoveryCommand{
		id:         types.RequestID("oauth-post-error"),
		enqueuedAt: time.Now(),
		polledAt:   time.Now(),
		headers:    http.Header{},
		shardToken: "shard-oauth-post-error",
	}

	err = processor.Process(context.Background(), cmd)
	require.ErrorIs(t, err, context.Canceled)
}

func TestProcessorRequiresShardToken(t *testing.T) {
	t.Parallel()

	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	responder := newRecordingResponder()
	transport := &stubForwardingTransport{conn: &stubForwardingConnection{}}

	meterProvider := newTestMeterProvider(t)
	processor, err := NewProcessor(processorParams{
		Logger:          logger,
		ChannelBindings: newTestChannelBindings(transport),
		TunnelResponder: responder,
		MCPConfig:       newTestMCPConfig(t, time.Second),
		OAuthHTTPClient: &http.Client{},
		ControlPlaneCfg: newTestControlPlaneConfig(t),
		MeterProvider:   meterProvider,
	})
	require.NoError(t, err)

	cmd := &fakePolledCommand{
		id:         types.RequestID("missing-shard"),
		message:    &jsonrpc.Request{Method: "ping"},
		enqueuedAt: time.Now(),
		polledAt:   time.Now(),
	}

	err = processor.Process(context.Background(), cmd)
	require.Error(t, err)
	require.Contains(t, err.Error(), "missing shard token")
}

func TestProcessorHandlesOAuthDiscoveryCommand(t *testing.T) {
	t.Parallel()

	var expectedResource string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = fmt.Fprintf(w, `{"resource":"%s","scopes_supported":["read"]}`, expectedResource)
	}))
	t.Cleanup(server.Close)

	serverURL, err := url.Parse(server.URL)
	require.NoError(t, err)
	expectedResource = serverURL.String()

	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	responder := newRecordingResponder()
	transport := &stubForwardingTransport{conn: &stubForwardingConnection{}}
	meterProvider := newTestMeterProvider(t)

	cfg := &config.MCPConfig{
		ServerURL:             serverURL,
		ConnectionMaxTTL:      2 * time.Second,
		MaxConcurrentRequests: 1,
	}
	processor, err := NewProcessor(processorParams{
		Logger:          logger,
		ChannelBindings: newTestChannelBindings(transport),
		TunnelResponder: responder,
		MCPConfig:       cfg,
		OAuthHTTPClient: &http.Client{},
		ControlPlaneCfg: newTestControlPlaneConfig(t),
		MeterProvider:   meterProvider,
	})
	require.NoError(t, err)

	cmd := &fakeOauthDiscoveryCommand{
		id:         types.RequestID("oauth-1"),
		enqueuedAt: time.Now().Add(-time.Second),
		polledAt:   time.Now(),
		headers:    http.Header{},
		shardToken: "shard-oauth",
	}

	err = processor.Process(context.Background(), cmd)
	require.NoError(t, err)

	got := responder.waitForResponse(t)
	require.Equal(t, cmd.id, got.requestID)
	require.Equal(t, types.ResponseTypeOAuthDiscovery, got.response.Type())
	require.Equal(t, http.StatusOK, got.response.ResponseCode())
	require.NotEmpty(t, got.response.Payload())
	require.Equal(t, "application/json", got.response.Headers().Get("Content-Type"))
}

func TestProcessorHandlesSessionTerminationCommand(t *testing.T) {
	t.Parallel()

	transport := &sessionTerminatingForwardingTransport{
		statusCode:      http.StatusMethodNotAllowed,
		responseHeaders: http.Header{"Mcp-Protocol-Version": {"2025-06-18"}},
	}
	responder := newRecordingResponder()
	processor, err := NewProcessor(processorParams{
		Logger:          slog.New(slog.NewTextHandler(io.Discard, nil)),
		ChannelBindings: newTestChannelBindings(transport),
		TunnelResponder: responder,
		MCPConfig:       newTestMCPConfig(t, time.Second),
		OAuthHTTPClient: &http.Client{},
		ControlPlaneCfg: newTestControlPlaneConfig(t),
		MeterProvider:   newTestMeterProvider(t),
	})
	require.NoError(t, err)

	sessionID := "session-terminate"
	cmd := &fakeSessionTerminationCommand{
		id:         types.RequestID("session-terminate"),
		enqueuedAt: time.Now().Add(-time.Second),
		polledAt:   time.Now(),
		headers: http.Header{
			mcpclient.HeaderSessionID:       {sessionID},
			mcpclient.HeaderProtocolVersion: {"2025-06-18"},
		},
		sessionID:  &sessionID,
		shardToken: "shard-session-terminate",
	}

	require.NoError(t, processor.Process(context.Background(), cmd))

	got := responder.waitForResponse(t)
	require.Equal(t, cmd.id, got.requestID)
	require.Equal(t, types.ResponseTypeSessionTermination, got.response.Type())
	require.Equal(t, http.StatusMethodNotAllowed, got.response.ResponseCode())
	require.Equal(t, "2025-06-18", got.response.Headers().Get("Mcp-Protocol-Version"))
	require.Equal(t, cmd.headers, transport.terminationHeaders)
}

func TestProcessorClonesSessionTerminationHeadersBeforeTransport(t *testing.T) {
	t.Parallel()

	transport := &sessionTerminatingForwardingTransport{
		statusCode:      http.StatusNoContent,
		responseHeaders: http.Header{"Mcp-Protocol-Version": {"2025-06-18"}},
		mutateHeaders: func(headers http.Header) {
			headers.Set("Authorization", "Bearer mutated-by-transport")
			headers.Set(mcpclient.HeaderProtocolVersion, "mutated-protocol")
			headers.Set("X-Transport-Only", "set-by-transport")
		},
	}
	responder := newRecordingResponder()
	processor, err := NewProcessor(processorParams{
		Logger:          slog.New(slog.NewTextHandler(io.Discard, nil)),
		ChannelBindings: newTestChannelBindings(transport),
		TunnelResponder: responder,
		MCPConfig:       newTestMCPConfig(t, time.Second),
		OAuthHTTPClient: &http.Client{},
		ControlPlaneCfg: newTestControlPlaneConfig(t),
		MeterProvider:   newTestMeterProvider(t),
	})
	require.NoError(t, err)

	sessionID := "session-terminate-clone"
	originalHeaders := http.Header{
		"Authorization":                 {"Bearer connector-token"},
		mcpclient.HeaderSessionID:       {sessionID},
		mcpclient.HeaderProtocolVersion: {"2025-06-18"},
	}
	cmd := &fakeSessionTerminationCommand{
		id:         types.RequestID("session-terminate-clone"),
		enqueuedAt: time.Now().Add(-time.Second),
		polledAt:   time.Now(),
		headers:    originalHeaders,
		sessionID:  &sessionID,
		shardToken: "shard-session-terminate-clone",
	}

	require.NoError(t, processor.Process(context.Background(), cmd))

	got := responder.waitForResponse(t)
	require.Equal(t, cmd.id, got.requestID)
	require.Equal(t, types.ResponseTypeSessionTermination, got.response.Type())
	require.Equal(t, http.StatusNoContent, got.response.ResponseCode())
	require.Equal(t, []string{"Bearer connector-token"}, transport.terminationHeaders["Authorization"])
	require.Equal(t, []string{"2025-06-18"}, transport.terminationHeaders[mcpclient.HeaderProtocolVersion])
	require.Equal(t, []string{"Bearer connector-token"}, originalHeaders["Authorization"])
	require.Equal(t, []string{"2025-06-18"}, originalHeaders[mcpclient.HeaderProtocolVersion])
	require.Empty(t, originalHeaders.Values("X-Transport-Only"))
}

func TestProcessorRejectsSessionTerminationForUnsupportedTransport(t *testing.T) {
	t.Parallel()

	responder := newRecordingResponder()
	processor, err := NewProcessor(processorParams{
		Logger: slog.New(slog.NewTextHandler(io.Discard, nil)),
		ChannelBindings: map[types.Channel]ChannelBinding{
			types.DefaultChannel: {
				Transport:                  &stubForwardingTransport{conn: &stubForwardingConnection{}},
				Priority:                   0,
				SupportsMCP:                true,
				SupportsOAuth:              true,
				SupportsSessionTermination: false,
			},
			types.ChannelHarpoon: {
				Transport:                  &stubForwardingTransport{conn: &stubForwardingConnection{}},
				Priority:                   0,
				Routable:                   func() bool { return false },
				SupportsMCP:                true,
				SupportsOAuth:              false,
				SupportsSessionTermination: false,
			},
		},
		TunnelResponder: responder,
		MCPConfig:       newTestMCPConfig(t, time.Second),
		OAuthHTTPClient: &http.Client{},
		ControlPlaneCfg: newTestControlPlaneConfig(t),
		MeterProvider:   newTestMeterProvider(t),
	})
	require.NoError(t, err)

	sessionID := "session-terminate"
	cmd := &fakeSessionTerminationCommand{
		id:         types.RequestID("session-terminate"),
		enqueuedAt: time.Now().Add(-time.Second),
		polledAt:   time.Now(),
		headers: http.Header{
			mcpclient.HeaderSessionID:       {sessionID},
			mcpclient.HeaderProtocolVersion: {"2025-06-18"},
		},
		sessionID:  &sessionID,
		shardToken: "shard-session-terminate",
	}

	require.NoError(t, processor.Process(context.Background(), cmd))

	got := responder.waitForResponse(t)
	require.Equal(t, cmd.id, got.requestID)
	require.Equal(t, types.ResponseTypeSessionTermination, got.response.Type())
	require.Equal(t, http.StatusMethodNotAllowed, got.response.ResponseCode())
}

func TestProcessorOAuthDiscoveryPublishesHostBundle(t *testing.T) {
	t.Parallel()

	mux := http.NewServeMux()
	server := httptest.NewServer(mux)
	t.Cleanup(server.Close)

	issuer := server.URL + "/issuer-0"
	prmdPayload := map[string]any{
		"resource":              server.URL + "/mcp",
		"authorization_servers": []string{issuer},
		"scopes_supported":      []string{"mcp:tools"},
	}
	mux.HandleFunc("/.well-known/oauth-protected-resource/mcp", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		require.NoError(t, json.NewEncoder(w).Encode(prmdPayload))
	})
	authMetaPayload := map[string]any{
		"issuer":                 issuer,
		"authorization_endpoint": issuer + "/authorize",
		"token_endpoint":         issuer + "/token",
		"registration_endpoint":  issuer + "/register",
	}
	mux.HandleFunc("/issuer-0/.well-known/oauth-authorization-server", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		require.NoError(t, json.NewEncoder(w).Encode(authMetaPayload))
	})

	serverURL, err := url.Parse(server.URL + "/mcp")
	require.NoError(t, err)

	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	responder := newRecordingResponder()
	transport := &stubForwardingTransport{conn: &stubForwardingConnection{}}
	hostBus := newRecordingHostBus()
	meterProvider := newTestMeterProvider(t)
	cfg := &config.MCPConfig{
		ServerURL:             serverURL,
		UnixSocketPath:        "/tmp/appgarden-dcr.sock",
		ConnectionMaxTTL:      2 * time.Second,
		MaxConcurrentRequests: 1,
	}

	processor, err := NewProcessor(processorParams{
		Logger:          logger,
		ChannelBindings: newTestChannelBindings(transport),
		TunnelResponder: responder,
		MCPConfig:       cfg,
		OAuthHTTPClient: &http.Client{},
		HostBus:         hostBus,
		ControlPlaneCfg: newTestControlPlaneConfig(t),
		MeterProvider:   meterProvider,
	})
	require.NoError(t, err)

	cmd := &fakeOauthDiscoveryCommand{
		id:         types.RequestID("oauth-discovery-publish"),
		enqueuedAt: time.Now().Add(-time.Second),
		polledAt:   time.Now(),
		headers:    http.Header{},
		shardToken: "shard-oauth-discovery-publish",
	}

	require.NoError(t, processor.Process(context.Background(), cmd))
	_ = responder.waitForResponse(t)

	bundle := hostBus.waitForBundle(t)
	require.NotEmpty(t, bundle.URLs)
	roles := make(map[string]bool, len(bundle.URLs))
	for _, record := range bundle.URLs {
		require.Equal(t, cfg.UnixSocketPath, record.UnixSocketPath)
		for _, tag := range record.Tags {
			if tag.Key == hostbus.TagKeyRole {
				roles[tag.Value] = true
			}
		}
	}
	require.True(t, roles["prmd-source"], "expected prmd-source role in host bundle")
	require.True(
		t,
		roles["auth-server-metadata"],
		"expected auth-server-metadata role in host bundle",
	)
}

func TestProcessorNormalizesZeroStatusCodeForJSONRPCResponses(t *testing.T) {
	t.Parallel()

	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	responder := newRecordingResponder()

	id, err := jsonrpc.MakeID("req-0-status")
	require.NoError(t, err)

	req := &jsonrpc.Request{
		ID:     id,
		Method: "ping",
	}

	conn := &stubForwardingConnection{
		statusCode:      0,
		responseHeaders: http.Header{"Content-Type": {"application/json"}},
		response:        &jsonrpc.Response{ID: id, Result: json.RawMessage(`{}`)},
	}
	transport := &stubForwardingTransport{conn: conn}
	meterProvider := newTestMeterProvider(t)

	processor, err := NewProcessor(processorParams{
		Logger:          logger,
		ChannelBindings: newTestChannelBindings(transport),
		TunnelResponder: responder,
		MCPConfig:       newTestMCPConfig(t, time.Second),
		OAuthHTTPClient: &http.Client{},
		ControlPlaneCfg: newTestControlPlaneConfig(t),
		MeterProvider:   meterProvider,
	})
	require.NoError(t, err)

	cmd := &fakePolledCommand{
		id:         types.RequestID("status-0-ok"),
		message:    req,
		enqueuedAt: time.Now(),
		polledAt:   time.Now(),
		headers:    http.Header{},
		shardToken: "shard-status-0-ok",
	}

	err = processor.Process(context.Background(), cmd)
	require.NoError(t, err)

	got := responder.waitForResponse(t)
	require.Equal(t, cmd.id, got.requestID)
	require.Equal(t, http.StatusOK, got.response.ResponseCode())
}

func TestProcessorNormalizesZeroStatusCodeForNotificationAck(t *testing.T) {
	t.Parallel()

	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	responder := newRecordingResponder()

	// A JSON-RPC notification has no ID.
	req := &jsonrpc.Request{
		Method: "notify",
	}

	conn := &stubForwardingConnection{
		statusCode:      0,
		responseHeaders: http.Header{"X-Test": {"ok"}},
	}
	transport := &stubForwardingTransport{conn: conn}
	meterProvider := newTestMeterProvider(t)

	processor, err := NewProcessor(processorParams{
		Logger:          logger,
		ChannelBindings: newTestChannelBindings(transport),
		TunnelResponder: responder,
		MCPConfig:       newTestMCPConfig(t, time.Second),
		OAuthHTTPClient: &http.Client{},
		ControlPlaneCfg: newTestControlPlaneConfig(t),
		MeterProvider:   meterProvider,
	})
	require.NoError(t, err)

	cmd := &fakePolledCommand{
		id:         types.RequestID("status-0-ack"),
		message:    req,
		enqueuedAt: time.Now(),
		polledAt:   time.Now(),
		headers:    http.Header{},
		shardToken: "shard-status-0-ack",
	}

	err = processor.Process(context.Background(), cmd)
	require.NoError(t, err)

	got := responder.waitForResponse(t)
	require.Equal(t, cmd.id, got.requestID)
	require.Equal(t, types.ResponseTypeNotificationAcknowledgment, got.response.Type())
	require.Equal(t, http.StatusOK, got.response.ResponseCode())
}

type recordingResponder struct {
	responses              chan tunnelResponse
	tunnelServiceRequestID types.TunnelServiceRequestID
}

type recordingHostBus struct {
	bundles chan hostbus.URLBundle
}

type tunnelResponse struct {
	requestID                    types.RequestID
	response                     *types.TunnelResponse
	controlPlaneCommandRequestID string
}

func newRecordingResponder() *recordingResponder {
	return &recordingResponder{
		responses: make(chan tunnelResponse, 8),
	}
}

func newRecordingHostBus() *recordingHostBus {
	return &recordingHostBus{
		bundles: make(chan hostbus.URLBundle, 8),
	}
}

func (r *recordingResponder) PostResponse(ctx context.Context, requestID types.RequestID, response *types.TunnelResponse) (types.TunnelServiceRequestID, error) {
	var controlPlaneRequestID string
	if id, ok := tunnelctx.ControlPlaneCommandRequestIDFromContext(ctx); ok {
		controlPlaneRequestID = id.String()
	}

	select {
	case r.responses <- tunnelResponse{requestID: requestID, response: response, controlPlaneCommandRequestID: controlPlaneRequestID}:
		return r.tunnelServiceRequestID, nil
	case <-ctx.Done():
		return "", ctx.Err()
	}
}

func (r *recordingResponder) waitForResponse(t *testing.T) tunnelResponse {
	t.Helper()

	select {
	case resp := <-r.responses:
		return resp
	case <-time.After(2 * time.Second):
		t.Fatalf("timed out waiting for response")
		return tunnelResponse{}
	}
}

func (r *recordingResponder) waitForResponses(t *testing.T, n int) []tunnelResponse {
	t.Helper()

	out := make([]tunnelResponse, 0, n)
	for len(out) < n {
		out = append(out, r.waitForResponse(t))
	}
	return out
}

func (r *recordingHostBus) Publish(ctx context.Context, bundle hostbus.URLBundle) error {
	select {
	case r.bundles <- bundle:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

func (r *recordingHostBus) Close() error {
	return nil
}

func (r *recordingHostBus) waitForBundle(t *testing.T) hostbus.URLBundle {
	t.Helper()

	select {
	case bundle := <-r.bundles:
		return bundle
	case <-time.After(2 * time.Second):
		t.Fatalf("timed out waiting for host bundle")
		return hostbus.URLBundle{}
	}
}

func newTestMeterProvider(t *testing.T) *sdkmetric.MeterProvider {
	t.Helper()
	provider := sdkmetric.NewMeterProvider()
	t.Cleanup(func() {
		_ = provider.Shutdown(context.Background())
	})
	return provider
}

func newTestControlPlaneConfig(t *testing.T) *config.ControlPlaneConfig {
	t.Helper()
	return &config.ControlPlaneConfig{
		TunnelID: types.TunnelID("test-tunnel"),
	}
}

func newTestMCPConfig(t *testing.T, ttl time.Duration) *config.MCPConfig {
	t.Helper()

	serverURL, err := url.Parse("https://example.com/mcp")
	require.NoError(t, err)

	if ttl <= 0 {
		ttl = time.Second
	}

	cfg := &config.MCPConfig{
		ServerURL:             serverURL,
		ConnectionMaxTTL:      ttl,
		MaxConcurrentRequests: 2,
	}
	return cfg
}

func newTestChannelBindings(defaultTransport mcpclient.ForwardingTransport) map[types.Channel]ChannelBinding {
	return map[types.Channel]ChannelBinding{
		types.DefaultChannel: {
			Transport:                  defaultTransport,
			Priority:                   0,
			SupportsMCP:                true,
			SupportsOAuth:              true,
			SupportsSessionTermination: true,
		},
		types.ChannelHarpoon: {
			Transport:                  &stubForwardingTransport{conn: &stubForwardingConnection{}},
			Priority:                   0,
			Routable:                   func() bool { return false },
			SupportsMCP:                true,
			SupportsOAuth:              false,
			SupportsSessionTermination: false,
		},
	}
}

func newTestHarpoonRegistry(t *testing.T) *harpoon.Registry {
	t.Helper()

	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	baseURL, err := url.Parse("https://example.com")
	require.NoError(t, err)

	registry, err := harpoon.NewRegistry(logger, false, []harpoon.Target{{
		Label:   "auth",
		BaseURL: baseURL,
	}})
	require.NoError(t, err)
	return registry
}

func findHistogram(rm metricdata.ResourceMetrics, name string) (metricdata.Histogram[float64], bool) {
	for _, scope := range rm.ScopeMetrics {
		for _, m := range scope.Metrics {
			if m.Name != name {
				continue
			}
			histogram, ok := m.Data.(metricdata.Histogram[float64])
			if !ok {
				return metricdata.Histogram[float64]{}, false
			}
			return histogram, true
		}
	}
	return metricdata.Histogram[float64]{}, false
}

func findCounter(rm metricdata.ResourceMetrics, name string) (metricdata.Sum[int64], bool) {
	for _, scope := range rm.ScopeMetrics {
		for _, m := range scope.Metrics {
			if m.Name != name {
				continue
			}
			sum, ok := m.Data.(metricdata.Sum[int64])
			if !ok {
				return metricdata.Sum[int64]{}, false
			}
			return sum, true
		}
	}
	return metricdata.Sum[int64]{}, false
}

func dataPointsByLatencyType(t *testing.T, dps []metricdata.HistogramDataPoint[float64]) map[string]metricdata.HistogramDataPoint[float64] {
	t.Helper()
	out := make(map[string]metricdata.HistogramDataPoint[float64])
	for _, dp := range dps {
		latencyType, ok := dp.Attributes.Value(attribute.Key("latency_type"))
		if !ok {
			continue
		}
		out[latencyType.AsString()] = dp
	}
	require.Contains(t, out, "enqueue_to_response")
	require.Contains(t, out, "poll_to_response")
	return out
}

type stubForwardingTransport struct {
	conn mcpclient.ForwardingConnection
}

func (s *stubForwardingTransport) Connect(context.Context) (mcpclient.ForwardingConnection, error) {
	return s.conn, nil
}

type countingForwardingTransport struct {
	conn  mcpclient.ForwardingConnection
	calls atomic.Int32
}

func (t *countingForwardingTransport) Connect(context.Context) (mcpclient.ForwardingConnection, error) {
	t.calls.Add(1)
	return t.conn, nil
}

type failingForwardingTransport struct {
	err error
}

func (s *failingForwardingTransport) Connect(context.Context) (mcpclient.ForwardingConnection, error) {
	return nil, s.err
}

type sessionTerminatingForwardingTransport struct {
	stubForwardingTransport
	statusCode         int
	responseHeaders    http.Header
	err                error
	terminationHeaders http.Header
	mutateHeaders      func(http.Header)
}

func (s *sessionTerminatingForwardingTransport) TerminateSession(_ context.Context, headers http.Header) (int, http.Header, error) {
	s.terminationHeaders = headers.Clone()
	if s.mutateHeaders != nil {
		s.mutateHeaders(headers)
	}
	return s.statusCode, s.responseHeaders, s.err
}

type countingMCPTransport struct {
	conn  mcp.Connection
	calls atomic.Int32
}

func (t *countingMCPTransport) Connect(context.Context) (mcp.Connection, error) {
	t.calls.Add(1)
	return t.conn, nil
}

type stubForwardingConnection struct {
	responseHeaders    http.Header
	response           jsonrpc.Message
	statusCode         int
	writeErr           error
	writeHeaders       http.Header
	mutateWriteHeaders func(http.Header)
}

func (c *stubForwardingConnection) Write(_ context.Context, headers http.Header, _ jsonrpc.Message) (int, http.Header, error) {
	if headers == nil {
		c.writeHeaders = nil
	} else {
		c.writeHeaders = headers.Clone()
		if c.mutateWriteHeaders != nil {
			c.mutateWriteHeaders(headers)
		}
	}
	return c.statusCode, c.responseHeaders, c.writeErr
}

func (c *stubForwardingConnection) Read(context.Context) (jsonrpc.Message, error) {
	if c.response == nil {
		return nil, io.EOF
	}
	msg := c.response
	c.response = nil
	return msg, nil
}

func (c *stubForwardingConnection) Close() error { return nil }

type stubMCPConnection struct {
	writeErr error
	readMsg  jsonrpc.Message
}

func (c *stubMCPConnection) Write(context.Context, jsonrpc.Message) error {
	return c.writeErr
}

func (c *stubMCPConnection) Read(context.Context) (jsonrpc.Message, error) {
	if c.readMsg == nil {
		return nil, io.EOF
	}
	msg := c.readMsg
	c.readMsg = nil
	return msg, nil
}

func (c *stubMCPConnection) Close() error { return nil }

func (c *stubMCPConnection) SessionID() string { return "" }

type readStep struct {
	msg            jsonrpc.Message
	err            error
	blockUntilDone bool
}

type scriptedForwardingConnection struct {
	statusCode int
	headers    http.Header
	writeErr   error

	mu        sync.Mutex
	closed    bool
	readSteps []readStep
}

func (c *scriptedForwardingConnection) Write(context.Context, http.Header, jsonrpc.Message) (int, http.Header, error) {
	return c.statusCode, c.headers, c.writeErr
}

func (c *scriptedForwardingConnection) Read(ctx context.Context) (jsonrpc.Message, error) {
	c.mu.Lock()
	defer c.mu.Unlock()

	if len(c.readSteps) == 0 {
		return nil, io.EOF
	}
	step := c.readSteps[0]
	c.readSteps = c.readSteps[1:]

	if step.blockUntilDone {
		<-ctx.Done()
		return nil, ctx.Err()
	}

	return step.msg, step.err
}

func (c *scriptedForwardingConnection) Close() error {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.closed = true
	return nil
}

type fakePolledCommand struct {
	id         types.RequestID
	message    jsonrpc.Message
	enqueuedAt time.Time
	polledAt   time.Time
	headers    http.Header
	sessionID  *string
	shardToken string
	channel    types.Channel
}

func (f *fakePolledCommand) RequestID() types.RequestID {
	return f.id
}

func (f *fakePolledCommand) Message() jsonrpc.Message {
	return f.message
}

func (f *fakePolledCommand) EnqueuedAt() time.Time {
	return f.enqueuedAt
}

func (f *fakePolledCommand) PolledAt() time.Time {
	return f.polledAt
}

func (f *fakePolledCommand) Headers() http.Header {
	return f.headers
}

func (f *fakePolledCommand) ShardToken() string {
	return f.shardToken
}

func (f *fakePolledCommand) Channel() types.Channel {
	if f.channel == "" {
		return types.DefaultChannel
	}
	return f.channel
}

func (f *fakePolledCommand) SessionID() (string, bool) {
	if f.sessionID == nil {
		return "", false
	}
	return *f.sessionID, true
}

type fakeOauthDiscoveryCommand struct {
	id         types.RequestID
	enqueuedAt time.Time
	polledAt   time.Time
	headers    http.Header
	shardToken string
	channel    types.Channel
}

func (f *fakeOauthDiscoveryCommand) RequestID() types.RequestID { return f.id }
func (f *fakeOauthDiscoveryCommand) EnqueuedAt() time.Time      { return f.enqueuedAt }
func (f *fakeOauthDiscoveryCommand) PolledAt() time.Time        { return f.polledAt }
func (f *fakeOauthDiscoveryCommand) Headers() http.Header       { return f.headers }
func (f *fakeOauthDiscoveryCommand) ShardToken() string         { return f.shardToken }
func (f *fakeOauthDiscoveryCommand) Channel() types.Channel {
	if f.channel == "" {
		return types.DefaultChannel
	}
	return f.channel
}
func (f *fakeOauthDiscoveryCommand) SessionID() (string, bool) { return "", false }
func (f *fakeOauthDiscoveryCommand) IsOAuthDiscovery() bool    { return true }

type fakeSessionTerminationCommand struct {
	id         types.RequestID
	enqueuedAt time.Time
	polledAt   time.Time
	headers    http.Header
	sessionID  *string
	shardToken string
	channel    types.Channel
}

func (f *fakeSessionTerminationCommand) RequestID() types.RequestID { return f.id }
func (f *fakeSessionTerminationCommand) EnqueuedAt() time.Time      { return f.enqueuedAt }
func (f *fakeSessionTerminationCommand) PolledAt() time.Time        { return f.polledAt }
func (f *fakeSessionTerminationCommand) Headers() http.Header       { return f.headers }
func (f *fakeSessionTerminationCommand) ShardToken() string         { return f.shardToken }
func (f *fakeSessionTerminationCommand) Channel() types.Channel {
	if f.channel == "" {
		return types.DefaultChannel
	}
	return f.channel
}
func (f *fakeSessionTerminationCommand) SessionID() (string, bool) {
	if f.sessionID == nil {
		return "", false
	}
	return *f.sessionID, true
}
func (f *fakeSessionTerminationCommand) IsSessionTermination() bool { return true }

type unknownPolledCommand struct {
	id         types.RequestID
	enqueuedAt time.Time
	polledAt   time.Time
	headers    http.Header
	sessionID  *string
	shardToken string
	channel    types.Channel
}

func (f *unknownPolledCommand) RequestID() types.RequestID { return f.id }
func (f *unknownPolledCommand) EnqueuedAt() time.Time      { return f.enqueuedAt }
func (f *unknownPolledCommand) PolledAt() time.Time        { return f.polledAt }
func (f *unknownPolledCommand) Headers() http.Header       { return f.headers }
func (f *unknownPolledCommand) ShardToken() string         { return f.shardToken }
func (f *unknownPolledCommand) Channel() types.Channel {
	if f.channel == "" {
		return types.DefaultChannel
	}
	return f.channel
}
func (f *unknownPolledCommand) SessionID() (string, bool) {
	if f.sessionID == nil {
		return "", false
	}
	return *f.sessionID, true
}

type failingResponder struct {
	err error
}

func (r *failingResponder) PostResponse(context.Context, types.RequestID, *types.TunnelResponse) (types.TunnelServiceRequestID, error) {
	return "", r.err
}

type countingResponder struct {
	err   error
	calls atomic.Int32
}

func (r *countingResponder) PostResponse(context.Context, types.RequestID, *types.TunnelResponse) (types.TunnelServiceRequestID, error) {
	r.calls.Add(1)
	return "", r.err
}
