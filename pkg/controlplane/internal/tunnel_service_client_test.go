package internal

import (
	"context"
	"crypto/rand"
	"crypto/rsa"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/json"
	"encoding/pem"
	"errors"
	"io"
	"log/slog"
	"math/big"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"

	"github.com/modelcontextprotocol/go-sdk/jsonrpc"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	noopmetric "go.opentelemetry.io/otel/metric/noop"

	"github.com/openai/tunnel-client/pkg/config"
	"github.com/openai/tunnel-client/pkg/controlplane"
	wiretypes "github.com/openai/tunnel-client/pkg/controlplane/wiretypes"
	"github.com/openai/tunnel-client/pkg/tlsconfig"
	"github.com/openai/tunnel-client/pkg/tunnelctx"
	"github.com/openai/tunnel-client/pkg/types"
	"github.com/openai/tunnel-client/pkg/version"
)

var testMeterProvider = noopmetric.NewMeterProvider()

func encodeResponse(t *testing.T, resp *jsonrpc.Response) json.RawMessage {
	t.Helper()

	encoded, err := jsonrpc.EncodeMessage(resp)
	assert.NoError(t, err, "encode jsonrpc response")
	return json.RawMessage(encoded)
}

func TestTunnelServiceClientPollSuccess(t *testing.T) {
	t.Parallel()

	const (
		tunnelID  = "cli-tunnel"
		apiKey    = "test-api-key"
		requestID = "dc7427fd-eeab-4128-a3a6-aee876de182c"
		createdAt = "2025-10-29T23:08:09Z"
		limit     = 5
	)

	const jsonrpcPayload = `
{
  "commands":
        [
          {
                "command_type": "jsonrpc",
                "request_id": "dc7427fd-eeab-4128-a3a6-aee876de182c",
                "shard_token": "shard-123",
                "jsonrpc": {
                  "jsonrpc": "2.0",
                  "id": 0,
                  "method": "initialize",
		  "params": {
			"protocolVersion": "2025-06-18",
			"capabilities": {
			  "sampling": {},
			  "elicitation": {},
			  "roots": {
				"listChanged": true
			  }
			},
			"clientInfo": {
			  "name": "inspector-client",
			  "version": "0.17.2"
			}
		  }
        },
        "created_at": "2025-10-29T23:08:09Z",
        "meta": {}
      }
    ]
}
`

	server := newHTTPTestServer(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		wantPath := "/v1/tunnels/" + url.PathEscape(tunnelID) + "/poll"
		assert.Equal(t, wantPath, r.URL.Path, "unexpected path")
		assert.Equal(t, strconv.Itoa(limit), r.URL.Query().Get("limit"), "unexpected limit")
		assert.Equal(t, "1000", r.URL.Query().Get("timeout_ms"), "unexpected timeout_ms")
		assert.Equal(t, "Bearer "+apiKey, r.Header.Get("Authorization"), "unexpected Authorization header")
		assert.Empty(t, r.Header.Get("X-Tunnel-ID"), "X-Tunnel-ID header should be omitted")
		assert.Equal(t, "application/json", r.Header.Get("Accept"), "unexpected Accept header")
		assert.Equal(t, version.UserAgent, r.Header.Get("User-Agent"), "unexpected User-Agent header")
		assert.Equal(t, version.ClientName, r.Header.Get(headerTunnelClientName), "unexpected tunnel client name header")
		assert.Equal(t, version.Version, r.Header.Get(headerTunnelClientVersion), "unexpected tunnel client version header")

		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(jsonrpcPayload))
	}))

	client, err := NewTunnelServiceClient(context.Background(), &config.ControlPlaneConfig{
		BaseURL:     mustParseURL(t, server.URL),
		TunnelID:    types.TunnelID(tunnelID),
		APIKey:      apiKey,
		PollTimeout: time.Second,
	}, nil, newDiscardLogger(), &config.LoggingConfig{}, testMeterProvider)
	if !assert.NoError(t, err, "NewTunnelServiceClient failed") {
		return
	}

	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()

	commands, _, err := client.Poll(ctx, limit)
	if !assert.NoError(t, err, "Poll failed") {
		return
	}
	if !assert.Len(t, commands, 1, "expected 1 command") {
		return
	}

	cmd := commands[0]
	assert.Equal(t, requestID, cmd.RequestID().String(), "unexpected request ID")
	assert.Equal(t, "shard-123", cmd.ShardToken(), "unexpected shard token")

	type hasMessage interface{ Message() jsonrpc.Message }
	rpcCmd, ok := cmd.(hasMessage)
	if !assert.True(t, ok, "expected JsonRpcCommand") {
		return
	}
	msg := rpcCmd.Message()
	req, ok := msg.(*jsonrpc.Request)
	if assert.True(t, ok, "expected JSON-RPC request message") {
		assert.Equal(t, "initialize", req.Method, "unexpected method")
		var params map[string]any
		if assert.NoError(t, json.Unmarshal(req.Params, &params), "unmarshal params") {
			assert.NotEmpty(t, params, "params should not be empty")
		}
	}

	wantEnqueuedAt, err := time.Parse(time.RFC3339, createdAt)
	if !assert.NoError(t, err, "parse enqueuedAt") {
		return
	}
	assert.Truef(t, cmd.EnqueuedAt().Equal(wantEnqueuedAt), "unexpected enqueued_at: got %s want %s", cmd.EnqueuedAt().Format(time.RFC3339), wantEnqueuedAt.Format(time.RFC3339))

}

func TestTunnelServiceClientPollRecordsReceiptBeforeRawLoggingReadsBody(t *testing.T) {
	t.Parallel()

	receivedAt := time.Now()
	payload := `{"commands":[{
		"command_type":"jsonrpc",
		"request_id":"req-timeout-1",
		"shard_token":"shard-timeout",
		"response_timeout":"4500ms",
		"jsonrpc":{"jsonrpc":"2.0","id":"rpc-1","method":"tools/list"}
	},{
		"command_type":"oauth_discovery",
		"request_id":"req-timeout-2",
		"shard_token":"shard-timeout",
		"response_timeout":"4500ms"
	},{
		"command_type":"session_termination",
		"request_id":"req-timeout-3",
		"shard_token":"shard-timeout",
		"headers":{"Mcp-Session-Id":["session-1"]},
		"response_timeout":"4500ms"
	}]}`
	requestReceived := make(chan struct{})
	releaseHeaders := make(chan struct{})
	headersFlushed := make(chan struct{})
	releaseBody := make(chan struct{})
	var releaseBodyOnce sync.Once
	releaseResponseBody := func() {
		releaseBodyOnce.Do(func() { close(releaseBody) })
	}
	defer releaseResponseBody()

	server := newHTTPTestServer(t, http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		close(requestReceived)
		<-releaseHeaders
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("Content-Length", strconv.Itoa(len(payload)))
		w.WriteHeader(http.StatusOK)
		w.(http.Flusher).Flush()
		close(headersFlushed)
		<-releaseBody
		_, _ = w.Write([]byte(payload))
	}))
	client, err := NewTunnelServiceClient(context.Background(), &config.ControlPlaneConfig{
		BaseURL:  mustParseURL(t, server.URL),
		TunnelID: types.TunnelID("cli-tunnel"),
		APIKey:   "test-api-key",
	}, nil, newDiscardLogger(), &config.LoggingConfig{HTTPRawUnsafe: true}, testMeterProvider)
	require.NoError(t, err)

	receiptCaptured := make(chan struct{})
	nowCalls := 0
	client.now = func() time.Time {
		nowCalls++
		close(receiptCaptured)
		return receivedAt
	}

	type pollResult struct {
		commands []controlplane.PolledCommand
		err      error
	}
	result := make(chan pollResult, 1)
	go func() {
		commands, _, err := client.Poll(context.Background(), 3)
		result <- pollResult{commands: commands, err: err}
	}()

	select {
	case <-requestReceived:
	case <-time.After(5 * time.Second):
		t.Fatal("poll request was not received")
	}
	select {
	case <-receiptCaptured:
		t.Fatal("poll receipt time was captured before the real response headers arrived")
	default:
	}
	close(releaseHeaders)

	select {
	case <-headersFlushed:
	case <-time.After(5 * time.Second):
		t.Fatal("poll response headers were not flushed")
	}
	select {
	case <-receiptCaptured:
	case <-time.After(5 * time.Second):
		t.Fatal("poll receipt time was not captured while raw logging waited for the response body")
	}

	releaseResponseBody()
	var got pollResult
	select {
	case got = <-result:
	case <-time.After(5 * time.Second):
		t.Fatal("Poll did not return after the response body was released")
	}
	require.NoError(t, got.err)
	require.Equal(t, 1, nowCalls, "poll response receipt time must be captured once")
	require.Len(t, got.commands, 3)
	for _, command := range got.commands {
		deadline, ok := command.(controlplane.ResponseDeadlineProvider).ResponseDeadline()
		require.True(t, ok)
		require.Equal(t, 4500*time.Millisecond, deadline.Sub(receivedAt))
		require.True(t, command.PolledAt().Equal(receivedAt))
	}
}

func TestTunnelServiceClientPollSuccessWithControlPlaneURLPath(t *testing.T) {
	t.Parallel()

	server := newHTTPTestServer(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, "/chatgpttunnelgateway/dev/us/v1/tunnels/cli-tunnel/poll", r.URL.Path)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"commands":[]}`))
	}))

	client, err := NewTunnelServiceClient(context.Background(), &config.ControlPlaneConfig{
		BaseURL:     mustParseURL(t, server.URL+"/"),
		URLPath:     "/chatgpttunnelgateway/dev/us",
		TunnelID:    types.TunnelID("cli-tunnel"),
		APIKey:      "test-api-key",
		PollTimeout: time.Second,
	}, nil, newDiscardLogger(), &config.LoggingConfig{}, testMeterProvider)
	require.NoError(t, err)

	_, _, err = client.Poll(context.Background(), 1)
	require.NoError(t, err)
}

func TestTunnelServiceClientPollUsesConfiguredUnixSocketPath(t *testing.T) {
	t.Parallel()

	socketPath := filepath.Join(t.TempDir(), "control.sock")
	listener, err := net.Listen("unix", socketPath)
	if err != nil {
		t.Skipf("skipping test: unable to bind unix listener: %v", err)
	}
	server := &http.Server{Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, "/v1/tunnels/cli-tunnel/poll", r.URL.Path)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"commands":[]}`))
	})}
	go func() {
		_ = server.Serve(listener)
	}()
	t.Cleanup(func() {
		shutdownCtx, cancel := context.WithTimeout(context.Background(), time.Second)
		defer cancel()
		_ = server.Shutdown(shutdownCtx)
	})

	client, err := NewTunnelServiceClient(context.Background(), &config.ControlPlaneConfig{
		BaseURL:        mustParseURL(t, "http://127.0.0.1:1"),
		UnixSocketPath: socketPath,
		TunnelID:       types.TunnelID("cli-tunnel"),
		APIKey:         "test-api-key",
		PollTimeout:    time.Second,
	}, nil, newDiscardLogger(), &config.LoggingConfig{}, testMeterProvider)
	require.NoError(t, err)

	_, _, err = client.Poll(context.Background(), 1)
	require.NoError(t, err)
}

