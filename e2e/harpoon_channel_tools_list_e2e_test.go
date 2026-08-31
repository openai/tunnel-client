package e2e_test

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/openai/tunnel-client/pkg/config"
	"github.com/openai/tunnel-client/pkg/controlplane/wiretypes"
	harnesspkg "github.com/openai/tunnel-client/testsupport/e2e"
	"github.com/openai/tunnel-client/testsupport/mocktunnelservice"
)

func TestHarpoonChannelInitializeThenToolsList(t *testing.T) {
	const (
		channel              = "harpoon"
		initializeCommandID  = "cmd-harpoon-init"
		initializedCommandID = "cmd-harpoon-initialized"
		toolsListCommandID   = "cmd-harpoon-tools-list"
	)

	targetServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"ok":true}`))
	}))
	defer targetServer.Close()
	targetURL := mustParseURL(t, targetServer.URL)

	initializeCommand := mocktunnelservice.CommandResponse{
		Command: newChannelCommand(
			initializeCommandID,
			channel,
			json.RawMessage(`{
				"jsonrpc":"2.0",
				"id":"initialize-harpoon-0",
				"method":"initialize",
				"params":{
					"protocolVersion":"2025-06-18",
					"capabilities":{},
					"clientInfo":{"name":"harpoon-e2e","version":"0.0.1"}
				}
			}`),
			http.Header{
				"Accept":       []string{"application/json, text/event-stream"},
				"Content-Type": []string{"application/json"},
			},
		),
		ExpectedResponses: []mocktunnelservice.ExpectedResponse{{
			RequestID: initializeCommandID,
			Assert: func(tb testing.TB, resp mocktunnelservice.ReceivedResponse) {
				if tb != nil {
					tb.Helper()
				}
				target := tb
				if target == nil {
					target = t
				}
				if resp.ResponseType != string(wiretypes.ResponsePayloadJSONRPC) {
					target.Fatalf("initialize response type mismatch: got %q", resp.ResponseType)
				}
				if resp.ResponseCode != http.StatusOK {
					target.Fatalf("initialize response code mismatch: got %d", resp.ResponseCode)
				}
				if len(resp.JSONResponse) == 0 {
					target.Fatalf("initialize response missing resp_json payload")
				}
				var payload struct {
					Result struct {
						ServerInfo struct {
							Name string `json:"name"`
						} `json:"serverInfo"`
					} `json:"result"`
				}
				if err := json.Unmarshal(resp.JSONResponse, &payload); err != nil {
					target.Fatalf("decode initialize response payload: %v", err)
				}
				if payload.Result.ServerInfo.Name != "harpoon" {
					target.Fatalf(
						"initialize server info mismatch: got %q want %q",
						payload.Result.ServerInfo.Name,
						"harpoon",
					)
				}
			},
		}},
	}

	initializedCommand := mocktunnelservice.CommandResponse{
		Command: newChannelCommand(
			initializedCommandID,
			channel,
			json.RawMessage(`{
				"jsonrpc":"2.0",
				"method":"notifications/initialized",
				"params":{}
			}`),
			http.Header{
				"Accept":       []string{"application/json"},
				"Content-Type": []string{"application/json"},
			},
		),
		ExpectedResponses: []mocktunnelservice.ExpectedResponse{{
			RequestID: initializedCommandID,
			Assert: func(tb testing.TB, resp mocktunnelservice.ReceivedResponse) {
				if tb != nil {
					tb.Helper()
				}
				target := tb
				if target == nil {
					target = t
				}
				if resp.ResponseType != string(wiretypes.ResponsePayloadNotifyAck) {
					target.Fatalf("initialized ack type mismatch: got %q", resp.ResponseType)
				}
				if resp.ResponseCode != http.StatusOK {
					target.Fatalf("initialized ack code mismatch: got %d", resp.ResponseCode)
				}
			},
		}},
	}

	toolsListCommand := mocktunnelservice.CommandResponse{
		Command: newChannelCommand(
			toolsListCommandID,
			channel,
			json.RawMessage(`{
				"jsonrpc":"2.0",
				"id":"tools-list-harpoon-1",
				"method":"tools/list",
				"params":{}
			}`),
			http.Header{
				"Accept":       []string{"application/json"},
				"Content-Type": []string{"application/json"},
			},
		),
		ExpectedResponses: []mocktunnelservice.ExpectedResponse{{
			RequestID: toolsListCommandID,
			Assert: func(tb testing.TB, resp mocktunnelservice.ReceivedResponse) {
				if tb != nil {
					tb.Helper()
				}
				target := tb
				if target == nil {
					target = t
				}
				if resp.ResponseType != string(wiretypes.ResponsePayloadJSONRPC) {
					target.Fatalf("tools/list response type mismatch: got %q", resp.ResponseType)
				}
				if resp.ResponseCode != http.StatusOK {
					target.Fatalf("tools/list response code mismatch: got %d", resp.ResponseCode)
				}
				if len(resp.JSONResponse) == 0 {
					target.Fatalf("tools/list response missing resp_json payload")
				}
				var payload struct {
					Result struct {
						Tools []struct {
							Name string `json:"name"`
						} `json:"tools"`
					} `json:"result"`
				}
				if err := json.Unmarshal(resp.JSONResponse, &payload); err != nil {
					target.Fatalf("decode tools/list response payload: %v", err)
				}
				toolNames := make(map[string]bool, len(payload.Result.Tools))
				for _, tool := range payload.Result.Tools {
					toolNames[tool.Name] = true
				}
				if !toolNames["list_targets"] {
					target.Fatalf("tools/list missing list_targets tool")
				}
				if !toolNames["call_target"] {
					target.Fatalf("tools/list missing call_target tool")
				}
			},
		}},
	}

	h := harnesspkg.NewHarness(
		t,
		harnesspkg.WithClientConfig(func(cfg *config.Config) {
			cfg.Logging.Level = slog.LevelDebug
			cfg.Harpoon.AllowPlaintextHTTP = true
			cfg.Harpoon.Targets = []config.HarpoonTarget{{
				Label:       "seed",
				Description: "seed target for routable harpoon channel",
				BaseURL:     targetURL,
			}}
		}),
		harnesspkg.WithControlPlaneOptions(
			mocktunnelservice.WithCommandResponses(
				initializeCommand,
				initializedCommand,
				toolsListCommand,
			),
		),
	)

	h.ExecuteScenarious(t)

	matched := h.ControlPlane.ReceivedResponses(mocktunnelservice.ResponseMatchMatched)
	if len(matched) != 3 {
		t.Fatalf("expected three matched responses (initialize, initialized, tools/list); got %d", len(matched))
	}
	delivered := h.ControlPlane.DeliveredCommands()
	if len(delivered) != 3 {
		t.Fatalf("expected three delivered commands; got %d", len(delivered))
	}
}

func TestHarpoonChannelAcceptsSelfContained20260728Requests(t *testing.T) {
	t.Parallel()

	targetServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte("{\"ok\":true}"))
	}))
	defer targetServer.Close()

	commands := []mocktunnelservice.CommandResponse{
		newModernHarpoonCommand(t, "cmd-harpoon-discover-modern", "server/discover", nil, func(tb testing.TB, result map[string]any) {
			if result["resultType"] != "complete" {
				tb.Fatalf("server/discover resultType = %v, want complete", result["resultType"])
			}
			if !resultContains(t, result, "2026-07-28") {
				tb.Fatalf("server/discover result = %v, want 2026-07-28 support", result)
			}
		}),
		newModernHarpoonCommand(t, "cmd-harpoon-tools-list-modern", "tools/list", nil, func(tb testing.TB, result map[string]any) {
			if result["resultType"] != "complete" {
				tb.Fatalf("tools/list resultType = %v, want complete", result["resultType"])
			}
			if !resultContains(t, result, "list_targets") || !resultContains(t, result, "call_target") {
				tb.Fatalf("tools/list result = %v, want Harpoon tools", result)
			}
		}),
		newModernHarpoonCommand(t, "cmd-harpoon-tools-call-modern", "tools/call", map[string]any{
			"name":      "list_targets",
			"arguments": map[string]any{},
		}, func(tb testing.TB, result map[string]any) {
			if result["resultType"] != "complete" {
				tb.Fatalf("tools/call resultType = %v, want complete", result["resultType"])
			}
			if !resultContains(t, result, "seed") {
				tb.Fatalf("tools/call result = %v, want seed target", result)
			}
		}),
	}

	h := harnesspkg.NewHarness(
		t,
		harnesspkg.WithHarpoonInMemoryTransport(),
		harnesspkg.WithClientConfig(func(cfg *config.Config) {
			cfg.Logging.Level = slog.LevelDebug
			cfg.Harpoon.AllowPlaintextHTTP = true
			cfg.Harpoon.Targets = []config.HarpoonTarget{{
				Label:       "seed",
				Description: "seed target for routable harpoon channel",
				BaseURL:     mustParseURL(t, targetServer.URL),
			}}
		}),
		harnesspkg.WithControlPlaneOptions(mocktunnelservice.WithCommandResponses(commands...)),
	)

	h.ExecuteScenarious(t)

	if got := len(h.ControlPlane.ReceivedResponses(mocktunnelservice.ResponseMatchMatched)); got != len(commands) {
		t.Fatalf("matched self-contained requests = %d, want %d", got, len(commands))
	}
	if got := len(h.ControlPlane.DeliveredCommands()); got != len(commands) {
		t.Fatalf("delivered self-contained requests = %d, want %d", got, len(commands))
	}
}

func TestHarpoonChannelSelfContainedRequestsHandoverAcrossRedundantClients(t *testing.T) {
	var targetCalls atomic.Int32
	targetServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		targetCalls.Add(1)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"ok":true}`))
	}))
	defer targetServer.Close()

	const timeout = 5 * time.Second
	var (
		primaryClient   *harnesspkg.TunnelClient
		secondaryClient *harnesspkg.TunnelClient
		h               *harnesspkg.Harness
	)
	primaryReady := make(chan struct{})
	secondaryReady := make(chan struct{})
	handoverErrors := make(chan error, 1)

	discoverCommand := newModernHarpoonCommand(t, "cmd-harpoon-redundant-discover", "server/discover", nil, func(tb testing.TB, result map[string]any) {
		if !resultContains(t, result, "2026-07-28") {
			tb.Fatalf("server/discover result = %v, want 2026-07-28 support", result)
		}
	})
	discoverCommand.DeliverAfter = primaryReady
	discoverAssert := discoverCommand.ExpectedResponses[0].Assert
	discoverCommand.ExpectedResponses[0].Assert = func(tb testing.TB, resp mocktunnelservice.ReceivedResponse) {
		discoverAssert(tb, resp)
		go func() {
			ctx, cancel := context.WithTimeout(context.Background(), timeout)
			defer cancel()
			if err := waitForActiveHarpoonPollRequests(ctx, h, primaryClient); err != nil {
				handoverErrors <- err
				close(secondaryReady)
				return
			}
			if err := primaryClient.PausePoller(ctx); err != nil {
				handoverErrors <- fmt.Errorf("pause primary Harpoon poller: %w", err)
				close(secondaryReady)
				return
			}
			if err := waitForNoActiveHarpoonPollRequests(ctx, h, primaryClient); err != nil {
				handoverErrors <- err
				close(secondaryReady)
				return
			}
			pollsBeforeHandover := secondaryClient.PollCount()
			secondaryClient.UnpausePoller()
			if err := secondaryClient.WaitForPolls(ctx, pollsBeforeHandover+1); err != nil {
				handoverErrors <- fmt.Errorf("wait for secondary Harpoon poll: %w", err)
				close(secondaryReady)
				return
			}
			close(secondaryReady)
		}()
	}

	toolsListCommand := newModernHarpoonCommand(t, "cmd-harpoon-redundant-tools-list", "tools/list", nil, func(tb testing.TB, result map[string]any) {
		if !resultContains(t, result, "list_targets") || !resultContains(t, result, "call_target") {
			tb.Fatalf("tools/list result = %v, want Harpoon tools", result)
		}
	})
	toolsListCommand.DeliverAfter = secondaryReady

	callTargetCommand := newModernHarpoonCommand(t, "cmd-harpoon-redundant-call-target", "tools/call", map[string]any{
		"name": "call_target",
		"arguments": map[string]any{
			"label":   "seed",
			"method":  "GET",
			"headers": map[string]any{},
		},
	}, func(tb testing.TB, result map[string]any) {
		if !resultContains(t, result, "status_code") {
			tb.Fatalf("tools/call result = %v, want call_target response", result)
		}
	})
	callTargetCommand.DeliverAfter = secondaryReady

	h = harnesspkg.NewHarness(
		t,
		harnesspkg.WithHarpoonInMemoryTransport(),
		harnesspkg.WithScenarioTimeout(15*time.Second),
		harnesspkg.WithClientConfig(func(cfg *config.Config) {
			cfg.Logging.Level = slog.LevelDebug
			cfg.Harpoon.AllowPlaintextHTTP = true
			cfg.Harpoon.Targets = []config.HarpoonTarget{{
				Label:       "seed",
				Description: "identical static target for redundant Harpoon clients",
				BaseURL:     mustParseURL(t, targetServer.URL),
			}}
		}),
		harnesspkg.WithControlPlaneOptions(
			mocktunnelservice.WithPollWaitLimit(time.Second),
			mocktunnelservice.WithCommandResponses(discoverCommand, toolsListCommand, callTargetCommand),
		),
		harnesspkg.WithAfterClientStart(func(h *harnesspkg.Harness) {
			primaryClient = h.PrimaryClient()
			secondaryClient = h.StartAdditionalClient(t)
			waitForActiveHarpoonPollers(t, h, primaryClient, secondaryClient)
			ctx, cancel := context.WithTimeout(context.Background(), timeout)
			defer cancel()
			if err := waitForActiveHarpoonPollRequests(ctx, h, secondaryClient); err != nil {
				t.Fatalf("wait for active secondary Harpoon poller: %v", err)
			}
			if err := secondaryClient.PausePoller(ctx); err != nil {
				t.Fatalf("pause secondary Harpoon poller: %v", err)
			}
			if err := waitForNoActiveHarpoonPollRequests(ctx, h, secondaryClient); err != nil {
				t.Fatalf("drain secondary Harpoon poller: %v", err)
			}
			close(primaryReady)
		}),
	)

	h.ExecuteScenarious(t)

	select {
	case err := <-handoverErrors:
		t.Fatal(err)
	default:
	}
	if got := targetCalls.Load(); got != 1 {
		t.Fatalf("call_target downstream requests = %d, want 1", got)
	}
	assertUniqueHarpoonCommandIDs(t, h.ControlPlane.DeliveredCommands(), 3)
	matched := h.ControlPlane.ReceivedResponses(mocktunnelservice.ResponseMatchMatched)
	if len(matched) != 3 {
		t.Fatalf("matched redundant Harpoon responses = %d, want 3", len(matched))
	}
	if unexpected := h.ControlPlane.ReceivedResponses(mocktunnelservice.ResponseMatchUnexpected); len(unexpected) != 0 {
		t.Fatalf("unexpected redundant Harpoon responses: got %d", len(unexpected))
	}
	assertHarpoonResponseAttribution(t, h.ControlPlane.ReceivedHTTPRequests(), map[string]int{
		primaryClient.Name():   1,
		secondaryClient.Name(): 2,
	})
}

