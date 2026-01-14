package mocktunnelservice

import (
	"context"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	wiretypes "go.openai.org/api/tunnel-client/pkg/controlplane/wiretypes"
)

const (
	defaultAPIKey                    = "mock-api-key"
	defaultTunnelID                  = "mock-tunnel"
	defaultPollWaitLimit             = 30 * time.Millisecond
	sessionHeaderKey                 = "Mcp-Session-Id"
	initializationCommandRequestID   = "initialization::initialize"
	initializedNotificationRequestID = "initialization::notifications/initialized"
	initializationAcceptHeader       = "application/json, text/event-stream"
)

// Option configures a MockTunnelService instance.
type Option func(*MockTunnelService)

// WithAPIKey overrides the randomly generated API key used by the mock.
func WithAPIKey(key string) Option {
	return func(m *MockTunnelService) {
		m.apiKey = key
	}
}

// WithTunnelID overrides the tunnel identifier the mock expects in request paths.
func WithTunnelID(id string) Option {
	trimmed := strings.TrimSpace(id)
	return func(mock *MockTunnelService) {
		mock.tunnelID = trimmed
	}
}

// WithPollWaitLimit overrides the amount of time GET /poll should block
// before returning an empty command list.
func WithPollWaitLimit(limit time.Duration) Option {
	return func(mock *MockTunnelService) {
		mock.pollWaitLimit = limit
	}
}

// WithSessionHeaderPropagation enables automatic propagation of MCP session headers between
// responses and subsequent commands.
func WithSessionHeaderPropagation() Option {
	return func(mock *MockTunnelService) {
		mock.autoSessionMutators = true
	}
}

// ExpectedResponse defines one expected POST /v1/tunnel/{tunnel_id}/response payload.
type ExpectedResponse struct {
	RequestID   string
	Headers     http.Header
	Assert      func(testing.TB, ReceivedResponse)
	PostProcess ResponsePostProcessor
}

// ReceivedResponse captures a single payload posted to the mock tunnel-service.
type ReceivedResponse struct {
	RequestID       string
	JSONResponse    json.RawMessage
	ResponseHeaders http.Header
	ResponseCode    int
	ResponseType    string
	MatchedCommand  bool
}

// CommandMutator can modify a command payload before it is delivered to the client.
type CommandMutator func(json.RawMessage, SharedStorage) json.RawMessage

// ResponsePostProcessor captures data from a received response into shared storage.
type ResponsePostProcessor func(ReceivedResponse, SharedStorage)

// SharedStorage exposes a concurrency-safe map that command and response processors can use.
type SharedStorage interface {
	Get(key string) (string, bool)
	Set(key, value string)
	Delete(key string)
}

// ResponseMatchFilter controls which received responses are returned when querying the mock.
type ResponseMatchFilter int

const (
	// ResponseMatchAll returns every received response regardless of whether it matched a command.
	ResponseMatchAll ResponseMatchFilter = iota
	// ResponseMatchUnexpected returns only responses that did not correspond to a delivered command.
	ResponseMatchUnexpected
	// ResponseMatchMatched returns only responses that matched an existing command.
	ResponseMatchMatched
)

func (f ResponseMatchFilter) includes(matched bool) bool {
	switch f {
	case ResponseMatchMatched:
		return matched
	case ResponseMatchUnexpected:
		return !matched
	case ResponseMatchAll:
		fallthrough
	default:
		return true
	}
}

type sharedStorage struct {
	mu   sync.RWMutex
	data map[string]string
}

func newSharedStorage() *sharedStorage {
	return &sharedStorage{
		data: make(map[string]string),
	}
}

func (s *sharedStorage) Get(key string) (string, bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	val, ok := s.data[key]
	return val, ok
}

func (s *sharedStorage) Set(key string, value string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.data[key] = value
}

func (s *sharedStorage) Delete(key string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.data, key)
}

func (s *sharedStorage) Snapshot() map[string]string {
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := make(map[string]string, len(s.data))
	for k, v := range s.data {
		out[k] = v
	}
	return out
}

type polledCommandEnvelope struct {
	Commands []json.RawMessage `json:"commands"`
}