func TestTunnelServiceClientPollIgnoresLegacyBaseURLPath(t *testing.T) {
	t.Parallel()

	server := newHTTPTestServer(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, "/v1/tunnels/cli-tunnel/poll", r.URL.Path)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"commands":[]}`))
	}))

	client, err := NewTunnelServiceClient(context.Background(), &config.ControlPlaneConfig{
		BaseURL:     mustParseURL(t, server.URL+"/v1/tunnel"),
		TunnelID:    types.TunnelID("cli-tunnel"),
		APIKey:      "test-api-key",
		PollTimeout: time.Second,
	}, nil, newDiscardLogger(), &config.LoggingConfig{}, testMeterProvider)
	require.NoError(t, err)

	_, _, err = client.Poll(context.Background(), 1)
	require.NoError(t, err)
}

func TestTunnelServiceClientPollSkipsInvalidCommands(t *testing.T) {
	t.Parallel()

	const payload = `
{
  "commands":
	[
          {
                "command_type": "jsonrpc",
                "request_id": "",
                "shard_token": "shard-missing-id",
                "jsonrpc": {
                  "method": "invalid"
                }
          },
          {
                "command_type": "jsonrpc",
                "request_id": "valid",
                "shard_token": "shard-valid",
                "jsonrpc": {
                  "jsonrpc": "2.0",
                  "id": 1,
		  "method": "ping"
		},
		"created_at": "2024-01-01T00:00:00Z"
	  }
	]
}
`

	server := newHTTPTestServer(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(payload))
	}))

	client, err := NewTunnelServiceClient(context.Background(), &config.ControlPlaneConfig{
		BaseURL:     mustParseURL(t, server.URL),
		TunnelID:    types.TunnelID("cli-tunnel"),
		APIKey:      "test-api-key",
		PollTimeout: time.Second,
	}, nil, newDiscardLogger(), &config.LoggingConfig{}, testMeterProvider)
	if !assert.NoError(t, err, "NewTunnelServiceClient failed") {
		return
	}

	commands, _, err := client.Poll(context.Background(), 2)
	if assert.NoError(t, err, "Poll failed") {
		if assert.Len(t, commands, 1, "expected 1 valid command") {
			assert.Equal(t, "valid", commands[0].RequestID().String(), "unexpected command ID")
		}
	}
}

func TestTunnelServiceClientPollWithNonPositiveLimit(t *testing.T) {
	t.Parallel()

	client, err := NewTunnelServiceClient(context.Background(), &config.ControlPlaneConfig{
		BaseURL:  mustParseURL(t, "https://example.com"),
		TunnelID: types.TunnelID("cli-tunnel"),
		APIKey:   "test-api-key",
	}, nil, newDiscardLogger(), &config.LoggingConfig{}, testMeterProvider)
	if !assert.NoError(t, err, "NewTunnelServiceClient failed") {
		return
	}

	cmds, _, pollErr := client.Poll(context.Background(), 0)
	assert.NoError(t, pollErr, "Poll should not error")
	assert.Nil(t, cmds, "expected nil result for non-positive limit")
}

func TestNewTunnelServiceClientUsesConfiguredPollDeadlineGuardrail(t *testing.T) {
	t.Parallel()

	const (
		pollTimeout   = 500 * time.Millisecond
		pollGuardrail = 500 * time.Millisecond
	)

	client, err := NewTunnelServiceClient(context.Background(), &config.ControlPlaneConfig{
		BaseURL:               mustParseURL(t, "https://example.com"),
		TunnelID:              types.TunnelID("cli-tunnel"),
		APIKey:                "test-api-key",
		PollTimeout:           pollTimeout,
		PollDeadlineGuardrail: pollGuardrail,
	}, nil, newDiscardLogger(), &config.LoggingConfig{}, testMeterProvider)
	require.NoError(t, err)

	require.Equal(t, pollTimeout, client.pollTimeout)
	require.Equal(t, pollGuardrail, client.pollGuardrail)
	require.Equal(t, pollTimeout+pollGuardrail, client.client.Timeout)
}

func TestPollDeadlineTimeoutUsesRequestedPollTimeout(t *testing.T) {
	t.Parallel()

	require.Equal(
		t,
		1*time.Second,
		(config.ControlPlaneConfig{
			PollTimeout:           500 * time.Millisecond,
			PollDeadlineGuardrail: 500 * time.Millisecond,
		}).PollDeadlineTimeoutOrDefault(),
	)
}

func TestPollTimeoutMillisecondsRoundsPositiveSubMillisecondDurationUp(t *testing.T) {
	t.Parallel()

	require.EqualValues(t, 1, pollTimeoutMilliseconds(time.Nanosecond))
}

func TestTunnelServiceClientPollSurfacesAPIErrorCode(t *testing.T) {
	t.Parallel()

	const (
		tunnelID = "cli-tunnel"
		apiKey   = "test-api-key"
	)

	server := newHTTPTestServer(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/tunnels/"+url.PathEscape(tunnelID)+"/poll" {
			t.Fatalf("unexpected path: %s", r.URL.Path)
		}
		if got := r.Header.Get("Authorization"); got != "Bearer "+apiKey {
			t.Fatalf("unexpected auth header: %q", got)
		}
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("X-Request-Id", "req_certificate_required")
		w.WriteHeader(http.StatusUnauthorized)
		_, _ = w.Write([]byte(`{"error":{"message":"Your request must be made with a client certificate in addition to your API key.","type":"invalid_request_error","code":"certificate_required"}}`))
	}))

	client, err := NewTunnelServiceClient(context.Background(), &config.ControlPlaneConfig{
		BaseURL:  mustParseURL(t, server.URL),
		TunnelID: types.TunnelID(tunnelID),
		APIKey:   apiKey,
	}, nil, newDiscardLogger(), &config.LoggingConfig{}, testMeterProvider)
	if !assert.NoError(t, err, "NewTunnelServiceClient failed") {
		return
	}

	_, requestID, err := client.Poll(context.Background(), 1)
	if !assert.Error(t, err, "expected error for mTLS-required poll request without certificate") {
		return
	}
	assert.Equal(t, types.TunnelServiceRequestID("req_certificate_required"), requestID)
	assert.ErrorContains(t, err, "unexpected status 401")
	assert.ErrorContains(t, err, "certificate_required")
	var statusErr *APIStatusError
	if !assert.ErrorAs(t, err, &statusErr) {
		return
	}
	assert.Equal(t, http.StatusUnauthorized, statusErr.StatusCode())
	assert.Equal(t, "certificate_required", statusErr.Code())
}

func TestTunnelServiceClientPollAddsMitigationForTunnelServiceErrorCode(t *testing.T) {
	t.Parallel()

	const (
		tunnelID = "cli-tunnel"
		apiKey   = "test-api-key"
	)

	server := newHTTPTestServer(t, http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusUnauthorized)
		_, _ = w.Write([]byte(`{"error":{"message":"Access denied: this tunnel requires an active organization context.","type":"invalid_request_error","code":"tunnel_active_organization_required"}}`))
	}))

	client, err := NewTunnelServiceClient(context.Background(), &config.ControlPlaneConfig{
		BaseURL:  mustParseURL(t, server.URL),
		TunnelID: types.TunnelID(tunnelID),
		APIKey:   apiKey,
	}, nil, newDiscardLogger(), &config.LoggingConfig{}, testMeterProvider)
	if !assert.NoError(t, err, "NewTunnelServiceClient failed") {
		return
	}

	_, _, err = client.Poll(context.Background(), 1)
	if !assert.Error(t, err, "expected organization-context error") {
		return
	}
	assert.ErrorContains(t, err, "tunnel_active_organization_required")
	assert.ErrorContains(t, err, "control_plane.organization_id")
	var statusErr *APIStatusError
	if !assert.ErrorAs(t, err, &statusErr) {
		return
	}
	assert.Contains(t, statusErr.Mitigation(), "CONTROL_PLANE_ORGANIZATION_ID")
}

func TestTunnelServiceClientControlPlaneMTLS(t *testing.T) {
	t.Parallel()

	const (
		tunnelID = "tunnel_0123456789abcdef0123456789abcdef"
		apiKey   = "test-api-key"
	)

	material := newControlPlaneMTLSTestMaterial(t)

	var (
		mu       sync.Mutex
		seenPath = map[string]int{}
	)
	server := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.TLS == nil || len(r.TLS.PeerCertificates) == 0 {
			http.Error(w, "missing client certificate", http.StatusUnauthorized)
			return
		}
		if got := r.TLS.PeerCertificates[0].Subject.CommonName; got != "control-plane-client" {
			http.Error(w, "unexpected client certificate", http.StatusUnauthorized)
			return
		}

		mu.Lock()
		seenPath[r.URL.Path]++
		mu.Unlock()

		switch r.URL.Path {
		case "/v1/tunnels/" + url.PathEscape(tunnelID):
			assert.Equal(t, http.MethodGet, r.Method)
			assert.Equal(t, "Bearer "+apiKey, r.Header.Get("Authorization"))
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"id":"` + tunnelID + `","name":"mtls-test","description":"fake tunnel-service"}`))
		case "/v1/tunnels/" + url.PathEscape(tunnelID) + "/poll":
			assert.Equal(t, http.MethodGet, r.Method)
			assert.Equal(t, "Bearer "+apiKey, r.Header.Get("Authorization"))
			w.WriteHeader(http.StatusNoContent)
		case "/v1/tunnels/" + url.PathEscape(tunnelID) + "/response":
			assert.Equal(t, http.MethodPost, r.Method)
			assert.Equal(t, "Bearer "+apiKey, r.Header.Get("Authorization"))
			assert.Equal(t, "shard-mtls", r.Header.Get("X-Tunnel-Shard-Token"))
			w.WriteHeader(http.StatusOK)
		default:
			http.NotFound(w, r)
		}
	}))
	server.TLS = &tls.Config{
		Certificates: []tls.Certificate{material.serverCertificate},
		ClientAuth:   tls.RequireAndVerifyClientCert,
		ClientCAs:    material.caPool,
		MinVersion:   tls.VersionTLS12,
	}
	server.StartTLS()
	t.Cleanup(server.Close)

	client, err := NewTunnelServiceClient(context.Background(), &config.ControlPlaneConfig{
		BaseURL:           mustParseURL(t, server.URL),
		TunnelID:          types.TunnelID(tunnelID),
		APIKey:            apiKey,
		PollTimeout:       time.Second,
		ClientCertificate: material.clientCertificate,
	}, &tlsconfig.Bundle{RootCAs: material.caPool}, newDiscardLogger(), &config.LoggingConfig{}, testMeterProvider)
	require.NoError(t, err)

	metadata, err := client.FetchTunnelMetadata(context.Background())
	require.NoError(t, err)
	require.Equal(t, tunnelID, metadata.ID)

	commands, _, err := client.Poll(context.Background(), 1)
	require.NoError(t, err)
	require.Nil(t, commands)

	ctx := tunnelctx.ContextWithShardToken(context.Background(), "shard-mtls")
	ctx = tunnelctx.ContextWithChannel(ctx, types.DefaultChannel)
	_, err = client.PostResponse(ctx, types.RequestID("req-mtls"), types.NewNotificationAck(types.DefaultChannel, http.StatusOK, http.Header{}))
	require.NoError(t, err)

	mu.Lock()
	defer mu.Unlock()
	require.Equal(t, 1, seenPath["/v1/tunnels/"+url.PathEscape(tunnelID)])
	require.Equal(t, 1, seenPath["/v1/tunnels/"+url.PathEscape(tunnelID)+"/poll"])
	require.Equal(t, 1, seenPath["/v1/tunnels/"+url.PathEscape(tunnelID)+"/response"])
}

