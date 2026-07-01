package e2e_test

import (
	"encoding/base64"
	"encoding/json"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/url"
	"testing"
	"time"

	"go.openai.org/api/tunnel-client/pkg/config"
	"go.openai.org/api/tunnel-client/pkg/controlplane/wiretypes"
	harnesspkg "go.openai.org/api/tunnel-client/testsupport/e2e"
	"go.openai.org/api/tunnel-client/testsupport/mocktunnelservice"
)

type harpoonRequest struct {
	method  string
	path    string
	headers http.Header
}

func TestHarpoonInMemoryCallTargetToolCall(t *testing.T) {
	const (
		toolRequestID = "cmd-harpoon-call"
		callID        = "harpoon-call-1"
		targetLabel   = "abc"
		responseBody  = "pong"
	)

	reqCh := make(chan harpoonRequest, 2)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		reqCh <- harpoonRequest{
			method:  r.Method,
			path:    r.URL.Path,
			headers: r.Header.Clone(),
		}
		w.Header().Set("Content-Type", "text/plain")
		_, _ = w.Write([]byte(responseBody))
	}))
	defer server.Close()

	targetURL := mustParseURL(t, server.URL)
	expectedBase64 := base64.StdEncoding.EncodeToString([]byte(responseBody))

	toolCommand := mocktunnelservice.CommandResponse{
		Command: mocktunnelservice.NewCommand(
			toolRequestID,
			json.RawMessage(`{
				"jsonrpc":"2.0",
				"id":"`+callID+`",
				"method":"tools/call",
				"params":{
					"name":"call_target",
					"arguments":{
						"label":"`+targetLabel+`",
						"method":"GET",
						"headers":{}
					},
					"_meta":{
						"progressToken":3
					}
				}
			}`),
			nil,
		),
		ExpectedResponses: []mocktunnelservice.ExpectedResponse{{
			RequestID: toolRequestID,
			Assert: func(tb testing.TB, resp mocktunnelservice.ReceivedResponse) {
				if tb != nil {
					tb.Helper()
				}
				target := tb
				if target == nil {
					target = t
				}
				if resp.ResponseType != string(wiretypes.ResponsePayloadJSONRPC) {
					target.Fatalf("tool call response type mismatch: got %q", resp.ResponseType)
				}
				if resp.ResponseCode != http.StatusOK {
					target.Fatalf("tool call response code mismatch: %d", resp.ResponseCode)
				}
				if len(resp.JSONResponse) == 0 {
					target.Fatalf("tool call missing resp_json payload")
				}
				var payload struct {
					Result struct {
						StructuredContent json.RawMessage `json:"structuredContent"`
					} `json:"result"`
				}
				if err := json.Unmarshal(resp.JSONResponse, &payload); err != nil {
					target.Fatalf("decode tool response payload: %v", err)
				}
				if len(payload.Result.StructuredContent) == 0 {
					target.Fatalf("tool response missing structuredContent")
				}
				var structured struct {
					StatusCode int    `json:"status_code"`
					BodyBase64 string `json:"body_base64"`
					BodySize   int    `json:"body_size_bytes"`
					Truncated  bool   `json:"truncated"`
				}
				if err := json.Unmarshal(payload.Result.StructuredContent, &structured); err != nil {
					target.Fatalf("decode harpoon structured response: %v", err)
				}
				if structured.StatusCode != http.StatusOK {
					target.Fatalf("harpoon status code mismatch: got %d", structured.StatusCode)
				}
				if structured.BodyBase64 != expectedBase64 {
					target.Fatalf("harpoon body mismatch: got %q want %q", structured.BodyBase64, expectedBase64)
				}
				if structured.BodySize != len(responseBody) {
					target.Fatalf("harpoon body size mismatch: got %d want %d", structured.BodySize, len(responseBody))
				}
				if structured.Truncated {
					target.Fatalf("harpoon response unexpectedly truncated")
				}
			},
		}},
	}

	h := harnesspkg.NewHarness(
		t,
		harnesspkg.WithHarpoonInMemoryTransport(),
		harnesspkg.WithClientConfig(func(cfg *config.Config) {
			cfg.Logging.Level = slog.LevelDebug
			cfg.Harpoon.AllowPlaintextHTTP = true
			cfg.Harpoon.MaxResponseBytes = config.DefaultHarpoonMaxResponseBytes
			cfg.Harpoon.MaxRedirects = config.DefaultHarpoonMaxRedirects
			cfg.Harpoon.Targets = []config.HarpoonTarget{{
				Label:       targetLabel,
				Description: "harpoon test target",
				BaseURL:     targetURL,
			}}
		}),
		harnesspkg.WithControlPlaneOptions(
			mocktunnelservice.WithInitializationPhaseCommandsWithoutSessionHeaders(),
			mocktunnelservice.WithCommandResponses(toolCommand),
		),
	)

	h.ExecuteScenarious(t)

	select {
	case req := <-reqCh:
		if req.method != http.MethodGet {
			t.Fatalf("harpoon target method mismatch: got %q want %q", req.method, http.MethodGet)
		}
		if req.path != "/" {
			t.Fatalf("harpoon target path mismatch: got %q want %q", req.path, "/")
		}
	case <-time.After(time.Second):
		t.Fatalf("expected harpoon target to be called")
	}

	matched := h.ControlPlane.ReceivedResponses(mocktunnelservice.ResponseMatchMatched)
	if len(matched) != 3 {
		t.Fatalf("expected three matched responses (initialize, initialized, tool); got %d", len(matched))
	}
	delivered := h.ControlPlane.DeliveredCommands()
	if len(delivered) != 3 {
		t.Fatalf("expected three delivered commands; got %d", len(delivered))
	}
}

func mustParseURL(tb testing.TB, raw string) *url.URL {
	tb.Helper()
	parsed, err := url.Parse(raw)
	if err != nil {
		tb.Fatalf("parse url %q: %v", raw, err)
	}
	return parsed
}
