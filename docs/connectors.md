# Connector behavior and onboarding notes

This page documents how ChatGPT, Responses API, AgentKit, and Codex connector
traffic reaches a private MCP server through `tunnel-client`. It is intended for
new contributors who need to reason about the connector path before changing
routing, auth, or transport code.

## What a connector actually calls

A product connector does **not** call the customer's MCP server directly. The
operator creates or selects a tunnel in Tunnels management, configures the
connector with that tunnel, and keeps a matching `tunnel-client run ...` process
alive inside the customer network.

At runtime:

1. The product sends MCP JSON-RPC to the OpenAI tunnel-service MCP endpoint for
   the tunnel.
2. Tunnel service queues the work for the tunnel id.
3. `tunnel-client` long-polls `GET /v1/tunnels/{tunnel_id}/poll` with the
   runtime API key.
4. The dispatcher forwards the command to the configured local MCP binding for
   the command channel.
5. The client posts the terminal response, and any streamed notifications, to
   `POST /v1/tunnels/{tunnel_id}/response`.

The connector-facing MCP endpoint is POST-only for JSON-RPC traffic. A GET to
`/v1/mcp/{tunnel_id}` is not a persistent SSE stream; streaming only happens for
POST requests whose connector request accepts `text/event-stream`.

## First-use checklist for connector setup

Use the CLI as the source of truth before hand-editing YAML:

```bash
tunnel-client help quickstart
tunnel-client profiles samples list
tunnel-client init --sample sample_mcp_stdio_local --profile local-stdio --tunnel-id tunnel_0123456789abcdef0123456789abcdef --mcp-command "python /path/to/server.py"
tunnel-client doctor --profile local-stdio --explain
tunnel-client run --profile local-stdio
```

Create or inspect values in these places:

- Tunnels management: `https://platform.openai.com/settings/organization/tunnels`
- Runtime API keys: `https://platform.openai.com/settings/organization/api-keys`
- Admin API keys: `https://platform.openai.com/settings/organization/admin-keys`
- ChatGPT connector settings: `https://chatgpt.com/#settings/Connectors`

Keep the key split strict:

- `CONTROL_PLANE_TUNNEL_ID` identifies the tunnel. It comes from Tunnels
  management or `tunnel-client admin tunnels create|list|get ...`.
- `CONTROL_PLANE_API_KEY` is the long-lived runtime key for `doctor`, `run`,
  polling, and response posting. `OPENAI_API_KEY` is only a fallback when
  `CONTROL_PLANE_API_KEY` is unset.
- `OPENAI_ADMIN_KEY` is for tunnel CRUD commands. Do not put an admin key in a
  daemon profile that only needs to poll and post responses.

A connector may look correctly configured in ChatGPT while the runtime is still
not usable. Only ask product operators to test discovery or tool calls after
`/readyz` is `200` or `tunnel-client doctor --explain` has explained why the
only remaining probe failure is an expected auth challenge from the MCP server.

## MCP bindings and channels

`tunnel-client` always requires a `main` channel binding. Connector traffic with
an empty channel is normalized to `main`.

Supported customer MCP transports are:

- **Streamable HTTP**: `MCP_SERVER_URL` or `--mcp.server-url`. Use this for MCP
  servers reachable over HTTP(S) from the tunnel-client host. Local deployments
  can keep the logical HTTP URL while dialing it over a Unix-domain socket with
  channel-qualified `unix-socket=<path|env:VAR>`.
- **stdio**: `MCP_COMMAND` or `--mcp.command`. Use this for local MCP servers
  launched as child processes. Stdio bindings do not have an HTTP session id and
  ignore proxy, CA, and mTLS settings.
- **in-memory**: used by tests and embedded/demo flows, not by customer YAML.

Additional logical channels can be configured with channel-qualified entries:

```bash
--mcp.server-url="channel=search,url=https://search-mcp.internal/mcp"
--mcp.command="channel=tools,command=python /srv/tools_mcp.py"
```

The channel name `harpoon` is reserved for the embedded Harpoon MCP server.
Harpoon is routable only when at least one allowlisted target is registered.
When no Harpoon target exists, commands on `harpoon` receive an
`unsupported_channel` response rather than falling back to `main`.

OAuth discovery can make tunnel-service open an internal FastMCP session on
`harpoon`. Seeing `initialize`, `notifications/initialized`, or
`tools/list` on that channel is expected control traffic for the embedded
Harpoon server, not ChatGPT's runtime session with the customer MCP server.
Those internal commands intentionally do not carry the connector's OAuth
bearer token and must not be routed to the customer MCP binding.

## Streaming and notifications

The dispatcher treats JSON-RPC requests and notifications differently:

- JSON-RPC calls with an id are forwarded to the MCP transport, then the client
  reads until it sees a final response with the same id.
- JSON-RPC notifications without an id are acknowledged to tunnel service after
  the downstream write succeeds; the client does not wait for a response.
- Downstream JSON-RPC notifications emitted while a call is in flight are posted
  back as connector stream events. The final JSON-RPC response closes the stream.
- If the downstream connection closes before a final response, the dispatcher
  posts a terminal JSON-RPC error response so the connector does not hang.

The effective connection window is bounded by `MCP_CONNECTION_MAX_TTL`
(default `10m`). Long-running tools should either finish within that window or
stream progress notifications and return a final response before the TTL ends.