func TestTunnelServiceClientPostResponseSuccess(t *testing.T) {
	t.Parallel()

	const (
		tunnelID   = "cli-tunnel"
		apiKey     = "test-api-key"
		requestID  = "req-123"
		shardToken = "shard-post-success"
	)

	var (
		seenMethod string
		seenPath   string
		seenBody   []byte
	)

	server := newHTTPTestServer(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		seenMethod = r.Method
		seenPath = r.URL.Path

		assert.Equal(t, "Bearer "+apiKey, r.Header.Get("Authorization"), "unexpected Authorization header")
		assert.Empty(t, r.Header.Get("X-Tunnel-ID"), "X-Tunnel-ID header should be omitted")
		assert.Equal(t, "application/json", r.Header.Get("Content-Type"), "unexpected Content-Type header")
		assert.Equal(t, version.UserAgent, r.Header.Get("User-Agent"), "unexpected User-Agent header")
		assert.Equal(t, version.ClientName, r.Header.Get(headerTunnelClientName), "unexpected tunnel client name header")
		assert.Equal(t, version.Version, r.Header.Get(headerTunnelClientVersion), "unexpected tunnel client version header")

		var err error
		seenBody, err = io.ReadAll(r.Body)
		assert.NoError(t, err, "read request body")

		w.WriteHeader(http.StatusOK)
	}))

	client, err := NewTunnelServiceClient(context.Background(), &config.ControlPlaneConfig{
		BaseURL:  mustParseURL(t, server.URL),
		TunnelID: types.TunnelID(tunnelID),
		APIKey:   apiKey,
	}, nil, newDiscardLogger(), &config.LoggingConfig{}, testMeterProvider)
	if !assert.NoError(t, err, "NewTunnelServiceClient failed") {
		return
	}

	id, err := jsonrpc.MakeID("1")
	if !assert.NoError(t, err, "MakeID failed") {
		return
	}

	response := &jsonrpc.Response{
		ID:     id,
		Result: json.RawMessage(`{"ok":true}`),
	}
	rawResponse := encodeResponse(t, response)

	ctx := tunnelctx.ContextWithShardToken(context.Background(), shardToken)
	ctx = tunnelctx.ContextWithChannel(ctx, types.DefaultChannel)

	headers := http.Header{
		"Mcp-Session-Id": {"abc123"},
	}

	resp := types.NewTunnelResponse(types.DefaultChannel, rawResponse, http.StatusOK, headers)
	_, err = client.PostResponse(
		ctx,
		types.RequestID(requestID),
		resp,
	)
	if !assert.NoError(t, err, "PostResponse failed") {
		return
	}

	assert.Equal(t, http.MethodPost, seenMethod, "unexpected HTTP method")
	assert.Equal(t, "/v1/tunnels/"+url.PathEscape(tunnelID)+"/response", seenPath, "unexpected request path")

	var payload struct {
		RequestID   string          `json:"request_id"`
		Channel     string          `json:"channel"`
		RPCResp     json.RawMessage `json:"resp_json"`
		RespHeaders http.Header     `json:"resp_headers"`
		RespCode    int             `json:"resp_code"`
		RespType    string          `json:"resp_type"`
	}

	if assert.NoError(t, json.Unmarshal(seenBody, &payload), "unmarshal request payload") {
		assert.Equal(t, requestID, payload.RequestID, "unexpected request_id")
		assert.Equal(t, types.DefaultChannel.String(), payload.Channel, "unexpected channel")
		assert.JSONEq(t, `{"jsonrpc":"2.0","id":"1","result":{"ok":true}}`, string(payload.RPCResp), "unexpected rpc_resp")
		assert.Equal(t, headers, payload.RespHeaders, "unexpected resp_headers")
		assert.Equal(t, http.StatusOK, payload.RespCode, "unexpected resp_code")
		assert.Equal(t, string(wiretypes.ResponsePayloadJSONRPC), payload.RespType, "unexpected resp_type")
	}
}

func TestTunnelServiceClientPostResponseOAuthDiscovery(t *testing.T) {
	t.Parallel()

	const (
		tunnelID   = "cli-tunnel"
		apiKey     = "test-api-key"
		requestID  = "req-oauth"
		shardToken = "shard-oauth"
	)

	var (
		seenMethod string
		seenPath   string
		seenBody   []byte
	)

	server := newHTTPTestServer(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		seenMethod = r.Method
		seenPath = r.URL.Path
		body, _ := io.ReadAll(r.Body)
		seenBody = body
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
	}))

	client, err := NewTunnelServiceClient(context.Background(), &config.ControlPlaneConfig{
		BaseURL:  mustParseURL(t, server.URL),
		TunnelID: types.TunnelID(tunnelID),
		APIKey:   apiKey,
	}, nil, newDiscardLogger(), &config.LoggingConfig{}, testMeterProvider)
	require.NoError(t, err, "NewTunnelServiceClient failed")

	ctx := tunnelctx.ContextWithShardToken(context.Background(), shardToken)
	ctx = tunnelctx.ContextWithChannel(ctx, types.DefaultChannel)
	resp := types.NewOAuthDiscoveryResponse(types.DefaultChannel, json.RawMessage(`{"resource":"https://example.com"}`), http.StatusOK, http.Header{
		"Content-Type": []string{"application/json"},
	})

	_, err = client.PostResponse(ctx, types.RequestID(requestID), resp)
	require.NoError(t, err, "PostResponse failed")

	require.Equal(t, http.MethodPost, seenMethod, "unexpected HTTP method")
	require.Equal(t, "/v1/tunnels/"+url.PathEscape(tunnelID)+"/response", seenPath, "unexpected request path")

	var payload struct {
		RequestID   string          `json:"request_id"`
		Channel     string          `json:"channel"`
		RPCResp     json.RawMessage `json:"resp_json"`
		RespHeaders http.Header     `json:"resp_headers"`
		RespCode    int             `json:"resp_code"`
		RespType    string          `json:"resp_type"`
	}

	require.NoError(t, json.Unmarshal(seenBody, &payload), "unmarshal request payload")
	require.Equal(t, requestID, payload.RequestID, "unexpected request_id")
	require.Equal(t, types.DefaultChannel.String(), payload.Channel, "unexpected channel")
	require.JSONEq(t, `{"resource":"https://example.com"}`, string(payload.RPCResp), "unexpected payload")
	require.Equal(t, http.StatusOK, payload.RespCode, "unexpected resp_code")
	require.Equal(t, string(wiretypes.ResponsePayloadOAuth), payload.RespType, "unexpected resp_type")
	require.Equal(t, http.Header{"Content-Type": []string{"application/json"}}, payload.RespHeaders)
}

func TestTunnelServiceClientPostResponsePrefersResponseChannel(t *testing.T) {
	t.Parallel()

	const (
		tunnelID   = "cli-tunnel"
		apiKey     = "test-api-key"
		requestID  = "req-channel-response"
		shardToken = "shard-channel-response"
		channel    = types.ChannelHarpoon
	)

	var seenBody []byte

	server := newHTTPTestServer(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		seenBody = body
		w.WriteHeader(http.StatusOK)
	}))

	client, err := NewTunnelServiceClient(context.Background(), &config.ControlPlaneConfig{
		BaseURL:  mustParseURL(t, server.URL),
		TunnelID: types.TunnelID(tunnelID),
		APIKey:   apiKey,
	}, nil, newDiscardLogger(), &config.LoggingConfig{}, testMeterProvider)
	require.NoError(t, err, "NewTunnelServiceClient failed")

	ctx := tunnelctx.ContextWithShardToken(context.Background(), shardToken)
	ctx = tunnelctx.ContextWithChannel(ctx, types.DefaultChannel)

	resp := types.NewNotificationAck(channel, http.StatusOK, http.Header{})
	_, err = client.PostResponse(ctx, types.RequestID(requestID), resp)
	require.NoError(t, err, "PostResponse failed")

	var payload struct {
		RequestID string `json:"request_id"`
		Channel   string `json:"channel"`
	}
	require.NoError(t, json.Unmarshal(seenBody, &payload), "unmarshal request payload")
	require.Equal(t, requestID, payload.RequestID)
	require.Equal(t, channel.String(), payload.Channel)
}