// CommandResponse ties a polled command to the response the mock expects.
type CommandResponse struct {
	Command           json.RawMessage
	CommandMutator    CommandMutator
	ExpectedResponses []ExpectedResponse
}

type scriptedCommand struct {
	command       json.RawMessage
	expected      []ExpectedResponse
	responseIndex int
	mutator       CommandMutator
	delivered     bool
	completed     bool
}

func (s *scriptedCommand) remainingResponses() int {
	if s.completed {
		return 0
	}
	if s.responseIndex >= len(s.expected) {
		return 0
	}
	return len(s.expected) - s.responseIndex
}

// MockTunnelService simulates the subset of tunnel-service endpoints the control-plane client calls.
type MockTunnelService struct {
	apiKey   string
	tunnelID string

	mu                  sync.Mutex
	script              []*scriptedCommand
	received            []ReceivedResponse
	delivered           []json.RawMessage
	stateCh             chan struct{}
	pollWaitLimit       time.Duration
	server              *httptest.Server
	baseURL             *url.URL
	closeOnce           sync.Once
	storage             *sharedStorage
	autoSessionMutators bool

	tb atomic.Value // testing.TB
}

// NewMockTunnelService constructs a mock with optional configuration overrides.
func NewMockTunnelService(opts ...Option) *MockTunnelService {
	mock := &MockTunnelService{
		apiKey:        defaultAPIKey,
		tunnelID:      defaultTunnelID,
		stateCh:       make(chan struct{}),
		pollWaitLimit: defaultPollWaitLimit,
		storage:       newSharedStorage(),
	}
	for _, opt := range opts {
		opt(mock)
	}
	return mock
}

// WithCommandResponses appends scripted command/response pairs to the queue using the Option pattern.
func WithCommandResponses(commands ...CommandResponse) Option {
	return func(mock *MockTunnelService) {
		mock.appendCommandResponses(commands...)
	}
}

// WithInitializationPhaseCommands pre-populates the mock with the standard MCP
// initialization handshake commands (initialize + notifications/initialized)
// and asserts the responses comply with the protocol. The MCP session id
// captured in the initialize response is persisted into shared storage so it
// can be propagated to subsequent commands via WithSessionHeaderPropagation.
func WithInitializationPhaseCommands() Option {
	return func(mock *MockTunnelService) {
		if mock == nil {
			return
		}
		commands := mock.initializationPhaseCommandResponses()
		if len(commands) == 0 {
			return
		}
		count := len(commands)
		mock.appendCommandResponses(commands...)
		mock.mu.Lock()
		defer mock.mu.Unlock()
		total := len(mock.script)
		if total < count {
			return
		}
		start := total - count
		reordered := make([]*scriptedCommand, 0, total)
		reordered = append(reordered, mock.script[start:]...)
		reordered = append(reordered, mock.script[:start]...)
		mock.script = reordered
	}
}

func (m *MockTunnelService) appendCommandResponses(commands ...CommandResponse) {
	m.mu.Lock()
	defer m.mu.Unlock()
	for _, entry := range commands {
		cmd := cloneJSON(entry.Command)
		if cmd == nil {
			m.failf("command payload must be non-nil")
		}
		expectedResponses := entry.ExpectedResponses
		if len(expectedResponses) == 0 {
			m.failf("expected responses must be non-empty")
		}
		mutator := entry.CommandMutator
		if m.autoSessionMutators {
			if mutator == nil {
				mutator = m.defaultSessionCommandMutator()
			}
		}
		normalizedExpected := make([]ExpectedResponse, len(expectedResponses))
		for i, resp := range expectedResponses {
			expected := resp
			if m.autoSessionMutators && expected.PostProcess == nil {
				expected.PostProcess = m.defaultSessionPostProcessor()
			}
			if expected.Headers != nil {
				expected.Headers = cloneHeader(expected.Headers)
			}
			normalizedExpected[i] = expected
		}
		slot := &scriptedCommand{command: cmd, expected: normalizedExpected, mutator: mutator}
		m.script = append(m.script, slot)
	}
	m.signalStateChangeLocked()
}

