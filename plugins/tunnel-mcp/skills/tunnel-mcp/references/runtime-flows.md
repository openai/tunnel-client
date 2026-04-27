# Runtime flows

Use `tunnel-client runtimes ...` for native runtime lifecycle management. The
plugin router is only a thin wrapper over this command family.

Create or reuse a remote tunnel alias:

- `tunnel-client runtimes create --alias docs-mcp --organization-id org_123`

Connect a local HTTP MCP server:

- `tunnel-client runtimes connect --alias docs-mcp --organization-id org_123 --mcp-server-url http://127.0.0.1:3001/mcp`

Connect a local stdio MCP server:

- `tunnel-client runtimes connect --alias docs-mcp --organization-id org_123 --mcp-command "python /path/to/server.py"`

Attach to an existing tunnel without admin CRUD:

- `tunnel-client runtimes connect --alias existing-mcp --tunnel-id tunnel_... --runtime-api-key env:TUNNEL_RUNTIME_KEY --mcp-command "python /path/to/server.py"`

Inspect, list, or stop managed local runtimes:

- `tunnel-client runtimes list`
- `tunnel-client runtimes status docs-mcp`
- `tunnel-client runtimes stop docs-mcp`
- `tunnel-client runtimes rm docs-mcp`

`connect` success means the local runtime is actually launched and health is
reachable, not merely that a launch command was issued.