func TestTunnelServiceClientPostResponsePropagatesClientRequestID(t *testing.T) {
	t.Parallel()

	const (
		controlPlaneRequestID = "ctl-req-123"
		tunnelID              = "cli-tunnel"
		apiKey                = "test-api-key"
		requestID             = "req-123"
		shardToken            = "shard-client-request"
	)

	var seenClientRequestID string

	server := newHTTPTestServer(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		seenClientRequestID = r.Header.Get("X-Client-Request-Id")
		_, _ = io.ReadAll(r.Body)
		w.WriteHeader(http.StatusOK)
	}))

	client, err := NewTunnelServiceClient(context.Background(), &config.ControlPlaneConfig{
		BaseURL:  mustParseURL(t, server.URL),
		TunnelID: types.TunnelID(tunnelID),
		APIKey:   apiKey,
	}, nil, newDiscardLogger(), &config.LoggingConfig{}, testMeterProvider)
	if !assert.NoError(t, err, "NewTunnelServiceClient failed") {
		return
	}

	id, err := jsonrpc.MakeID("1")
	if !assert.NoError(t, err, "MakeID failed") {
		return
	}

	ctx := tunnelctx.ContextWithShardToken(context.Background(), shardToken)
	ctx = tunnelctx.ContextWithChannel(ctx, types.DefaultChannel)
	ctx = tunnelctx.ContextWithControlPlaneCommandRequestID(ctx, types.ControlPlaneRequestID(controlPlaneRequestID))
	response := &jsonrpc.Response{
		ID:     id,
		Result: json.RawMessage(`{"ok":true}`),
	}
	rawResponse := encodeResponse(t, response)

	resp := types.NewTunnelResponse(types.DefaultChannel, rawResponse, http.StatusOK, nil)
	_, err = client.PostResponse(
		ctx,
		types.RequestID(requestID),
		resp,
	)
	if !assert.NoError(t, err, "PostResponse failed") {
		return
	}

	assert.Equal(t, controlPlaneRequestID, seenClientRequestID, "expected X-Client-Request-Id header to propagate")
}

func TestTunnelServiceClientPostResponsePropagatesShardToken(t *testing.T) {
	t.Parallel()

	const (
		shardToken = "shard-xyz"
		tunnelID   = "cli-tunnel"
		apiKey     = "test-api-key"
		requestID  = "req-123"
	)

	var seenShardToken string

	server := newHTTPTestServer(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		seenShardToken = r.Header.Get("X-Tunnel-Shard-Token")
		_, _ = io.ReadAll(r.Body)
		w.WriteHeader(http.StatusOK)
	}))

	client, err := NewTunnelServiceClient(context.Background(), &config.ControlPlaneConfig{
		BaseURL:  mustParseURL(t, server.URL),
		TunnelID: types.TunnelID(tunnelID),
		APIKey:   apiKey,
	}, nil, newDiscardLogger(), &config.LoggingConfig{}, testMeterProvider)
	if !assert.NoError(t, err, "NewTunnelServiceClient failed") {
		return
	}

	id, err := jsonrpc.MakeID("1")
	if !assert.NoError(t, err, "MakeID failed") {
		return
	}

	ctx := tunnelctx.ContextWithShardToken(context.Background(), shardToken)
	ctx = tunnelctx.ContextWithChannel(ctx, types.DefaultChannel)
	response := &jsonrpc.Response{
		ID:     id,
		Result: json.RawMessage(`{"ok":true}`),
	}
	rawResponse := encodeResponse(t, response)

	resp := types.NewTunnelResponse(types.DefaultChannel, rawResponse, http.StatusOK, nil)
	_, err = client.PostResponse(
		ctx,
		types.RequestID(requestID),
		resp,
	)
	if !assert.NoError(t, err, "PostResponse failed") {
		return
	}

	assert.Equal(t, shardToken, seenShardToken, "expected X-Tunnel-Shard-Token header to propagate")
}

func TestTunnelServiceClientPostResponseRequiresShardToken(t *testing.T) {
	t.Parallel()

	server := newHTTPTestServer(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Fatalf("request should not be sent without shard token")
	}))

	client, err := NewTunnelServiceClient(context.Background(), &config.ControlPlaneConfig{
		BaseURL:  mustParseURL(t, server.URL),
		TunnelID: types.TunnelID("cli-tunnel"),
		APIKey:   "test-api-key",
	}, nil, newDiscardLogger(), &config.LoggingConfig{}, testMeterProvider)
	if !assert.NoError(t, err, "NewTunnelServiceClient failed") {
		return
	}

	id, err := jsonrpc.MakeID("missing-shard")
	if !assert.NoError(t, err, "MakeID failed") {
		return
	}

	response := &jsonrpc.Response{
		ID:     id,
		Result: json.RawMessage(`{"ok":true}`),
	}
	rawResponse := encodeResponse(t, response)

	resp := types.NewTunnelResponse(types.DefaultChannel, rawResponse, http.StatusOK, nil)
	_, err = client.PostResponse(
		tunnelctx.ContextWithChannel(context.Background(), types.DefaultChannel),
		types.RequestID("req-missing-shard"),
		resp,
	)
	assert.Error(t, err, "PostResponse should require a shard token")
}

func TestTunnelServiceClientPostResponseTreatsNotFoundAsSuccess(t *testing.T) {
	t.Parallel()

	server := newHTTPTestServer(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNotFound)
	}))

	client, err := NewTunnelServiceClient(context.Background(), &config.ControlPlaneConfig{
		BaseURL:  mustParseURL(t, server.URL),
		TunnelID: types.TunnelID("cli-tunnel"),
		APIKey:   "test-api-key",
	}, nil, newDiscardLogger(), &config.LoggingConfig{}, testMeterProvider)
	if !assert.NoError(t, err, "NewTunnelServiceClient failed") {
		return
	}

	response := &jsonrpc.Response{
		Result: json.RawMessage(`{"ok":true}`),
	}
	rawResponse := encodeResponse(t, response)

	ctx := tunnelctx.ContextWithShardToken(context.Background(), "shard-404")
	ctx = tunnelctx.ContextWithChannel(ctx, types.DefaultChannel)

	resp := types.NewTunnelResponse(types.DefaultChannel, rawResponse, http.StatusOK, nil)
	_, err = client.PostResponse(
		ctx,
		types.RequestID("request-404"),
		resp,
	)
	assert.NoError(t, err, "PostResponse should treat 404 as success")
}

func TestTunnelServiceClientPostResponseSurfacingNonSuccess(t *testing.T) {
	t.Parallel()

	server := newHTTPTestServer(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusBadGateway)
	}))

	client, err := NewTunnelServiceClient(context.Background(), &config.ControlPlaneConfig{
		BaseURL:  mustParseURL(t, server.URL),
		TunnelID: types.TunnelID("cli-tunnel"),
		APIKey:   "test-api-key",
	}, nil, newDiscardLogger(), &config.LoggingConfig{}, testMeterProvider)
	if !assert.NoError(t, err, "NewTunnelServiceClient failed") {
		return
	}

	ctx := tunnelctx.ContextWithShardToken(context.Background(), "shard-502")
	ctx = tunnelctx.ContextWithChannel(ctx, types.DefaultChannel)
	rawResponse := encodeResponse(t, &jsonrpc.Response{
		Result: json.RawMessage(`{"ok":true}`),
	})

	resp := types.NewTunnelResponse(types.DefaultChannel, rawResponse, http.StatusOK, nil)
	_, err = client.PostResponse(
		ctx,
		types.RequestID("request-502"),
		resp,
	)
	assert.Error(t, err, "PostResponse should propagate non-200/404 errors")
	assert.ErrorContains(t, err, "unexpected status 502")
}

func TestTunnelServiceClientPostResponseSurfacesAPIErrorCode(t *testing.T) {
	t.Parallel()

	server := newHTTPTestServer(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusUnauthorized)
		_, _ = w.Write([]byte(`{"error":{"message":"Your request must be made with a client certificate in addition to your API key.","type":"invalid_request_error","code":"certificate_required"}}`))
	}))

	client, err := NewTunnelServiceClient(context.Background(), &config.ControlPlaneConfig{
		BaseURL:  mustParseURL(t, server.URL),
		TunnelID: types.TunnelID("cli-tunnel"),
		APIKey:   "test-api-key",
	}, nil, newDiscardLogger(), &config.LoggingConfig{}, testMeterProvider)
	if !assert.NoError(t, err, "NewTunnelServiceClient failed") {
		return
	}

	ctx := tunnelctx.ContextWithShardToken(context.Background(), "shard-401")
	ctx = tunnelctx.ContextWithChannel(ctx, types.DefaultChannel)
	rawResponse := encodeResponse(t, &jsonrpc.Response{
		Result: json.RawMessage(`{"ok":true}`),
	})

	resp := types.NewTunnelResponse(types.DefaultChannel, rawResponse, http.StatusOK, nil)
	_, err = client.PostResponse(ctx, types.RequestID("request-401"), resp)
	if !assert.Error(t, err, "PostResponse should propagate mTLS auth errors") {
		return
	}
	assert.ErrorContains(t, err, "unexpected status 401")
	assert.ErrorContains(t, err, "certificate_required")
	var statusErr *APIStatusError
	if !assert.ErrorAs(t, err, &statusErr) {
		return
	}
	assert.Equal(t, http.StatusUnauthorized, statusErr.StatusCode())
	assert.Equal(t, "certificate_required", statusErr.Code())
}

func TestTunnelServiceClientPostResponseNotificationAck(t *testing.T) {
	t.Parallel()

	var seenBody []byte
	server := newHTTPTestServer(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var err error
		seenBody, err = io.ReadAll(r.Body)
		assert.NoError(t, err, "read request body")
		w.WriteHeader(http.StatusOK)
	}))

	client, err := NewTunnelServiceClient(context.Background(), &config.ControlPlaneConfig{
		BaseURL:  mustParseURL(t, server.URL),
		TunnelID: types.TunnelID("cli-tunnel"),
		APIKey:   "test-api-key",
	}, nil, newDiscardLogger(), &config.LoggingConfig{}, testMeterProvider)
	if !assert.NoError(t, err, "NewTunnelServiceClient failed") {
		return
	}

	ctx := tunnelctx.ContextWithShardToken(context.Background(), "shard-notif")
	ctx = tunnelctx.ContextWithChannel(ctx, types.DefaultChannel)
	notificationAck := types.NewNotificationAck(types.DefaultChannel, http.StatusOK, http.Header{})

	_, err = client.PostResponse(
		ctx,
		types.RequestID("notif-req"),
		notificationAck,
	)
	assert.NoError(t, err, "PostResponse should allow notification acknowledgements")

	var payload map[string]any
	if assert.NoError(t, json.Unmarshal(seenBody, &payload), "unmarshal request payload") {
		assert.Equal(t, "notif-req", payload["request_id"])
		_, hasResponse := payload["rpc_resp"]
		assert.False(t, hasResponse, "rpc_resp should be omitted for notification acks")
		assert.Equal(t, string(wiretypes.ResponsePayloadNotifyAck), payload["resp_type"])
		assert.Equal(t, float64(http.StatusOK), payload["resp_code"])
	}
}