// NewCommand builds a raw tunnel command payload with the supplied JSON-RPC request and headers.
func NewCommand(requestID string, jsonrpc json.RawMessage, headers http.Header) json.RawMessage {
	command := map[string]any{
		"command_type": "jsonrpc",
		"request_id":   requestID,
		"jsonrpc":      jsonrpc,
		"created_at":   time.Now().UTC().Format(time.RFC3339),
		"shard_token":  requestID,
	}
	if hdrs := cloneHeader(headers); hdrs != nil {
		command["headers"] = hdrs
	}
	data, _ := json.Marshal(command)
	return json.RawMessage(data)
}

// NewOAuthDiscoveryCommand builds a raw tunnel command payload for OAuth discovery.
func NewOAuthDiscoveryCommand(requestID string, headers http.Header) json.RawMessage {
	command := map[string]any{
		"command_type": "oauth_discovery",
		"request_id":   requestID,
		"created_at":   time.Now().UTC().Format(time.RFC3339),
		"shard_token":  requestID,
	}
	if hdrs := cloneHeader(headers); hdrs != nil {
		command["headers"] = hdrs
	}
	data, _ := json.Marshal(command)
	return json.RawMessage(data)
}

// Start launches the underlying httptest.Server and registers cleanup with t.
func (m *MockTunnelService) Start(t testing.TB) {
	t.Helper()
	m.mu.Lock()
	if m.server != nil {
		m.mu.Unlock()
		t.Fatalf("mock tunnel-service already started")
		return
	}
	handler := http.NewServeMux()
	handler.HandleFunc("/v1/tunnel/", m.handleTunnel)
	listener, err := net.Listen("tcp4", "127.0.0.1:0")
	if err != nil {
		m.mu.Unlock()
		t.Skipf("mock tunnel-service listener unavailable: %v", err)
		return
	}
	server := &httptest.Server{
		Listener: listener,
		Config:   &http.Server{Handler: handler},
	}
	server.Start()
	parsed, err := url.Parse(server.URL)
	if err != nil {
		m.mu.Unlock()
		server.Close()
		t.Fatalf("parse mock server URL: %v", err)
		return
	}
	m.server = server
	m.baseURL = parsed
	m.mu.Unlock()
	m.tb.Store(t)
	t.Cleanup(m.Close)
}

// Close stops the server and asserts all commands and expected responses were consumed.
func (m *MockTunnelService) Close() {
	m.closeOnce.Do(func() {
		var (
			server       *httptest.Server
			remainingCmd int
			remainingExp int
		)
		m.mu.Lock()
		server = m.server
		m.server = nil
		if server != nil {
			m.baseURL = nil
		}
		for _, slot := range m.script {
			if !slot.delivered {
				remainingCmd++
			}
			if !slot.completed {
				remainingExp += slot.remainingResponses()
			}
		}
		m.script = nil
		m.mu.Unlock()
		if server != nil {
			server.Close()
		}
		if remainingCmd != 0 {
			m.failf("mock tunnel-service stopped with %d pending command(s)", remainingCmd)
		}
		if remainingExp != 0 {
			m.failf("mock tunnel-service stopped after missing %d expected response(s)", remainingExp)
		}
	})
}

// BaseURL returns the base URL the client should target.
func (m *MockTunnelService) BaseURL() *url.URL {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.baseURL == nil {
		return nil
	}
	copyURL := *m.baseURL
	return &copyURL
}

// APIKey exposes the API key the client should send in Authorization headers.
func (m *MockTunnelService) APIKey() string {
	return m.apiKey
}

// WaitForResponses blocks until at least n responses have been observed or ctx expires.
func (m *MockTunnelService) WaitForResponses(ctx context.Context, n int) error {
	if n <= 0 {
		return nil
	}
	ticker := time.NewTicker(5 * time.Millisecond)
	defer ticker.Stop()
	for {
		m.mu.Lock()
		count := len(m.received)
		m.mu.Unlock()
		if count >= n {
			return nil
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-ticker.C:
		}
	}
}