## OAuth-protected connector behavior

For OAuth-protected MCP servers, discovery and auth challenges still happen from
inside the customer network:

- The connector's inbound `Authorization` header is forwarded to the MCP server.
- After OAuth completes, runtime `initialize`, `tools/list`, and tool calls
  use the configured `main` MCP endpoint (`/v1/mcp/{tunnel_id}` for a
  tunnel-backed connector). The connector bearer is forwarded only on that
  customer MCP path, not on the internal `harpoon` channel.
- Protected-resource OAuth discovery is represented as a tunnel command and
  executed by `tunnel-client`, using the MCP server URL and the same outbound
  proxy/CA trust as other MCP HTTP traffic.
- OAuth-discovered protected-resource targets follow Harpoon's HTTPS policy. If
  a trusted local-development MCP endpoint uses plaintext HTTP (for example
  `http://127.0.0.1:8765/mcp`), prefer HTTPS or explicitly set
  `--harpoon.allow-plaintext-http` / `HARPOON_ALLOW_PLAINTEXT_HTTP=true`.
- `authorization_servers[0]` from Protected Resource Metadata is the source of
  truth for auth-server metadata enrichment and Harpoon OAuth target
  registration.
- Auth-server metadata with an `issuer` that differs from
  `authorization_servers[0]` is accepted for external enterprise IdP setups;
  the mismatch is preserved in logs and admin state for diagnostics.

The authorization server is not generically tunneled. Registered Harpoon
targets can carry known registration, token, and revocation POST endpoints, but
the supported auto-registered path leaves browser authorization direct and any
unshimmed public endpoint must be reachable by the actor that calls it.

## Environment variables contributors commonly miss

Minimum runtime:

- `CONTROL_PLANE_TUNNEL_ID`
- `CONTROL_PLANE_API_KEY` (or fallback `OPENAI_API_KEY`)
- `MCP_SERVER_URL` **or** `MCP_COMMAND` for the `main` channel

Common deployment additions:

- `CONTROL_PLANE_BASE_URL`: host root only, usually `https://api.openai.com`. <!-- citadel-ignore: public endpoint example for external tunnel-client config -->
  Do not include `/v1/tunnel`.
- `HEALTH_LISTEN_ADDR` and `HEALTH_URL_FILE`: useful when the runtime manager
  needs the resolved `/healthz`, `/readyz`, and `/ui` base URL.
- `TUNNEL_CLIENT_HTTP_PROXY`, `CONTROL_PLANE_HTTP_PROXY`, `MCP_HTTP_PROXY`, and
  `HARPOON_HTTP_PROXY`: explicit proxy references for global/control-plane/MCP/
  Harpoon traffic. These can be raw proxy URLs or `env:VAR` references.
- `HTTP_PROXY`, `HTTPS_PROXY`, and `NO_PROXY`: standard process-level proxy
  fallback when no explicit tunnel-client proxy is set for that route.
- `CA_BUNDLE`: additive custom PEM trust bundle for outbound TLS.
- `MCP_CLIENT_CERT` and `MCP_CLIENT_KEY`: default mTLS client cert/key for
  Streamable HTTP MCP channels.
- `LOG_HTTP_RAW_UNSAFE`: last-resort debugging only; it can log payloads and
  sensitive headers.

## Troubleshooting connector-specific failures

- **Connector discovery fails but `/healthz` is live**: check `/readyz`. Readiness
  is gated on startup probes and OAuth discovery; liveness only means the
  process is running.
- **ChatGPT reports an unreachable connector**: make sure the daemon is still
  running with the same `CONTROL_PLANE_TUNNEL_ID` that the connector selected.
  A stopped local runtime leaves the remote tunnel object behind.
- **404s or doubled paths in logs**: set `CONTROL_PLANE_BASE_URL` to the host
  root, not a pre-prefixed `/v1/tunnels/...` URL.
- **`unsupported_channel`**: the product sent a channel that is not configured,
  or Harpoon has no registered targets. Configure the channel explicitly or add
  a Harpoon target.
- **OAuth issuer mismatch warnings**: issuer mismatch is allowed. Treat the
  warning as diagnostic context unless the authorization server URL itself is
  wrong or unreachable.
- **Private CA or mTLS failures**: use `CA_BUNDLE` for trust, and provide both
  `MCP_CLIENT_CERT` and `MCP_CLIENT_KEY` together. Stdio MCP ignores these
  settings.

## Documentation and screenshot audit notes

The current docs intentionally link to product setup URLs instead of embedding
screenshots of ChatGPT connector settings. That avoids stale UI captures when
product navigation or labels change. The image files under `docs/images/` and
`docs/screenshots/` are support artifacts for admin UI behavior, not required
steps in the connector setup flow. If a future guide adds product screenshots,
include the capture date, product surface, and the exact setting or button name
so reviewers can spot drift quickly.

## Areas that still need product or design input

- Whether the ChatGPT connector settings page should expose a stable deep link
  for a specific tunnel id rather than requiring operators to select or paste it.
- The final user-facing copy for connector errors caused by local daemon
  downtime versus OAuth discovery failures; today contributors infer this from
  `/readyz`, logs, and tunnel-service responses.
- Whether GET requests to connector MCP URLs should return a more explicit
  product-facing diagnostic instead of the current POST-only behavior.