func TestTunnelServiceClientPostResponseJSONRPCNotification(t *testing.T) {
	t.Parallel()

	var seenBody []byte
	server := newHTTPTestServer(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var err error
		seenBody, err = io.ReadAll(r.Body)
		assert.NoError(t, err, "read request body")
		w.WriteHeader(http.StatusOK)
	}))

	client, err := NewTunnelServiceClient(context.Background(), &config.ControlPlaneConfig{
		BaseURL:  mustParseURL(t, server.URL),
		TunnelID: types.TunnelID("cli-tunnel"),
		APIKey:   "test-api-key",
	}, nil, newDiscardLogger(), &config.LoggingConfig{}, testMeterProvider)
	if !assert.NoError(t, err, "NewTunnelServiceClient failed") {
		return
	}

	rawNotification, err := jsonrpc.EncodeMessage(&jsonrpc.Request{Method: "notifications/progress"})
	if !assert.NoError(t, err, "EncodeMessage failed") {
		return
	}

	ctx := tunnelctx.ContextWithShardToken(context.Background(), "shard-notify-jsonrpc")
	ctx = tunnelctx.ContextWithChannel(ctx, types.DefaultChannel)
	headers := http.Header{"Content-Type": []string{"application/json"}}

	resp := types.NewJSONRPCNotification(types.DefaultChannel, rawNotification, http.StatusOK, headers)
	_, err = client.PostResponse(
		ctx,
		types.RequestID("notif-jsonrpc"),
		resp,
	)
	assert.NoError(t, err, "PostResponse should allow JSON-RPC notifications")

	var payload struct {
		RequestID   string          `json:"request_id"`
		RPCResp     json.RawMessage `json:"resp_json"`
		RespHeaders http.Header     `json:"resp_headers"`
		RespCode    int             `json:"resp_code"`
		RespType    string          `json:"resp_type"`
	}
	if assert.NoError(t, json.Unmarshal(seenBody, &payload), "unmarshal request payload") {
		assert.Equal(t, "notif-jsonrpc", payload.RequestID)
		assert.JSONEq(t, `{"jsonrpc":"2.0","method":"notifications/progress"}`, string(payload.RPCResp))
		assert.Equal(t, headers, payload.RespHeaders)
		assert.Equal(t, http.StatusOK, payload.RespCode)
		assert.Equal(t, string(wiretypes.ResponsePayloadJSONRPCNotify), payload.RespType)
	}
}

func TestTunnelServiceClientExtraHeadersAreSent(t *testing.T) {
	t.Parallel()

	const (
		tunnelID = "cli-tunnel"
		apiKey   = "test-api-key"
	)

	server := newHTTPTestServer(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, "true", r.Header.Get("extra-header"), "expected extra header to be sent")
		w.WriteHeader(http.StatusNoContent)
	}))

	client, err := NewTunnelServiceClient(context.Background(), &config.ControlPlaneConfig{
		BaseURL:      mustParseURL(t, server.URL),
		TunnelID:     types.TunnelID(tunnelID),
		APIKey:       apiKey,
		ExtraHeaders: map[string]string{"extra-header": "true"},
	}, nil, newDiscardLogger(), &config.LoggingConfig{}, testMeterProvider)
	if !assert.NoError(t, err, "NewTunnelServiceClient failed") {
		return
	}

	_, _, err = client.Poll(context.Background(), 1)
	assert.NoError(t, err, "Poll should succeed with extra headers enabled")
}

type warnCaptureHandler struct {
	seenOverride bool
	header       string
}

func (h *warnCaptureHandler) Enabled(context.Context, slog.Level) bool { return true }

func (h *warnCaptureHandler) Handle(_ context.Context, r slog.Record) error {
	if r.Level == slog.LevelWarn && (r.Message == "control-plane extra header overrides existing header" ||
		r.Message == "control-plane extra header cannot override protected header") {
		h.seenOverride = true
		r.Attrs(func(a slog.Attr) bool {
			if a.Key == "header" {
				h.header = a.Value.String()
			}
			return true
		})
	}
	return nil
}

func (h *warnCaptureHandler) WithAttrs(_ []slog.Attr) slog.Handler { return h }
func (h *warnCaptureHandler) WithGroup(string) slog.Handler        { return h }

func TestTunnelServiceClientExtraHeadersCannotOverrideProtectedHeaders(t *testing.T) {
	t.Parallel()

	const (
		tunnelID = "cli-tunnel"
		apiKey   = "test-api-key"
	)

	server := newHTTPTestServer(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if got := r.Header.Get("Accept"); got != "application/json" {
			t.Fatalf("expected protected Accept header to be preserved, got %q", got)
		}
		w.WriteHeader(http.StatusNoContent)
	}))

	handler := &warnCaptureHandler{}
	logger := slog.New(handler)

	client, err := NewTunnelServiceClient(context.Background(), &config.ControlPlaneConfig{
		BaseURL:      mustParseURL(t, server.URL),
		TunnelID:     types.TunnelID(tunnelID),
		APIKey:       apiKey,
		ExtraHeaders: map[string]string{"Accept": "application/problem+json"},
	}, nil, logger, &config.LoggingConfig{}, testMeterProvider)
	if !assert.NoError(t, err, "NewTunnelServiceClient failed") {
		return
	}

	_, _, err = client.Poll(context.Background(), 1)
	assert.NoError(t, err, "Poll should succeed with extra headers enabled")
	assert.True(t, handler.seenOverride, "expected warning when extra header attempts to override a protected header")
	assert.Equal(t, "Accept", handler.header, "expected warning for Accept header")
}