func TestHarpoonStartupCatalogDigestMatchesRedundantClientsE2E(t *testing.T) {
	oauthServer := newStartupCatalogDigestOAuthServer(t)

	capture := newStartupCatalogDigestCapture()
	h := newStartupCatalogDigestHarness(t, oauthServer.URL, "test-api-key", capture, func(h *harnesspkg.Harness) {
		h.StartAdditionalClient(t)
		capture.WaitForRecords(t, 2)
	})
	h.ExecuteScenarious(t)

	redundantRecords := capture.Records(t)
	if len(redundantRecords) != 2 {
		t.Fatalf("startup catalog digest records = %d, want 2", len(redundantRecords))
	}
	firstDigest := startupCatalogDigestFromRecord(t, redundantRecords[0])
	secondDigest := startupCatalogDigestFromRecord(t, redundantRecords[1])
	if firstDigest != secondDigest {
		t.Fatalf("redundant startup catalog digests differ: %q != %q", firstDigest, secondDigest)
	}

	changedTargetDigest := runSingleStartupCatalogDigestHarness(t, oauthServer.URL+"/changed", "test-api-key")
	if changedTargetDigest == firstDigest {
		t.Fatal("changed startup target emitted the same catalog digest")
	}

	differentKeyDigest := runSingleStartupCatalogDigestHarness(t, oauthServer.URL, "different-test-api-key")
	if differentKeyDigest == firstDigest {
		t.Fatal("different runtime API key emitted the same catalog digest")
	}
}