// WaitUntilIdle blocks until every scripted command has been delivered and all
// expected responses have been observed, or until ctx expires.
func (m *MockTunnelService) WaitUntilIdle(ctx context.Context) error {
	ticker := time.NewTicker(5 * time.Millisecond)
	defer ticker.Stop()
	for {
		pendingCmd, pendingResp := m.pendingWork()
		if pendingCmd == 0 && pendingResp == 0 {
			return nil
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-ticker.C:
		}
	}
}

func (m *MockTunnelService) pendingWork() (int, int) {
	m.mu.Lock()
	defer m.mu.Unlock()
	var pendingCmd, pendingResp int
	for _, slot := range m.script {
		if !slot.delivered {
			pendingCmd++
			continue
		}
		if !slot.completed {
			pendingResp += slot.remainingResponses()
		}
	}
	return pendingCmd, pendingResp
}

// ReceivedResponses returns a snapshot of all captured responses.
func (m *MockTunnelService) ReceivedResponses(filter ResponseMatchFilter) []ReceivedResponse {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := make([]ReceivedResponse, 0, len(m.received))
	for _, resp := range m.received {
		if !filter.includes(resp.MatchedCommand) {
			continue
		}
		out = append(out, ReceivedResponse{
			RequestID:       resp.RequestID,
			JSONResponse:    cloneJSON(resp.JSONResponse),
			ResponseHeaders: cloneHeader(resp.ResponseHeaders),
			ResponseCode:    resp.ResponseCode,
			ResponseType:    resp.ResponseType,
			MatchedCommand:  resp.MatchedCommand,
		})
	}
	return out
}

// DeliveredCommands returns a snapshot of all commands that have been issued to clients.
func (m *MockTunnelService) DeliveredCommands() []json.RawMessage {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := make([]json.RawMessage, len(m.delivered))
	for i, cmd := range m.delivered {
		out[i] = cloneJSON(cmd)
	}
	return out
}

// PendingScriptDebugInfo returns the outstanding script commands that have not
// yet been delivered as well as those waiting on a response.
func (m *MockTunnelService) PendingScriptDebugInfo() (pendingCommands []json.RawMessage, awaitingResponses []json.RawMessage) {
	m.mu.Lock()
	defer m.mu.Unlock()
	for _, slot := range m.script {
		if !slot.delivered {
			pendingCommands = append(pendingCommands, cloneJSON(slot.command))
			continue
		}
		if !slot.completed {
			awaitingResponses = append(awaitingResponses, cloneJSON(slot.command))
		}
	}
	return pendingCommands, awaitingResponses
}

// SharedStorageSnapshot returns a copy of the shared storage contents.
func (m *MockTunnelService) SharedStorageSnapshot() map[string]string {
	if m.storage == nil {
		return map[string]string{}
	}
	return m.storage.Snapshot()
}

func (m *MockTunnelService) handleTunnel(w http.ResponseWriter, r *http.Request) {
	switch {
	case strings.HasSuffix(r.URL.Path, "/poll"):
		m.handlePoll(w, r)
	case strings.HasSuffix(r.URL.Path, "/response"):
		m.handleResponse(w, r)
	default:
		http.NotFound(w, r)
	}
}

func (m *MockTunnelService) handlePoll(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	m.assertAuthHeaders(r)
	if got := m.extractTunnelID(r.URL.Path, "/poll"); got != m.tunnelID {
		m.failf("unexpected tunnel_id %q in poll path", got)
	}
	waitLimit := m.pollWaitLimit
	if waitLimit <= 0 {
		waitLimit = defaultPollWaitLimit
	}
	timer := time.NewTimer(waitLimit)
	defer timer.Stop()
	for {
		m.mu.Lock()
		cmd, ok := m.nextCommandLocked()
		var state <-chan struct{}
		if !ok {
			state = m.stateCh
		}
		m.mu.Unlock()
		if ok {
			m.writeCommandEnvelope(w, []json.RawMessage{cmd})
			return
		}
		select {
		case <-r.Context().Done():
			return
		case <-timer.C:
			m.writeCommandEnvelope(w, nil)
			return
		case <-state:
		}
	}
}