func TestTunnelServiceClientFetchTunnelMetadata(t *testing.T) {
	t.Parallel()

	const (
		tunnelID = "cli-tunnel"
		apiKey   = "test-api-key"
	)

	server := newHTTPTestServer(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/tunnels/"+url.PathEscape(tunnelID) {
			t.Fatalf("unexpected path: %s", r.URL.Path)
		}
		if got := r.Header.Get("Authorization"); got != "Bearer "+apiKey {
			t.Fatalf("unexpected auth header: %q", got)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"id":"tunnel_123","name":"Demo tunnel","description":"demo"}`))
	}))

	client, err := NewTunnelServiceClient(context.Background(), &config.ControlPlaneConfig{
		BaseURL:  mustParseURL(t, server.URL),
		TunnelID: types.TunnelID(tunnelID),
		APIKey:   apiKey,
	}, nil, newDiscardLogger(), &config.LoggingConfig{}, testMeterProvider)
	if !assert.NoError(t, err, "NewTunnelServiceClient failed") {
		return
	}

	metadata, err := client.FetchTunnelMetadata(context.Background())
	if !assert.NoError(t, err, "FetchTunnelMetadata failed") {
		return
	}
	assert.Equal(t, "tunnel_123", metadata.ID)
	assert.Equal(t, "Demo tunnel", metadata.Name)
	assert.Equal(t, "demo", metadata.Description)
}

func TestTunnelServiceClientFetchTunnelMetadataStatusError(t *testing.T) {
	t.Parallel()

	const (
		tunnelID = "cli-tunnel"
		apiKey   = "test-api-key"
	)

	server := newHTTPTestServer(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if got := r.Header.Get("Authorization"); got != "Bearer "+apiKey {
			t.Fatalf("unexpected auth header: %q", got)
		}
		w.WriteHeader(http.StatusForbidden)
		_, _ = w.Write([]byte(`{"detail":"missing permission"}`))
	}))

	client, err := NewTunnelServiceClient(context.Background(), &config.ControlPlaneConfig{
		BaseURL:  mustParseURL(t, server.URL),
		TunnelID: types.TunnelID(tunnelID),
		APIKey:   apiKey,
	}, nil, newDiscardLogger(), &config.LoggingConfig{}, testMeterProvider)
	if !assert.NoError(t, err, "NewTunnelServiceClient failed") {
		return
	}

	_, err = client.FetchTunnelMetadata(context.Background())
	if !assert.Error(t, err, "expected error for non-2xx status") {
		return
	}
	var statusErr *MetadataStatusError
	if !assert.ErrorAs(t, err, &statusErr) {
		return
	}
	assert.Equal(t, http.StatusForbidden, statusErr.StatusCode())
	assert.Equal(t, "403 Forbidden", statusErr.Status())
}

type errorReadCloser struct {
	err error
}

func (r errorReadCloser) Read(_ []byte) (int, error) {
	return 0, r.err
}

func (errorReadCloser) Close() error {
	return nil
}

func TestNewAPIStatusErrorDrainsOversizedErrorBody(t *testing.T) {
	t.Parallel()

	resp := &http.Response{
		StatusCode: http.StatusBadGateway,
		Status:     "502 Bad Gateway",
		Body:       io.NopCloser(strings.NewReader(strings.Repeat("x", maxControlPlaneErrorBodySize+17))),
	}

	statusErr := newAPIStatusError("controlplane client: unexpected status", resp, time.Now())

	assert.Equal(t, http.StatusBadGateway, statusErr.StatusCode())
	remainder, err := io.ReadAll(resp.Body)
	assert.NoError(t, err)
	assert.Empty(t, remainder)
}

func TestNewAPIStatusErrorTruncatesInvalidErrorBody(t *testing.T) {
	t.Parallel()

	resp := &http.Response{
		StatusCode: http.StatusBadGateway,
		Status:     "502 Bad Gateway",
		Body:       io.NopCloser(strings.NewReader(strings.Repeat("x", maxControlPlaneErrorBodySize+17))),
	}

	statusErr := newAPIStatusError("controlplane client: unexpected status", resp, time.Now())

	assert.Equal(t, http.StatusBadGateway, statusErr.StatusCode())
	assert.Empty(t, statusErr.Code())
	assert.Empty(t, statusErr.Message())
	assert.Len(t, statusErr.Detail(), 1027)
	assert.True(t, strings.HasSuffix(statusErr.Detail(), "..."))
}

func TestNewAPIStatusErrorHandlesBodyReadFailure(t *testing.T) {
	t.Parallel()

	resp := &http.Response{
		StatusCode: http.StatusBadGateway,
		Status:     "502 Bad Gateway",
		Body:       errorReadCloser{err: errors.New("boom")},
	}

	statusErr := newAPIStatusError("controlplane client: unexpected status", resp, time.Now())

	assert.Equal(t, http.StatusBadGateway, statusErr.StatusCode())
	assert.Empty(t, statusErr.Code())
	assert.Equal(t, "read error body: boom", statusErr.Message())
	assert.Equal(t, "read error body: boom", statusErr.Detail())
}

func TestPopulateAPIStatusErrorDefensiveParsing(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name        string
		body        string
		wantCode    string
		wantType    string
		wantMessage string
		wantDetail  string
	}{
		{
			name: "empty body",
			body: " \n\t ",
		},
		{
			name:       "malformed json",
			body:       `{"error":`,
			wantDetail: `{"error":`,
		},
		{
			name:        "top-level api error",
			body:        `{"message":"missing cert","type":"invalid_request_error","code":"certificate_required"}`,
			wantCode:    "certificate_required",
			wantType:    "invalid_request_error",
			wantMessage: "missing cert",
			wantDetail:  "certificate_required: missing cert",
		},
		{
			name:        "nested api error",
			body:        `{"error":{"message":"missing cert","type":"invalid_request_error","code":"certificate_required"}}`,
			wantCode:    "certificate_required",
			wantType:    "invalid_request_error",
			wantMessage: "missing cert",
			wantDetail:  "certificate_required: missing cert",
		},
		{
			name:        "detail string",
			body:        `{"detail":"missing cert"}`,
			wantMessage: "missing cert",
			wantDetail:  "missing cert",
		},
		{
			name:        "detail object",
			body:        `{"detail":{"reason":"certificate_required"}}`,
			wantMessage: `{"reason":"certificate_required"}`,
			wantDetail:  `{"reason":"certificate_required"}`,
		},
		{
			name:       "unexpected json shape",
			body:       `["not","an","object"]`,
			wantDetail: `["not","an","object"]`,
		},
	}

	for _, testCase := range tests {
		testCase := testCase
		t.Run(testCase.name, func(t *testing.T) {
			t.Parallel()

			statusErr := &APIStatusError{}

			populateAPIStatusError(statusErr, []byte(testCase.body))

			assert.Equal(t, testCase.wantCode, statusErr.Code())
			assert.Equal(t, testCase.wantType, statusErr.Type())
			assert.Equal(t, testCase.wantMessage, statusErr.Message())
			assert.Equal(t, testCase.wantDetail, statusErr.Detail())
		})
	}
}

func TestTunnelServiceClientFetchTunnelMetadataSurfacesAPIErrorCode(t *testing.T) {
	t.Parallel()

	const (
		tunnelID = "cli-tunnel"
		apiKey   = "test-api-key"
	)

	server := newHTTPTestServer(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if got := r.Header.Get("Authorization"); got != "Bearer "+apiKey {
			t.Fatalf("unexpected auth header: %q", got)
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusUnauthorized)
		_, _ = w.Write([]byte(`{"error":{"message":"Your request must be made with a client certificate in addition to your API key.","type":"invalid_request_error","code":"certificate_required"}}`))
	}))

	client, err := NewTunnelServiceClient(context.Background(), &config.ControlPlaneConfig{
		BaseURL:  mustParseURL(t, server.URL),
		TunnelID: types.TunnelID(tunnelID),
		APIKey:   apiKey,
	}, nil, newDiscardLogger(), &config.LoggingConfig{}, testMeterProvider)
	if !assert.NoError(t, err, "NewTunnelServiceClient failed") {
		return
	}

	_, err = client.FetchTunnelMetadata(context.Background())
	if !assert.Error(t, err, "expected error for mTLS-required metadata request without certificate") {
		return
	}
	assert.ErrorContains(t, err, "certificate_required")
	assert.ErrorContains(t, err, "client certificate")
	var statusErr *MetadataStatusError
	if !assert.ErrorAs(t, err, &statusErr) {
		return
	}
	assert.Equal(t, http.StatusUnauthorized, statusErr.StatusCode())
	assert.Equal(t, "certificate_required", statusErr.Code())
	assert.Equal(t, "invalid_request_error", statusErr.Type())
	assert.Contains(t, statusErr.Message(), "client certificate")
}

func TestTunnelServiceClientWarnsWhenServerExceedsLimit(t *testing.T) {
	t.Parallel()

	const (
		tunnelID = "cli-tunnel"
		apiKey   = "test-api-key"
	)

	// Prepare a poll payload with 3 commands while we'll request limit=1.
	const body = `{
  "commands": [
    {
      "command_type": "jsonrpc",
      "request_id": "cmd-1",
      "shard_token": "sh-1",
      "jsonrpc": {"jsonrpc":"2.0","id":1,"method":"initialize"},
      "created_at": "2025-10-29T23:08:09Z",
      "headers": {"X": ["y"]}
    },
    {
      "command_type": "jsonrpc",
      "request_id": "cmd-2",
      "shard_token": "sh-2",
      "jsonrpc": {"jsonrpc":"2.0","id":2,"method":"noop"},
      "created_at": "2025-10-29T23:08:10Z",
      "headers": {"X": ["y"]}
    },
    {
      "command_type": "jsonrpc",
      "request_id": "cmd-3",
      "shard_token": "sh-3",
      "jsonrpc": {"jsonrpc":"2.0","id":3,"method":"noop"},
      "created_at": "2025-10-29T23:08:11Z",
      "headers": {"X": ["y"]}
    }
  ]
}`

	server := newHTTPTestServer(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/tunnels/"+url.PathEscape(tunnelID)+"/poll" {
			t.Fatalf("unexpected path: %s", r.URL.Path)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(body))
	}))

	// Capture warnings/errors.
	var cap dropWarnCapture
	logger := slog.New(&dropWarnHandler{cap: &cap})

	client, err := NewTunnelServiceClient(context.Background(), &config.ControlPlaneConfig{
		BaseURL:     mustParseURL(t, server.URL),
		TunnelID:    types.TunnelID(tunnelID),
		APIKey:      apiKey,
		PollTimeout: time.Second,
	}, nil, logger, &config.LoggingConfig{}, testMeterProvider)
	if !assert.NoError(t, err, "NewTunnelServiceClient failed") {
		return
	}

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	cmds, _, err := client.Poll(ctx, 1)
	if !assert.NoError(t, err, "Poll failed") {
		return
	}
	// Server returning more than requested should be logged, but we should not drop commands.
	assert.Len(t, cmds, 3)

	assert.True(t, cap.seen, "expected warning/error when server returns more than requested limit")
	assert.Equal(t, 1, cap.limit)
	assert.Equal(t, 3, cap.total)
}

type dropWarnHandler struct {
	cap *dropWarnCapture
}

func (h *dropWarnHandler) Enabled(ctx context.Context, level slog.Level) bool {
	return true
}

func (h *dropWarnHandler) Handle(ctx context.Context, r slog.Record) error {
	if r.Message == "control-plane returned more commands than requested limit" {
		h.cap.seen = true
		r.Attrs(func(a slog.Attr) bool {
			switch a.Key {
			case "limit":
				if v, ok := a.Value.Any().(int); ok {
					h.cap.limit = v
				} else if a.Value.Kind() == slog.KindInt64 {
					h.cap.limit = int(a.Value.Int64())
				}
			case "total":
				if v, ok := a.Value.Any().(int); ok {
					h.cap.total = v
				} else if a.Value.Kind() == slog.KindInt64 {
					h.cap.total = int(a.Value.Int64())
				}
			}
			return true
		})
	}
	return nil
}

func (h *dropWarnHandler) WithAttrs(attrs []slog.Attr) slog.Handler { return h }
func (h *dropWarnHandler) WithGroup(name string) slog.Handler       { return h }

type dropWarnCapture struct {
	seen  bool
	limit int
	total int
}

func TestTunnelServiceClientUsesProxy(t *testing.T) {
	t.Parallel()

	const (
		tunnelID = "cli-tunnel"
		apiKey   = "test-api-key"
	)

	targetCalled := make(chan struct{}, 1)
	targetServer := newHTTPTestServer(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		targetCalled <- struct{}{}
		http.Error(w, "unexpected direct request", http.StatusBadGateway)
	}))

	proxyCalled := make(chan struct{}, 1)
	proxyServer := newHTTPTestServer(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		proxyCalled <- struct{}{}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"commands":[]}`))
	}))

	client, err := NewTunnelServiceClient(context.Background(), &config.ControlPlaneConfig{
		BaseURL:     mustParseURL(t, targetServer.URL),
		TunnelID:    types.TunnelID(tunnelID),
		APIKey:      apiKey,
		PollTimeout: time.Second,
		HTTPProxy:   mustParseURL(t, proxyServer.URL),
	}, nil, newDiscardLogger(), &config.LoggingConfig{}, testMeterProvider)
	if err != nil {
		t.Fatalf("NewTunnelServiceClient failed: %v", err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()

	_, _, err = client.Poll(ctx, 1)
	if err != nil {
		t.Fatalf("Poll failed: %v", err)
	}

	select {
	case <-proxyCalled:
	default:
		t.Fatalf("expected proxy to receive request")
	}
	select {
	case <-targetCalled:
		t.Fatalf("expected target server not to be called directly")
	default:
	}
}

func newDiscardLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}

func newHTTPTestServer(t *testing.T, handler http.Handler) *httptest.Server {
	t.Helper()

	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		if errors.Is(err, syscall.EACCES) || errors.Is(err, syscall.EPERM) {
			t.Skipf("skipping test: unable to bind listener: %v", err)
		}
		t.Fatalf("failed to create listener: %v", err)
	}

	server := httptest.NewUnstartedServer(handler)
	server.Listener = listener
	server.Start()
	t.Cleanup(server.Close)

	return server
}

