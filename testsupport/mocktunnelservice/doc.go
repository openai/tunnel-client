package mocktunnelservice

import "encoding/json"

// Package mocktunnelservice provides helpers that simulate tunnel-service control
// plane endpoints for exercising the tunnel client in tests.
//
// Use options such as WithTunnelID, WithAPIKey,
// WithInitializationPhaseCommands, WithSessionHeaderPropagation, and
// WithCommandResponses to script the behavior your test expects before
// calling Start with a testing.TB.

// ExampleNewMockTunnelService_basic configures a mock service with a single
// scripted command and expected response.
func ExampleNewMockTunnelService_basic() {
	mock := NewMockTunnelService(
		WithTunnelID("cli-tunnel"),
		WithAPIKey("test-api-key"),
		WithCommandResponses(
			CommandResponse{
				Command: NewCommand(
					"cmd-1",
					json.RawMessage(`{"jsonrpc":"2.0","id":1,"method":"ping"}`),
					nil,
				),
				ExpectedResponses: []ExpectedResponse{{RequestID: "cmd-1"}},
			},
		),
	)

	_ = mock // In tests, call mock.Start(t) and point the tunnel client at mock.BaseURL().
}

// ExampleMockTunnelService_sessionPropagation enables session propagation so
// subsequent commands receive the MCP session headers captured from responses.
func ExampleMockTunnelService_sessionPropagation() {
	mock := NewMockTunnelService(
		WithSessionHeaderPropagation(),
		WithCommandResponses(
			CommandResponse{
				Command: NewCommand(
					"cmd-1",
					json.RawMessage(`{"jsonrpc":"2.0","id":1,"method":"init"}`),
					nil,
				),
				ExpectedResponses: []ExpectedResponse{{RequestID: "cmd-1"}},
			},
			CommandResponse{
				Command: NewCommand(
					"cmd-2",
					json.RawMessage(`{"jsonrpc":"2.0","id":2,"method":"ping"}`),
					nil,
				),
				ExpectedResponses: []ExpectedResponse{{RequestID: "cmd-2"}},
			},
		),
	)

	_ = mock
}