func newStartupCatalogDigestOAuthServer(t *testing.T) *httptest.Server {
	t.Helper()

	var server *httptest.Server
	server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/.well-known/oauth-protected-resource":
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"resource":"` + server.URL + `/resource"}`))
		case "/.well-known/oauth-protected-resource/changed":
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"resource":"` + server.URL + `/resource-changed"}`))
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(server.Close)
	return server
}

func newStartupCatalogDigestHarness(
	t *testing.T,
	mcpURL string,
	apiKey string,
	capture *startupCatalogDigestCapture,
	afterStart func(*harnesspkg.Harness),
) *harnesspkg.Harness {
	t.Helper()
	return harnesspkg.NewHarness(
		t,
		harnesspkg.WithAPIKey(apiKey),
		harnesspkg.WithLogWriter(capture),
		harnesspkg.WithPreserveClientURLs(),
		harnesspkg.WithClientConfig(func(cfg *config.Config) {
			cfg.Logging.Level = slog.LevelInfo
			cfg.Logging.Format = config.LogFormatJSON
			cfg.MCP.ServerURL = mustParseURL(t, mcpURL)
			cfg.MCP.TransportKind = config.MCPTransportHTTPStreamable
			cfg.Harpoon.AllowPlaintextHTTP = true
			cfg.Harpoon.HostClassifier.IncludeLoopback = true
		}),
		harnesspkg.WithAfterClientStart(afterStart),
	)
}