func mustParseURL(t *testing.T, raw string) *url.URL {
	t.Helper()
	parsed, err := url.Parse(raw)
	if err != nil {
		t.Fatalf("parse URL %q: %v", raw, err)
	}
	return parsed
}

func TestTunnelServiceClientPollWithExplicitCommandTypeJSONRPC(t *testing.T) {
	t.Parallel()

	const (
		tunnelID  = "cli-tunnel"
		apiKey    = "test-api-key"
		requestID = "cmd-jsonrpc-explicit"
		limit     = 3
	)

	const payload = `
{
  "commands": [
    {
      "command_type": "jsonrpc",
      "request_id": "cmd-jsonrpc-explicit",
      "shard_token": "shard-explicit",
      "jsonrpc": {
        "jsonrpc": "2.0",
        "id": 1,
        "method": "initialize"
      },
      "created_at": "2025-10-29T23:08:09Z",
      "headers": {"X-Test": ["true"]}
    }
  ]
}
`

	server := newHTTPTestServer(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(payload))
	}))

	client, err := NewTunnelServiceClient(context.Background(), &config.ControlPlaneConfig{
		BaseURL:     mustParseURL(t, server.URL),
		TunnelID:    types.TunnelID(tunnelID),
		APIKey:      apiKey,
		PollTimeout: time.Second,
	}, nil, newDiscardLogger(), &config.LoggingConfig{}, testMeterProvider)
	if !assert.NoError(t, err, "NewTunnelServiceClient failed") {
		return
	}

	cmds, _, err := client.Poll(context.Background(), limit)
	if !assert.NoError(t, err, "Poll failed") {
		return
	}
	if !assert.Len(t, cmds, 1, "expected exactly one command") {
		return
	}
	assert.Equal(t, requestID, cmds[0].RequestID().String())
	assert.Equal(t, "shard-explicit", cmds[0].ShardToken())
}

func TestTunnelServiceClientPollSkipsUnknownCommandType(t *testing.T) {
	t.Parallel()

	const (
		tunnelID  = "cli-tunnel"
		apiKey    = "test-api-key"
		requestID = "cmd-known"
		limit     = 5
	)

	const payload = `
{
  "commands": [
    {
      "command_type": "totally_unknown",
      "request_id": "cmd-unknown",
      "shard_token": "shard-unknown",
      "jsonrpc": {"jsonrpc": "2.0", "id": 1, "method": "noop"},
      "created_at": "2025-10-29T23:08:09Z",
      "headers": {}
    },
    {
      "request_id": "cmd-known",
      "shard_token": "shard-known",
      "command_type": "jsonrpc",
      "jsonrpc": {"jsonrpc": "2.0", "id": 2, "method": "initialize"},
      "created_at": "2025-10-29T23:08:09Z",
      "headers": {}
    }
  ]
}
`

	server := newHTTPTestServer(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(payload))
	}))

	client, err := NewTunnelServiceClient(context.Background(), &config.ControlPlaneConfig{
		BaseURL:     mustParseURL(t, server.URL),
		TunnelID:    types.TunnelID(tunnelID),
		APIKey:      apiKey,
		PollTimeout: time.Second,
	}, nil, newDiscardLogger(), &config.LoggingConfig{}, testMeterProvider)
	if !assert.NoError(t, err, "NewTunnelServiceClient failed") {
		return
	}

	cmds, _, err := client.Poll(context.Background(), limit)
	if !assert.NoError(t, err, "Poll failed") {
		return
	}
	if !assert.Len(t, cmds, 1, "expected only the known JSON-RPC command to be processed") {
		return
	}
	assert.Equal(t, requestID, cmds[0].RequestID().String())
	assert.Equal(t, "shard-known", cmds[0].ShardToken())
}

func TestTunnelServiceClientPollReturnsOauthDiscoveryCommand(t *testing.T) {
	t.Parallel()

	const (
		tunnelID  = "cli-tunnel"
		apiKey    = "test-api-key"
		requestID = "cmd-oauth"
		limit     = 1
	)

	const payload = `
{
  "commands": [
    {
      "command_type": "oauth_discovery",
      "request_id": "cmd-oauth",
      "shard_token": "shard-oauth",
      "created_at": "2025-10-29T23:08:09Z",
      "headers": {"X-Test": ["true"]}
    }
  ]
}
`

	server := newHTTPTestServer(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(payload))
	}))

	client, err := NewTunnelServiceClient(context.Background(), &config.ControlPlaneConfig{
		BaseURL:     mustParseURL(t, server.URL),
		TunnelID:    types.TunnelID(tunnelID),
		APIKey:      apiKey,
		PollTimeout: time.Second,
	}, nil, newDiscardLogger(), &config.LoggingConfig{}, testMeterProvider)
	if !assert.NoError(t, err, "NewTunnelServiceClient failed") {
		return
	}

	cmds, _, err := client.Poll(context.Background(), limit)
	if !assert.NoError(t, err, "Poll failed") {
		return
	}
	if !assert.Len(t, cmds, 1, "expected exactly one command") {
		return
	}

	cmd := cmds[0]
	assert.Equal(t, requestID, cmd.RequestID().String())
	assert.Equal(t, "shard-oauth", cmd.ShardToken())

	type hasMessage interface{ Message() jsonrpc.Message }
	_, ok := cmd.(hasMessage)
	assert.False(t, ok, "oauth discovery command should not expose Message()")
}

func TestTunnelServiceClientPollReturnsSessionTerminationCommand(t *testing.T) {
	t.Parallel()

	const (
		tunnelID  = "cli-tunnel"
		apiKey    = "test-api-key"
		requestID = "cmd-terminate"
		limit     = 1
	)

	const payload = `
{
  "commands": [
    {
      "command_type": "session_termination",
      "request_id": "cmd-terminate",
      "shard_token": "shard-terminate",
      "created_at": "2025-10-29T23:08:09Z",
      "headers": {"Mcp-Session-Id": ["session-123"]}
    }
  ]
}
`

	server := newHTTPTestServer(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(payload))
	}))

	client, err := NewTunnelServiceClient(context.Background(), &config.ControlPlaneConfig{
		BaseURL:     mustParseURL(t, server.URL),
		TunnelID:    types.TunnelID(tunnelID),
		APIKey:      apiKey,
		PollTimeout: time.Second,
	}, nil, newDiscardLogger(), &config.LoggingConfig{}, testMeterProvider)
	if !assert.NoError(t, err, "NewTunnelServiceClient failed") {
		return
	}

	cmds, _, err := client.Poll(context.Background(), limit)
	if !assert.NoError(t, err, "Poll failed") {
		return
	}
	if !assert.Len(t, cmds, 1, "expected exactly one command") {
		return
	}

	cmd, ok := cmds[0].(controlplane.SessionTerminationCommand)
	require.True(t, ok, "expected session termination command")
	assert.Equal(t, requestID, cmd.RequestID().String())
	assert.Equal(t, "shard-terminate", cmd.ShardToken())
	sessionID, hasSessionID := cmd.SessionID()
	assert.True(t, hasSessionID)
	assert.Equal(t, "session-123", sessionID)
}

func TestNewTunnelServiceClientValidatesInputs(t *testing.T) {
	t.Parallel()

	baseURL := mustParseURL(t, "https://example.com")
	logger := newDiscardLogger()

	t.Run("NilConfig", func(t *testing.T) {
		t.Parallel()

		_, err := NewTunnelServiceClient(context.Background(), nil, nil, logger, &config.LoggingConfig{}, testMeterProvider)
		require.ErrorIs(t, err, errMissingConfig)
	})

	t.Run("MissingBaseURL", func(t *testing.T) {
		t.Parallel()

		_, err := NewTunnelServiceClient(context.Background(), &config.ControlPlaneConfig{
			BaseURL:  nil,
			TunnelID: "tunnel",
			APIKey:   "key",
		}, nil, logger, &config.LoggingConfig{}, testMeterProvider)
		require.Error(t, err)
		require.Contains(t, err.Error(), "control-plane.base-url is required")
	})

	t.Run("MissingTunnelID", func(t *testing.T) {
		t.Parallel()

		_, err := NewTunnelServiceClient(context.Background(), &config.ControlPlaneConfig{
			BaseURL: baseURL,
			APIKey:  "key",
		}, nil, logger, &config.LoggingConfig{}, testMeterProvider)
		require.Error(t, err)
		require.Contains(t, err.Error(), "control-plane.tunnel-id is required")
	})

	t.Run("MissingAPIKey", func(t *testing.T) {
		t.Parallel()

		_, err := NewTunnelServiceClient(context.Background(), &config.ControlPlaneConfig{
			BaseURL:  baseURL,
			TunnelID: "tunnel",
		}, nil, logger, &config.LoggingConfig{}, testMeterProvider)
		require.Error(t, err)
		require.Contains(t, err.Error(), "control-plane.api-key is required")
	})

	t.Run("MalformedAPIKey", func(t *testing.T) {
		t.Parallel()

		const malformedKey = "bad key"
		_, err := NewTunnelServiceClient(context.Background(), &config.ControlPlaneConfig{
			BaseURL:  baseURL,
			TunnelID: "tunnel",
			APIKey:   malformedKey,
		}, nil, logger, &config.LoggingConfig{}, testMeterProvider)
		require.Error(t, err)
		require.Contains(t, err.Error(), "control plane API key is malformed")
		require.NotContains(t, err.Error(), malformedKey)
	})

	t.Run("NilMeterProvider", func(t *testing.T) {
		t.Parallel()

		_, err := NewTunnelServiceClient(context.Background(), &config.ControlPlaneConfig{
			BaseURL:  baseURL,
			TunnelID: "tunnel",
			APIKey:   "key",
		}, nil, logger, &config.LoggingConfig{}, nil)
		require.Error(t, err)
		require.Contains(t, err.Error(), "meter provider is required")
	})

	t.Run("NilLogger", func(t *testing.T) {
		t.Parallel()

		_, err := NewTunnelServiceClient(context.Background(), &config.ControlPlaneConfig{
			BaseURL:  baseURL,
			TunnelID: "tunnel",
			APIKey:   "key",
		}, nil, nil, &config.LoggingConfig{}, testMeterProvider)
		require.Error(t, err)
		require.Contains(t, err.Error(), "logger is required")
	})
}