func (m *MockTunnelService) handleResponse(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	m.assertAuthHeaders(r)
	if ct := r.Header.Get("Content-Type"); ct != "application/json" {
		m.failf("unexpected Content-Type %q in response", ct)
	}
	if got := m.extractTunnelID(r.URL.Path, "/response"); got != m.tunnelID {
		m.failf("unexpected tunnel_id %q in response path", got)
	}

	var payload wiretypes.TunnelResponsePayload
	if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
		m.failf("decode response payload: %v", err)
	}

	m.mu.Lock()
	defer m.mu.Unlock()
	slot := m.nextResponseSlotLocked()
	matched := slot != nil
	var expected ExpectedResponse
	if matched {
		expected = slot.expected[slot.responseIndex]
	}
	record := ReceivedResponse{
		RequestID:       payload.RequestID,
		JSONResponse:    cloneJSON(payload.JSONResponse),
		ResponseHeaders: cloneHeader(payload.ResponseHeaders),
		ResponseCode:    payload.ResponseCode,
		ResponseType:    string(payload.ResponseType),
		MatchedCommand:  matched,
	}
	m.received = append(m.received, record)
	if matched {
		if expected.RequestID != "" && expected.RequestID != payload.RequestID {
			m.failf("response out of order: got %q want %q", payload.RequestID, expected.RequestID)
		}
		if expected.Headers != nil && !headersEqual(expected.Headers, payload.ResponseHeaders) {
			m.failf("unexpected resp_headers for %q: got=%v want=%v", payload.RequestID, payload.ResponseHeaders, expected.Headers)
		}
		if expected.Assert != nil {
			if tb, ok := m.tb.Load().(testing.TB); ok && tb != nil {
				tb.Helper()
				expected.Assert(tb, record)
			} else {
				expected.Assert(nil, record)
			}
		}
		if expected.PostProcess != nil {
			expected.PostProcess(record, m.storage)
		}
		slot.responseIndex++
		if slot.responseIndex >= len(slot.expected) {
			slot.completed = true
		}
		m.signalStateChangeLocked()
	}

	w.Header().Set("Content-Type", "application/json")
	_, _ = w.Write([]byte(`{"status":"ok"}`))
}

func (m *MockTunnelService) nextCommandLocked() (json.RawMessage, bool) {
	for i, slot := range m.script {
		if slot.completed {
			continue
		}
		if slot.delivered {
			// waiting for response; no further commands until completed
			return nil, false
		}
		if i == 0 || m.script[i-1].completed {
			slot.delivered = true
			payload := slot.command
			if slot.mutator != nil {
				mutated := slot.mutator(cloneJSON(payload), m.storage)
				if mutated == nil {
					m.failf("command mutator returned nil for scripted command %d", i)
				}
				payload = mutated
			}
			cmd := cloneJSON(payload)
			m.delivered = append(m.delivered, cloneJSON(cmd))
			return cmd, true
		}
		return nil, false
	}
	return nil, false
}

func (m *MockTunnelService) nextResponseSlotLocked() *scriptedCommand {
	for _, slot := range m.script {
		if slot.completed {
			continue
		}
		if slot.remainingResponses() == 0 {
			slot.completed = true
			continue
		}
		if !slot.delivered {
			return nil
		}
		return slot
	}
	return nil
}

func (m *MockTunnelService) signalStateChangeLocked() {
	if m.stateCh == nil {
		m.stateCh = make(chan struct{})
		return
	}
	close(m.stateCh)
	m.stateCh = make(chan struct{})
}

func (m *MockTunnelService) writeCommandEnvelope(w http.ResponseWriter, commands []json.RawMessage) {
	w.Header().Set("Content-Type", "application/json")
	if commands == nil {
		commands = []json.RawMessage{}
	}
	if err := json.NewEncoder(w).Encode(polledCommandEnvelope{Commands: commands}); err != nil {
		m.failf("encode poll response: %v", err)
	}
}

