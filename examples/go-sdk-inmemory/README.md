# In-memory Go SDK example

This example runs an MCP server and Tunnel client in one Go process. The MCP
server uses the MCP Go SDK's in-memory transport, so it does not bind a local
port or launch a stdio child process.

~~~bash
go get github.com/openai/tunnel-client
export CONTROL_PLANE_TUNNEL_ID=tunnel_0123456789abcdef0123456789abcdef
export CONTROL_PLANE_API_KEY=sk-...
go run ./examples/go-sdk-inmemory
~~~

The server exposes an echo tool through the configured OpenAI Tunnel.