func runSingleStartupCatalogDigestHarness(t *testing.T, targetURL, apiKey string) string {
	t.Helper()
	capture := newStartupCatalogDigestCapture()
	h := newStartupCatalogDigestHarness(t, targetURL, apiKey, capture, func(*harnesspkg.Harness) {
		capture.WaitForRecords(t, 1)
	})
	h.ExecuteScenarious(t)
	records := capture.Records(t)
	if len(records) != 1 {
		t.Fatalf("startup catalog digest records = %d, want 1", len(records))
	}
	return startupCatalogDigestFromRecord(t, records[0])
}

func startupCatalogDigestFromRecord(t *testing.T, record map[string]any) string {
	t.Helper()
	if got := record["msg"]; got != "harpoon startup catalog digest" {
		t.Fatalf("startup catalog digest message = %v", got)
	}
	if got := record["catalog_digest_version"]; got != float64(1) {
		t.Fatalf("startup catalog digest version = %v, want 1", got)
	}
	targetCount, ok := record["target_count"].(float64)
	if !ok || targetCount < 1 {
		t.Fatalf("startup catalog target count = %v, want at least 1 discovered target", record["target_count"])
	}
	if got := record["digest_scope"]; got != "startup" {
		t.Fatalf("startup catalog digest scope = %v, want startup", got)
	}
	if got := record["comparability_scope"]; got != "same_tunnel_and_runtime_key" {
		t.Fatalf("startup catalog comparability scope = %v", got)
	}
	digest, ok := record["catalog_digest"].(string)
	if !ok || !strings.HasPrefix(digest, "hmac-sha256:v1:") {
		t.Fatalf("startup catalog digest = %v", record["catalog_digest"])
	}
	return digest
}

