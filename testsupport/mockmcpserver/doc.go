package mockmcpserver

import (
	"encoding/json"
	"fmt"
	"net/http"
)

// Package mockmcpserver provides MCP server doubles that let tests script tool
// responses and capture incoming call metadata without standing up a real MCP
// server. Use Start to run a streamable HTTP server, or StartInMemory/StartStdio
// to run over in-memory or stdio transports.
//
// Configure the server entirely through options such as WithCalls, start it in
// a test with testing.TB, and exercise it with a real MCP client.

// ExampleNewMockMCPServer_withCalls shows how to queue scripted tool calls when
// constructing the mock server.
func ExampleNewMockMCPServer_withCalls() {
	server := NewMockMCPServer(
		WithCalls(
			Call{
				Tool:   "ping",
				Result: json.RawMessage(`{"message":"pong"}`),
				ResponseHeaders: http.Header{
					"X-Mcp-Mode": {"simple"},
				},
			},
			Call{
				Tool: "stream",
				Progress: []ProgressUpdate{
					{Percentage: 50, Message: "halfway"},
				},
				Result: json.RawMessage(`{"complete":true}`),
				ResponseHeaders: http.Header{
					"X-Mcp-Mode": {"stream"},
				},
			},
		),
	)

	_ = server // In tests, call server.Start(t) and point your MCP client at server.BaseURL().
}

// ExampleMockMCPServer_receivedRequests demonstrates inspecting the recorded
// tool invocations after the client finishes calling the MCP server.
func ExampleMockMCPServer_receivedRequests() {
	server := NewMockMCPServer(
		WithCalls(
			Call{
				Tool:   "ping",
				Result: json.RawMessage(`{"message":"pong"}`),
			},
		),
	)

	for _, req := range server.ReceivedRequests() {
		fmt.Printf("%s -> %s\n", req.Tool, string(req.Arguments))
	}
}