func (m *MockTunnelService) initializationPhaseCommandResponses() []CommandResponse {
	sessionCapture := m.defaultSessionPostProcessor()
	initialize := CommandResponse{
		Command: NewCommand(
			initializationCommandRequestID,
			initializationRequestPayload(),
			initializationRequestHeaders(),
		),
		ExpectedResponses: []ExpectedResponse{{
			RequestID:   initializationCommandRequestID,
			Assert:      assertInitializationResponse,
			PostProcess: func(resp ReceivedResponse, storage SharedStorage) { sessionCapture(resp, storage) },
		}},
	}
	notification := CommandResponse{
		Command: NewCommand(
			initializedNotificationRequestID,
			initializedNotificationPayload(),
			initializedNotificationHeaders(),
		),
		ExpectedResponses: []ExpectedResponse{{
			RequestID: initializedNotificationRequestID,
			Assert:    assertInitializedNotificationResponse,
		}},
	}
	return []CommandResponse{initialize, notification}
}

func initializationRequestHeaders() http.Header {
	return http.Header{
		"Accept":       []string{initializationAcceptHeader},
		"Content-Type": []string{"application/json"},
	}
}

func initializedNotificationHeaders() http.Header {
	return http.Header{
		"Accept":       []string{"application/json"},
		"Content-Type": []string{"application/json"},
	}
}

func initializationRequestPayload() json.RawMessage {
	return mustMarshalJSON(map[string]any{
		"jsonrpc": "2.0",
		"id":      "initialize-0",
		"method":  "initialize",
		"params": map[string]any{
			"protocolVersion": "2025-06-18",
			"capabilities": map[string]any{
				"sampling":    map[string]any{},
				"elicitation": map[string]any{},
				"roots": map[string]any{
					"listChanged": true,
				},
			},
			"clientInfo": map[string]any{
				"name":    "mocktunnelservice",
				"version": "0.0.1",
			},
		},
	})
}

func initializedNotificationPayload() json.RawMessage {
	return mustMarshalJSON(map[string]any{
		"jsonrpc": "2.0",
		"method":  "notifications/initialized",
		"params":  map[string]any{},
	})
}

func assertInitializationResponse(tb testing.TB, resp ReceivedResponse) {
	if tb != nil {
		tb.Helper()
	}
	if resp.ResponseType != string(wiretypes.ResponsePayloadJSONRPC) {
		failAssertion(tb, "initialize response must be JSON-RPC, got %q", resp.ResponseType)
	}
	if resp.ResponseCode < 200 || resp.ResponseCode >= 300 {
		failAssertion(tb, "initialize response returned status %d", resp.ResponseCode)
	}
	if len(resp.JSONResponse) == 0 {
		failAssertion(tb, "initialize response missing resp_json payload")
	}
	if resp.ResponseHeaders.Get(sessionHeaderKey) == "" {
		failAssertion(tb, "initialize response missing %s header", sessionHeaderKey)
	}
	var envelope struct {
		JSONRPC string `json:"jsonrpc"`
		Result  struct {
			ProtocolVersion string `json:"protocolVersion"`
			ServerInfo      struct {
				Name    string `json:"name"`
				Version string `json:"version"`
			} `json:"serverInfo"`
		} `json:"result"`
		Error json.RawMessage `json:"error"`
	}
	if err := json.Unmarshal(resp.JSONResponse, &envelope); err != nil {
		failAssertion(tb, "initialize response invalid JSON-RPC: %v", err)
		return
	}
	if envelope.JSONRPC != "2.0" {
		failAssertion(tb, "initialize response jsonrpc was %q", envelope.JSONRPC)
	}
	if len(envelope.Error) != 0 {
		failAssertion(tb, "initialize response contained error payload: %s", string(envelope.Error))
	}
	if envelope.Result.ProtocolVersion == "" {
		failAssertion(tb, "initialize result missing protocolVersion")
	}
	if envelope.Result.ServerInfo.Name == "" || envelope.Result.ServerInfo.Version == "" {
		failAssertion(tb, "initialize result missing serverInfo fields: %+v", envelope.Result.ServerInfo)
	}
}

