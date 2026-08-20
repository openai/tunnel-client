package e2e_test

import (
	"encoding/json"
	"net/http"
	"strings"
	"testing"

	"github.com/openai/tunnel-client/pkg/controlplane/wiretypes"
	harnesspkg "github.com/openai/tunnel-client/testsupport/e2e"
	"github.com/openai/tunnel-client/testsupport/mockmcpserver"
	"github.com/openai/tunnel-client/testsupport/mocktunnelservice"
)

func TestSynthesizedTargetHTTPFailureAppearsOnWireAndTunnelServiceAcceptsIt(t *testing.T) {
	const (
		requestID       = "cmd-target-http-failure"
		rpcID           = "rpc-target-http-failure"
		targetOnlyValue = "target-body-must-not-cross-the-tunnel"
	)

	command := mocktunnelservice.CommandResponse{
		Command: mocktunnelservice.NewCommand(
			requestID,
			json.RawMessage(`{
				"jsonrpc":"2.0",
				"id":"`+rpcID+`",
				"method":"initialize",
				"params":{"protocolVersion":"2025-06-18"}
			}`),
			http.Header{
				"Accept":       []string{"application/json, text/event-stream"},
				"Content-Type": []string{"application/json"},
			},
		),
		ExpectedResponses: []mocktunnelservice.ExpectedResponse{{
			RequestID: requestID,
			Assert: func(tb testing.TB, response mocktunnelservice.ReceivedResponse) {
				tb.Helper()
				if response.ResponseType != string(wiretypes.ResponsePayloadJSONRPC) {
					tb.Fatalf("response type = %q, want %q", response.ResponseType, wiretypes.ResponsePayloadJSONRPC)
				}
				if response.ResponseCode != http.StatusServiceUnavailable {
					tb.Fatalf("response code = %d, want %d", response.ResponseCode, http.StatusServiceUnavailable)
				}
				if strings.Contains(string(response.JSONResponse), targetOnlyValue) {
					tb.Fatal("raw target response body crossed the tunnel")
				}

				var payload struct {
					JSONRPC string `json:"jsonrpc"`
					ID      string `json:"id"`
					Error   struct {
						Code    int    `json:"code"`
						Message string `json:"message"`
						Data    struct {
							TunnelFailure struct {
								Version                  int    `json:"version"`
								Source                   string `json:"source"`
								TransportErrorKind       string `json:"transport_error_kind"`
								UpstreamResponseReceived bool   `json:"upstream_response_received"`
								UpstreamStatus           int    `json:"upstream_status"`
							} `json:"tunnel_failure"`
						} `json:"data"`
					} `json:"error"`
				}
				if err := json.Unmarshal(response.JSONResponse, &payload); err != nil {
					tb.Fatalf("decode resp_json posted on the tunnel-service wire: %v", err)
				}
				if payload.JSONRPC != "2.0" || payload.ID != rpcID {
					tb.Fatalf("JSON-RPC envelope = version %q id %q", payload.JSONRPC, payload.ID)
				}
				if payload.Error.Code != -32603 || payload.Error.Message != http.StatusText(http.StatusServiceUnavailable) {
					tb.Fatalf("JSON-RPC error = code %d message %q", payload.Error.Code, payload.Error.Message)
				}
				failure := payload.Error.Data.TunnelFailure
				if failure.Version != 1 || failure.Source != "target_http" {
					tb.Fatalf("tunnel failure identity = version %d source %q", failure.Version, failure.Source)
				}
				if failure.TransportErrorKind != "malformed_json" {
					tb.Fatalf("transport error kind = %q, want %q", failure.TransportErrorKind, "malformed_json")
				}
				if !failure.UpstreamResponseReceived || failure.UpstreamStatus != http.StatusServiceUnavailable {
					tb.Fatalf(
						"tunnel failure upstream evidence = received %t status %d",
						failure.UpstreamResponseReceived,
						failure.UpstreamStatus,
					)
				}
			},
		}},
	}

	h := harnesspkg.NewHarness(
		t,
		harnesspkg.WithControlPlaneOptions(
			mocktunnelservice.WithCommandResponses(command),
		),
		harnesspkg.WithMCPOptions(
			mockmcpserver.WithHostHandler("127.0.0.1", http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				w.Header().Set("Content-Type", "text/plain")
				w.WriteHeader(http.StatusServiceUnavailable)
				_, _ = w.Write([]byte(targetOnlyValue))
			})),
		),
	)

	h.ExecuteScenarious(t)

	responses := h.ControlPlane.ReceivedResponses(mocktunnelservice.ResponseMatchMatched)
	if len(responses) != 1 {
		t.Fatalf("matched tunnel-service responses = %d, want 1", len(responses))
	}
}