type startupCatalogDigestCapture struct {
	mu      sync.Mutex
	records [][]byte
	notify  chan struct{}
}

func newStartupCatalogDigestCapture() *startupCatalogDigestCapture {
	return &startupCatalogDigestCapture{notify: make(chan struct{}, 8)}
}

func (c *startupCatalogDigestCapture) Write(p []byte) (int, error) {
	if !bytes.Contains(p, []byte("harpoon startup catalog digest")) {
		return len(p), nil
	}
	record := append([]byte(nil), bytes.TrimSpace(p)...)
	c.mu.Lock()
	c.records = append(c.records, record)
	c.mu.Unlock()
	select {
	case c.notify <- struct{}{}:
	default:
	}
	return len(p), nil
}

func (c *startupCatalogDigestCapture) WaitForRecords(t *testing.T, want int) {
	t.Helper()
	for {
		c.mu.Lock()
		got := len(c.records)
		c.mu.Unlock()
		if got >= want {
			return
		}
		select {
		case <-c.notify:
		case <-time.After(2 * time.Second):
			t.Fatalf("timed out waiting for startup catalog digests: got %d want %d", got, want)
		}
	}
}

func (c *startupCatalogDigestCapture) Records(t *testing.T) []map[string]any {
	t.Helper()
	c.mu.Lock()
	rawRecords := make([][]byte, len(c.records))
	for idx, record := range c.records {
		rawRecords[idx] = append([]byte(nil), record...)
	}
	c.mu.Unlock()

	records := make([]map[string]any, 0, len(rawRecords))
	for _, raw := range rawRecords {
		var record map[string]any
		if err := json.Unmarshal(raw, &record); err != nil {
			t.Fatalf("decode startup catalog digest log: %v", err)
		}
		records = append(records, record)
	}
	return records
}