func assertInitializedNotificationResponse(tb testing.TB, resp ReceivedResponse) {
	if tb != nil {
		tb.Helper()
	}
	if resp.ResponseType != string(wiretypes.ResponsePayloadNotifyAck) {
		failAssertion(tb, "notifications/initialized must use notify_ack, got %q", resp.ResponseType)
	}
	if resp.ResponseCode < 200 || resp.ResponseCode >= 300 {
		failAssertion(tb, "notifications/initialized ack returned status %d", resp.ResponseCode)
	}
	if len(resp.JSONResponse) != 0 {
		failAssertion(tb, "notifications/initialized ack should not include resp_json payload")
	}
}

func (m *MockTunnelService) defaultSessionCommandMutator() CommandMutator {
	return func(payload json.RawMessage, storage SharedStorage) json.RawMessage {
		if storage == nil {
			return payload
		}
		var envelope wiretypes.RawJSONRPCPolledCommand
		if err := json.Unmarshal(payload, &envelope); err != nil {
			m.failf("session mutator decode command payload: %v", err)
		}
		if envelope.Headers == nil {
			envelope.Headers = http.Header{}
		}
		if value, ok := storage.Get(sessionHeaderKey); ok && value != "" {
			envelope.Headers.Set(sessionHeaderKey, value)
		}
		mutated, err := json.Marshal(envelope)
		if err != nil {
			m.failf("session mutator encode command payload: %v", err)
		}
		return json.RawMessage(mutated)
	}
}

func (m *MockTunnelService) defaultSessionPostProcessor() ResponsePostProcessor {
	return func(resp ReceivedResponse, storage SharedStorage) {
		if storage == nil {
			return
		}
		if value := resp.ResponseHeaders.Get(sessionHeaderKey); value != "" {
			storage.Set(sessionHeaderKey, value)
		}
	}
}

func (m *MockTunnelService) assertAuthHeaders(r *http.Request) {
	auth := r.Header.Get("Authorization")
	expected := "Bearer " + m.apiKey
	if auth != expected {
		m.failf("unexpected Authorization header %q", auth)
	}
	if accept := r.Header.Get("Accept"); accept != "application/json" {
		m.failf("unexpected Accept header %q", accept)
	}
}

func (m *MockTunnelService) extractTunnelID(path, suffix string) string {
	const prefix = "/v1/tunnel/"
	if !strings.HasPrefix(path, prefix) || !strings.HasSuffix(path, suffix) {
		m.failf("unexpected path %q", path)
	}
	rest := strings.TrimPrefix(path, prefix)
	rest = strings.TrimSuffix(rest, suffix)
	rest = strings.TrimSuffix(rest, "/")
	if rest == "" {
		m.failf("missing tunnel_id in path %q", path)
	}
	return rest
}

func (m *MockTunnelService) failf(format string, args ...any) {
	if tb, ok := m.tb.Load().(testing.TB); ok && tb != nil {
		tb.Helper()
		tb.Fatalf(format, args...)
		return
	}
	panic(fmt.Sprintf(format, args...))
}

func cloneJSON(raw json.RawMessage) json.RawMessage {
	if raw == nil {
		return nil
	}
	out := make([]byte, len(raw))
	copy(out, raw)
	return json.RawMessage(out)
}

func cloneHeader(h http.Header) http.Header {
	if h == nil {
		return nil
	}
	out := make(http.Header, len(h))
	for k, values := range h {
		copyVals := make([]string, len(values))
		copy(copyVals, values)
		out[k] = copyVals
	}
	return out
}

func headersEqual(a, b http.Header) bool {
	if len(a) != len(b) {
		return false
	}
	for key, values := range a {
		other, ok := b[key]
		if !ok || len(values) != len(other) {
			return false
		}
		for i := range values {
			if values[i] != other[i] {
				return false
			}
		}
	}
	return true
}

func failAssertion(tb testing.TB, format string, args ...any) {
	if tb != nil {
		tb.Fatalf(format, args...)
		return
	}
	panic(fmt.Sprintf(format, args...))
}

func mustMarshalJSON(v any) json.RawMessage {
	data, err := json.Marshal(v)
	if err != nil {
		panic(fmt.Sprintf("mock tunnel-service marshal JSON: %v", err))
	}
	return json.RawMessage(data)
}