func TestTunnelServiceClientPostResponseValidatesArgs(t *testing.T) {
	t.Parallel()

	server := newHTTPTestServer(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Fatalf("server should not be called")
	}))

	client, err := NewTunnelServiceClient(context.Background(), &config.ControlPlaneConfig{
		BaseURL:     mustParseURL(t, server.URL),
		TunnelID:    types.TunnelID("cli-tunnel"),
		APIKey:      "test-api-key",
		PollTimeout: time.Second,
	}, nil, newDiscardLogger(), &config.LoggingConfig{}, testMeterProvider)
	require.NoError(t, err)

	ctx := tunnelctx.ContextWithShardToken(context.Background(), "shard-required")
	ctx = tunnelctx.ContextWithChannel(ctx, types.DefaultChannel)

	_, err = client.PostResponse(ctx, "", &types.TunnelResponse{})
	require.Error(t, err)
	require.Contains(t, err.Error(), "requestID is required")

	_, err = client.PostResponse(ctx, types.RequestID("req"), nil)
	require.Error(t, err)
	require.Contains(t, err.Error(), "response is required")

	resp := types.NewNotificationAck("", http.StatusOK, http.Header{})
	_, err = client.PostResponse(tunnelctx.ContextWithShardToken(context.Background(), "shard-required"), types.RequestID("req"), resp)
	require.Error(t, err)
	require.Contains(t, err.Error(), "channel is required")
}

func TestTunnelServiceClientPostResponseErrorsWithoutShardToken(t *testing.T) {
	t.Parallel()

	server := newHTTPTestServer(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Fatalf("server should not be called")
	}))

	client, err := NewTunnelServiceClient(context.Background(), &config.ControlPlaneConfig{
		BaseURL:     mustParseURL(t, server.URL),
		TunnelID:    types.TunnelID("cli-tunnel"),
		APIKey:      "test-api-key",
		PollTimeout: time.Second,
	}, nil, newDiscardLogger(), &config.LoggingConfig{}, testMeterProvider)
	require.NoError(t, err)

	resp := types.NewNotificationAck(types.DefaultChannel, http.StatusOK, http.Header{})
	ctx := tunnelctx.ContextWithChannel(context.Background(), types.DefaultChannel)
	_, err = client.PostResponse(ctx, types.RequestID("req"), resp)
	require.Error(t, err)
	require.Contains(t, err.Error(), "shard token is required")
}

func TestTunnelServiceClientPostResponseReturnsTunnelServiceRequestIDFromHeader(t *testing.T) {
	t.Parallel()

	const wantTSRID = "tsrid-123"

	server := newHTTPTestServer(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("X-Request-Id", wantTSRID)
		w.WriteHeader(http.StatusOK)
	}))

	client, err := NewTunnelServiceClient(context.Background(), &config.ControlPlaneConfig{
		BaseURL:     mustParseURL(t, server.URL),
		TunnelID:    types.TunnelID("cli-tunnel"),
		APIKey:      "test-api-key",
		PollTimeout: time.Second,
	}, nil, newDiscardLogger(), &config.LoggingConfig{}, testMeterProvider)
	require.NoError(t, err)

	ctx := tunnelctx.ContextWithShardToken(context.Background(), "shard-token")
	ctx = tunnelctx.ContextWithChannel(ctx, types.DefaultChannel)
	resp := types.NewNotificationAck(types.DefaultChannel, http.StatusOK, http.Header{})

	got, err := client.PostResponse(ctx, types.RequestID("req"), resp)
	require.NoError(t, err)
	require.Equal(t, types.TunnelServiceRequestID(wantTSRID), got)
}

func TestTunnelServiceClientPollReturnsTunnelServiceRequestIDFromHeader(t *testing.T) {
	t.Parallel()

	const wantTSRID = "tsrid-poll-1"

	server := newHTTPTestServer(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("X-Request-Id", wantTSRID)
		w.WriteHeader(http.StatusNoContent)
	}))

	client, err := NewTunnelServiceClient(context.Background(), &config.ControlPlaneConfig{
		BaseURL:     mustParseURL(t, server.URL),
		TunnelID:    types.TunnelID("cli-tunnel"),
		APIKey:      "test-api-key",
		PollTimeout: time.Second,
	}, nil, newDiscardLogger(), &config.LoggingConfig{}, testMeterProvider)
	require.NoError(t, err)

	cmds, gotTSRID, err := client.Poll(context.Background(), 1)
	require.NoError(t, err)
	require.Nil(t, cmds)
	require.Equal(t, types.TunnelServiceRequestID(wantTSRID), gotTSRID)
}

type controlPlaneMTLSTestMaterial struct {
	caPool            *x509.CertPool
	serverCertificate tls.Certificate
	clientCertificate *tlsconfig.ClientCertificate
}

func newControlPlaneMTLSTestMaterial(t *testing.T) controlPlaneMTLSTestMaterial {
	t.Helper()

	caCert, caKey, caPEM := generateControlPlaneTestCA(t)
	serverCert := generateControlPlaneSignedLeaf(t, caCert, caKey, "control-plane-server", []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth})
	clientCert, clientCertPath, clientKeyPath := generateControlPlaneSignedClientCertificate(t, caCert, caKey)

	pool := x509.NewCertPool()
	if ok := pool.AppendCertsFromPEM(caPEM); !ok {
		t.Fatalf("failed to append CA cert to pool")
	}

	return controlPlaneMTLSTestMaterial{
		caPool:            pool,
		serverCertificate: serverCert,
		clientCertificate: &tlsconfig.ClientCertificate{
			CertPath:    clientCertPath,
			KeyPath:     clientKeyPath,
			Certificate: clientCert,
		},
	}
}

func generateControlPlaneTestCA(t *testing.T) (*x509.Certificate, *rsa.PrivateKey, []byte) {
	t.Helper()

	caKey, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatalf("generate CA key: %v", err)
	}
	caTemplate := &x509.Certificate{
		SerialNumber:          big.NewInt(101),
		Subject:               pkix.Name{CommonName: "control-plane-test-ca"},
		NotBefore:             time.Now().Add(-time.Hour),
		NotAfter:              time.Now().Add(2 * time.Hour),
		KeyUsage:              x509.KeyUsageCertSign | x509.KeyUsageCRLSign,
		BasicConstraintsValid: true,
		IsCA:                  true,
	}
	caDER, err := x509.CreateCertificate(rand.Reader, caTemplate, caTemplate, &caKey.PublicKey, caKey)
	if err != nil {
		t.Fatalf("create CA certificate: %v", err)
	}
	caCert, err := x509.ParseCertificate(caDER)
	if err != nil {
		t.Fatalf("parse CA certificate: %v", err)
	}
	caPEM := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: caDER})
	if caPEM == nil {
		t.Fatalf("encode CA certificate PEM")
	}
	return caCert, caKey, caPEM
}

func generateControlPlaneSignedLeaf(t *testing.T, caCert *x509.Certificate, caKey *rsa.PrivateKey, commonName string, extKeyUsage []x509.ExtKeyUsage) tls.Certificate {
	t.Helper()

	leafKey, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatalf("generate leaf key: %v", err)
	}
	leafTemplate := &x509.Certificate{
		SerialNumber: big.NewInt(102),
		Subject:      pkix.Name{CommonName: commonName},
		NotBefore:    time.Now().Add(-time.Hour),
		NotAfter:     time.Now().Add(2 * time.Hour),
		KeyUsage:     x509.KeyUsageDigitalSignature | x509.KeyUsageKeyEncipherment,
		ExtKeyUsage:  extKeyUsage,
		DNSNames:     []string{"localhost"},
		IPAddresses:  []net.IP{net.ParseIP("127.0.0.1"), net.ParseIP("::1")},
	}
	leafDER, err := x509.CreateCertificate(rand.Reader, leafTemplate, caCert, &leafKey.PublicKey, caKey)
	if err != nil {
		t.Fatalf("create leaf certificate: %v", err)
	}
	leafPEM := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: leafDER})
	if leafPEM == nil {
		t.Fatalf("encode leaf certificate PEM")
	}
	keyPEM := pem.EncodeToMemory(&pem.Block{Type: "RSA PRIVATE KEY", Bytes: x509.MarshalPKCS1PrivateKey(leafKey)})
	if keyPEM == nil {
		t.Fatalf("encode leaf key PEM")
	}
	pair, err := tls.X509KeyPair(leafPEM, keyPEM)
	if err != nil {
		t.Fatalf("load leaf key pair: %v", err)
	}
	return pair
}

func generateControlPlaneSignedClientCertificate(t *testing.T, caCert *x509.Certificate, caKey *rsa.PrivateKey) (tls.Certificate, string, string) {
	t.Helper()

	clientKey, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatalf("generate client key: %v", err)
	}
	clientTemplate := &x509.Certificate{
		SerialNumber: big.NewInt(103),
		Subject:      pkix.Name{CommonName: "control-plane-client"},
		NotBefore:    time.Now().Add(-time.Hour),
		NotAfter:     time.Now().Add(2 * time.Hour),
		KeyUsage:     x509.KeyUsageDigitalSignature | x509.KeyUsageKeyEncipherment,
		ExtKeyUsage:  []x509.ExtKeyUsage{x509.ExtKeyUsageClientAuth},
	}
	clientDER, err := x509.CreateCertificate(rand.Reader, clientTemplate, caCert, &clientKey.PublicKey, caKey)
	if err != nil {
		t.Fatalf("create client certificate: %v", err)
	}
	clientPEM := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: clientDER})
	if clientPEM == nil {
		t.Fatalf("encode client certificate PEM")
	}
	keyPEM := pem.EncodeToMemory(&pem.Block{Type: "RSA PRIVATE KEY", Bytes: x509.MarshalPKCS1PrivateKey(clientKey)})
	if keyPEM == nil {
		t.Fatalf("encode client key PEM")
	}
	clientPair, err := tls.X509KeyPair(clientPEM, keyPEM)
	if err != nil {
		t.Fatalf("load client key pair: %v", err)
	}
	dir := t.TempDir()
	clientCertPath := filepath.Join(dir, "control-plane-client.crt")
	clientKeyPath := filepath.Join(dir, "control-plane-client.key")
	if err := os.WriteFile(clientCertPath, clientPEM, 0o600); err != nil {
		t.Fatalf("write client cert: %v", err)
	}
	if err := os.WriteFile(clientKeyPath, keyPEM, 0o600); err != nil {
		t.Fatalf("write client key: %v", err)
	}
	return clientPair, clientCertPath, clientKeyPath
}
