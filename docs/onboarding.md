# Onboarding Guide

This guide helps you get from zero to a working `tunnel-client` process connected to your MCP server.

For the customer-shareable network model and request flow, see
[`architecture.md`](architecture.md).

## 1) Prerequisites

- A reachable MCP server endpoint, or a command that starts a stdio MCP server.
- A tunnel control-plane API key.
- A provisioned `tunnel_id` for your environment.

## 2) First-run paths

If you already have a `tunnel-client` binary, start there. The CLI now embeds
the shortest first-use path:

```bash
tunnel-client help quickstart
tunnel-client help samples
```

The recommended binary-first path is:

```bash
tunnel-client run --embedded-mcp-stub --control-plane.tunnel-id tunnel_0123456789abcdef0123456789abcdef
```

If Codex is installed locally and you want the plugin surface instead of the raw
binary flow, install it directly from the binary:

```bash
tunnel-client plugin codex install
tunnel-client plugin codex uninstall
```

If you want a named profile instead of the one-command demo path:

```bash
tunnel-client init --sample sample_mcp_stdio_local --profile local-stdio --tunnel-id tunnel_0123456789abcdef0123456789abcdef --mcp-command "python /path/to/server.py"
tunnel-client doctor --profile local-stdio --explain
tunnel-client run --profile local-stdio
```

Other fast starts:

- Remote HTTP MCP server with no OAuth/PRMD metadata:
  `tunnel-client init --sample sample_mcp_remote_no_auth --profile remote-http --tunnel-id tunnel_... --mcp-server-url https://mcp.example.com/mcp`
- Outbound proxy or private PKI environment:
  `export HTTPS_PROXY="http://proxy.internal.example.com:8080"`
  `export ENTERPRISE_CA_BUNDLE="/etc/ssl/certs/proxy-root.pem"`
  then
  `tunnel-client init --sample sample_mcp_enterprise_proxy --profile corp-proxy --tunnel-id tunnel_... --mcp-server-url https://mcp.internal.example.com/mcp`
- Embedded demo MCP server for end-to-end validation:
  `tunnel-client dev mcp-stub`
  then
  `tunnel-client init --sample sample_mcp_with_dcr --profile sample_mcp_with_dcr --tunnel-id tunnel_... --mcp-server-url http://127.0.0.1:NNNN/mcp`

The embedded UI is served from the health listener. With the default
`127.0.0.1:8080`, the UI is at `http://127.0.0.1:8080/ui`.

If Codex is installed locally and you want the plugin surface instead of the raw
binary flow, install it directly from the binary:

```bash
tunnel-client codex assistant "Summarize what tunnel-client is for."
tunnel-client codex status
tunnel-client codex plugin install
tunnel-client sessions list
tunnel-client codex plugin uninstall
```
Starter prompts for Codex:

- `Figure out what tunnel-client is for from the binary help, then get me to /ui with the shortest local path.`
- `Use tunnel-client to create or reuse a profile, run doctor --explain, and then start the daemon.`
- `Run tunnel-client codex assistant and summarize what this checkout is for in one sentence.`
- `Install the Codex plugin from the tunnel-client binary, connect the provided tunnel id, and tell me whether the runtime is launched, healthy, or ready.`

## 3) Build from source

From the `tunnel-client` module root:

```bash
go build -o bin/tunnel-client ./cmd/client
```

Or from the monorepo root:

```bash
bazel build //api/tunnel-client/cmd/client:client
./bazel-bin/api/tunnel-client/cmd/client/client
```

After building from source, use `./bin/tunnel-client` or the Bazel output path
unless you add that location to your `PATH`.
## 4) Configure

At minimum, you must set:

- `CONTROL_PLANE_API_KEY`: control-plane authentication.
- `CONTROL_PLANE_TUNNEL_ID`: the tunnel identifier for this deployment.
- One `main` MCP binding:
  - `MCP_SERVER_URL` for a Streamable HTTP MCP endpoint, or
  - `--mcp.command` for a stdio MCP server.

Auth split to keep straight:

- `CONTROL_PLANE_API_KEY` / `OPENAI_API_KEY`: runtime key used by the daemon.
- `tunnel-client admin tunnels get <tunnel_id>` can use that runtime key for
  read-only metadata lookup.
- `admin tunnels list/create/update/delete` require `OPENAI_ADMIN_KEY` or
  `--admin-key`.
- The enterprise proxy sample documents both keys in comments so runtime and
  admin flows stay separate.

Example:

```bash
export CONTROL_PLANE_API_KEY="sk-..."        # preferred
export CONTROL_PLANE_TUNNEL_ID="tunnel_0123456789abcdef0123456789abcdef"
export MCP_SERVER_URL="https://mcp.internal.example.com/mcp"
```

`CONTROL_PLANE_TUNNEL_ID` must match the runtime validator: `tunnel_` followed by 32 lowercase hexadecimal characters.

For the full surface (flags, defaults, advanced knobs), see [`configuration.md`](configuration.md).

### OAuth-protected MCP (supported)

- `Authorization` headers are forwarded through the OpenAI tunnel service to
  your MCP server.
- Custom MCP request headers configured on the app are forwarded through the
  OpenAI tunnel service, except
  internal auth and IP-forwarding transport headers.
- OAuth discovery GETs are forwarded to the tunnel-client; discovery payloads and
  `WWW-Authenticate resource_metadata` are rewritten to OpenAI tunnel-service
  URLs for the same `tunnel_id`.
- `authorization_servers[0]` from PRMD is the only source of truth and metadata
  fetch target for auth-server metadata enrichment and Harpoon OAuth target
  registration.
- Auth-server metadata is accepted even when metadata `issuer` differs from
  `authorization_servers[0]` (external IdP issuer topologies are supported), and
  mismatch diagnostics are retained.
- The authorization server itself is not tunneled. If it is only reachable
  on-prem or behind a firewall and not accessible from the internet or the
  tunnel-client host, the OAuth flow can fail.

## 5) Run

```bash
./bin/tunnel-client run --log.level=info --log.format=struct-text
```

The process will:

- Start polling the OpenAI tunnel service for work.
- Forward JSON-RPC requests to your MCP server.
- Expose health endpoints on `HEALTH_LISTEN_ADDR` (default `:8080`).

## 6) Verify

In another shell:

```bash
curl -fsS "http://127.0.0.1:8080/healthz"
curl -fsS "http://127.0.0.1:8080/readyz"
curl -fsS "http://127.0.0.1:8080/metrics" | head
```

## 7) Next reads

- **Deployments**: [`deployment/overview.md`](deployment/overview.md)
- **Architecture**: [`architecture.md`](architecture.md)
- **Troubleshooting**: [`troubleshooting.md`](troubleshooting.md)