func assertUniqueHarpoonCommandIDs(t *testing.T, commands []json.RawMessage, want int) {
	t.Helper()
	if len(commands) != want {
		t.Fatalf("delivered Harpoon commands = %d, want %d", len(commands), want)
	}
	seen := make(map[string]struct{}, len(commands))
	for _, command := range commands {
		var payload struct {
			RequestID string `json:"request_id"`
		}
		if err := json.Unmarshal(command, &payload); err != nil {
			t.Fatalf("decode delivered Harpoon command: %v", err)
		}
		if payload.RequestID == "" {
			t.Fatal("delivered Harpoon command has empty request_id")
		}
		if _, ok := seen[payload.RequestID]; ok {
			t.Fatalf("duplicate delivered Harpoon request_id %q", payload.RequestID)
		}
		seen[payload.RequestID] = struct{}{}
	}
}
func newModernHarpoonCommand(
	t *testing.T,
	requestID string,
	method string,
	extraParams map[string]any,
	assertResult func(testing.TB, map[string]any),
) mocktunnelservice.CommandResponse {
	t.Helper()

	params := map[string]any{
		"_meta": map[string]any{
			"io.modelcontextprotocol/protocolVersion":    "2026-07-28",
			"io.modelcontextprotocol/clientInfo":         map[string]any{"name": "harpoon-modern-e2e", "version": "0.0.1"},
			"io.modelcontextprotocol/clientCapabilities": map[string]any{},
		},
	}
	for key, value := range extraParams {
		params[key] = value
	}
	payload, err := json.Marshal(map[string]any{
		"jsonrpc": "2.0",
		"id":      requestID,
		"method":  method,
		"params":  params,
	})
	if err != nil {
		t.Fatalf("marshal modern Harpoon command: %v", err)
	}

	return mocktunnelservice.CommandResponse{
		Command: newChannelCommand(requestID, "harpoon", json.RawMessage(payload), nil),
		ExpectedResponses: []mocktunnelservice.ExpectedResponse{{
			RequestID: requestID,
			Assert: func(tb testing.TB, resp mocktunnelservice.ReceivedResponse) {
				if tb == nil {
					tb = t
				}
				tb.Helper()
				if resp.ResponseType != string(wiretypes.ResponsePayloadJSONRPC) || resp.ResponseCode != http.StatusOK {
					tb.Fatalf("%s response = (%q, %d), want JSON-RPC 200", method, resp.ResponseType, resp.ResponseCode)
				}
				var response struct {
					Result map[string]any  `json:"result"`
					Error  json.RawMessage `json:"error"`
				}
				if err := json.Unmarshal(resp.JSONResponse, &response); err != nil {
					tb.Fatalf("decode %s response: %v", method, err)
				}
				if len(response.Error) != 0 {
					tb.Fatalf("%s returned JSON-RPC error: %s", method, response.Error)
				}
				if response.Result == nil {
					tb.Fatalf("%s response missing result", method)
				}
				assertResult(tb, response.Result)
			},
		}},
	}
}

func resultContains(t *testing.T, result map[string]any, want string) bool {
	t.Helper()
	encoded, err := json.Marshal(result)
	if err != nil {
		t.Fatalf("marshal result: %v", err)
	}
	return strings.Contains(string(encoded), want)
}

func newChannelCommand(
	requestID string,
	channel string,
	jsonrpcPayload json.RawMessage,
	headers http.Header,
) json.RawMessage {
	command := map[string]any{
		"command_type": "jsonrpc",
		"request_id":   requestID,
		"jsonrpc":      jsonrpcPayload,
		"created_at":   time.Now().UTC().Format(time.RFC3339),
		"shard_token":  requestID,
		"channel":      channel,
	}
	if headers != nil {
		command["headers"] = headers
	}
	data, _ := json.Marshal(command)
	return json.RawMessage(data)
}
