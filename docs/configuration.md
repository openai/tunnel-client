# Configuration Reference

`tunnel-client` can be configured via CLI flags or environment variables.

- **Precedence**: flags > environment variables > defaults.
- **Requirement**: you must provide a control-plane API key, a tunnel ID, and a `main` MCP channel binding (via `--mcp.server-url` or `--mcp.command`).

## Commands

- `run`: start the tunnel client poll loop.
- `admin tunnels`: manage tunnel metadata via the admin API (`/v1/tunnels*`). Requires an admin key and org/workspace scope flags.
- `tunnel-client` with no subcommand prints help and available commands.

## Control plane

- **Base URL**
  - Flag: `--control-plane.base-url`
  - Env: `CONTROL_PLANE_BASE_URL`
  - Default: `https://api.openai.com`
  - **Important**: this value is treated as the **host root**, not a pre-prefixed path.
    - Correct: `https://api.openai.com`
    - Incorrect: `https://api.openai.com/v1/tunnel` (would create `/v1/tunnel/v1/tunnel/...`)
- **Tunnel ID**
  - Flag: `--control-plane.tunnel-id`
  - Env: `CONTROL_PLANE_TUNNEL_ID`
  - Required: yes
  - Allowed characters: `A–Z a–z 0–9 _ -`
- **API key**
  - Flag: `--control-plane.api-key=env:VARNAME` or `--control-plane.api-key=file:/path/to/secret`
  - Env (preferred): `CONTROL_PLANE_API_KEY`
  - Env (fallback): `OPENAI_API_KEY` (used only if `CONTROL_PLANE_API_KEY` is unset)
  - Required: yes
- **HTTP proxy (optional)**
  - Flag: `--control-plane.http-proxy=<url|env:VAR>`
- **Poll timeout**
  - Flag: `--control-plane.poll-timeout`
  - Env: `CONTROL_PLANE_POLL_TIMEOUT`
  - Default: `30s`
- **Max in-flight buffer**
  - Flag: `--control-plane.max-inflight`
  - Env: `CONTROL_PLANE_MAX_INFLIGHT_REQUESTS`
  - Default: `20` (max `10000`)
- **Extra headers (optional)**
  - Flag (repeatable): `--control-plane.extra-headers "Key: Value"`
  - Env: `CONTROL_PLANE_EXTRA_HEADERS="Key: Value, Key2: Value2"`

## TLS trust (custom CA bundle)

Use a PEM CA bundle to extend (additive to system trust) the trust store for **all** outbound TLS connections
(control plane, MCP HTTP, OAuth discovery, and Harpoon).

- **CA bundle**
  - Flag: `--ca-bundle /path/to/ca-bundle.pem`
  - Env: `CA_BUNDLE`
  - Bundle format: PEM file containing one or more CA certificates.

## Outbound HTTP proxy

Use explicit proxy flags to force tunnel-client traffic through a corporate proxy. Each flag accepts a proxy URL or `env:VAR` reference.

- **Global proxy (all outbound HTTP)**
  - Flag: `--http-proxy=<url|env:VAR>`
  - Applies to control plane, MCP HTTP, OAuth discovery, and Harpoon unless overridden.
- **Control plane proxy**
  - Flag: `--control-plane.http-proxy=<url|env:VAR>`
- **MCP proxy default**
  - Flag: `--mcp.http-proxy=<url|env:VAR>`
  - Per-channel override: `--mcp.server-url="channel=...,url=...,http-proxy=<url|env:VAR>"`
  - Note: stdio MCP bindings ignore proxy settings.
- **Harpoon proxy**
  - Flag: `--harpoon.http-proxy=<url|env:VAR>`

- **Proxy health checks**
  - Flag: `--proxy.check-interval=60s`
  - Env: `PROXY_CHECK_INTERVAL`
  - Default: `60s`

**Precedence (highest to lowest):**
1. Per-target/per-channel proxy flag.
2. MCP default proxy (`--mcp.http-proxy`).
3. Global proxy (`--http-proxy`).
4. Environment (`HTTP_PROXY` / `HTTPS_PROXY` / `NO_PROXY`).

When an explicit proxy flag is set for a target, environment proxy variables (including `NO_PROXY`) are ignored for that target.

## MCP server

- **Server URL**
  - Flag (repeatable): `--mcp.server-url`
  - Env: `MCP_SERVER_URL`
  - Required: yes for the `main` channel (unless `--mcp.command` supplies `main`)
  - Legacy form: `--mcp.server-url=https://main.example.com/mcp` (defaults to `main`)
  - Channel-qualified form: `--mcp.server-url="channel=foo,url=https://foo.example.com/mcp,http-proxy=<url|env:VAR>"`
- **Command (stdio transport)**
  - Flag (repeatable): `--mcp.command`
  - Env: `MCP_COMMAND`
  - Required: yes for the `main` channel (unless `--mcp.server-url` supplies `main`)
  - Legacy form: `--mcp.command="npx -y @org/main-mcp"` (defaults to `main`)
  - Channel-qualified form: `--mcp.command="channel=bar,command=npx -y @org/bar-mcp"`
  - Behavior: spawns the command once and uses the child process stdin/stdout for MCP frames
  - Note: stdio transport does not support MCP sessions
  - Note: when using `MCP_COMMAND` with multiple entries, separate entries with newlines so semicolons remain part of the command.
- **Multiple entries**
  - Flags are repeatable; each entry can target a different channel.
  - Environment variables accept newline-delimited entries.
  - Configuring both `--mcp.server-url` and `--mcp.command` is allowed as long as they target **different** channels.
  - If no `main` binding is configured, startup fails with `main channel is required`.
- **Connection max TTL**
  - Flag: `--mcp.connection-max-ttl`
  - Env: `MCP_CONNECTION_MAX_TTL`
  - Default: `10m`
- **Max concurrent requests**
  - Flag: `--mcp.max-concurrent-requests`
  - Env: `MCP_MAX_CONCURRENT_REQUESTS`
  - Default: `10`
- **HTTP proxy default (optional)**
  - Flag: `--mcp.http-proxy=<url|env:VAR>`

**OAuth-protected MCP notes:**
- Forwards inbound `Authorization` headers and discovery GETs through the tunnel-client; discovery payload `resource` and `WWW-Authenticate resource_metadata` are rewritten to tunnel-service URLs for the same `tunnel_id`.
- The authorization server is not tunneled. If it is only reachable on-prem/behind a firewall and not accessible from the internet or the tunnel-client host, the OAuth flow can fail.

## Channels

`tunnel-client` supports multiple logical channels:

- `main`: required; configured from `--mcp.server-url` or `--mcp.command`.
- `harpoon`: built-in and enabled only when Harpoon has at least one registered target (see Harpoon config below).
- additional channels: configured via channel-qualified `--mcp.server-url` and/or `--mcp.command` entries.

All response payloads posted to `/v1/tunnel/{tunnel_id}/response` include the resolved `channel` value.

## Harpoon MCP (outbound HTTP allowlist)

`harpoon` is an embedded MCP server that exposes an allowlisted, buffered HTTP client with labeled targets.

Harpoon’s channel (`harpoon`) is considered enabled only when at least one target is registered. If there are no targets, `harpoon` commands return `unsupported_channel`.

- **Target mappings**
  - Flag (repeatable): `--harpoon.target="label=auth,url=https://auth.example.com,desc=Auth server"`
  - Env: `HARPOON_TARGETS` (semicolon- or newline-delimited list of the same `label=...,url=...,desc=...` entries)
- **Allow plaintext HTTP**
  - Flag: `--harpoon.allow-plaintext-http`
  - Env: `HARPOON_ALLOW_PLAINTEXT_HTTP`
  - Default: `false`
- **Max response bytes**
  - Flag: `--harpoon.max-response-bytes`
  - Env: `HARPOON_MAX_RESPONSE_BYTES`
  - Default: `102400`
  - Note: this is the upper ceiling for per-call overrides.
- **Max redirects**
  - Flag: `--harpoon.max-redirects`
  - Env: `HARPOON_MAX_REDIRECTS`
  - Default: `5`
  - Note: this is the upper ceiling for per-call overrides.
- **HTTP proxy (optional)**
  - Flag: `--harpoon.http-proxy=<url|env:VAR>`
- **Additional transport (optional)**
  - Flag: `--harpoon.additional-transport=http-streamable`
  - Env: `HARPOON_ADDITIONAL_TRANSPORTS` (semicolon- or newline-delimited list)
  - Behavior: exposes the harpoon MCP server over the admin/health HTTP server at `GET/POST /harpoon/mcp` (loopback-only unless `--allow-remote-ui` is set).
- **Capture payloads (debug only)**
  - Flag: `--harpoon.capture-payloads`
  - Env: `HARPOON_CAPTURE_PAYLOADS`
  - Default: `false`
  - Behavior: stores request/response payloads in the Harpoon admin UI call history.
- **Private host auto-registration filters**
  - Flag (repeatable): `--harpoon.hosts-include-suffix`
  - Env: `HARPOON_HOSTS_INCLUDE_SUFFIX` (semicolon- or newline-delimited list)
  - Default: empty
  - Behavior: treat matching host suffixes as private for auto-registration.
- **Private host regex filters**
  - Flag (repeatable): `--harpoon.hosts-include-regex`
  - Env: `HARPOON_HOSTS_INCLUDE_REGEX` (semicolon- or newline-delimited list)
  - Default: empty
  - Behavior: treat matching hostnames as private for auto-registration (case-insensitive).
- **Include loopback hosts**
  - Flag: `--harpoon.hosts-include-loopback`
  - Env: `HARPOON_HOSTS_INCLUDE_LOOPBACK`
  - Default: `true`
- **Include private IPs**
  - Flag: `--harpoon.hosts-include-private`
  - Env: `HARPOON_HOSTS_INCLUDE_PRIVATE`
  - Default: `true`
  - Behavior: includes RFC1918 IPv4 plus IPv6 ULA (fc00::/7).

## Logging

- **Level**
  - Flag: `--log.level` (`debug`, `info`, `warn`)
  - Env: `LOG_LEVEL`
  - Default: `info`
- **Format**
  - Flag: `--log.format` (`struct-text`, `json`)
  - Env: `LOG_FORMAT`
  - Default: unset (uses Go’s default logger behavior)
- **File (optional)**
  - Flag: `--log.file`
  - Env: `LOG_FILE`
  - Default: stdout (when unset)
- **Raw HTTP logging (dangerous)**
  - Flag: `--log.http-raw-unsafe`
  - Env: `LOG_HTTP_RAW_UNSAFE`
  - Default: `false`
  - Warning: may log sensitive headers/bodies; enable only for controlled debugging.

## Health/admin server

- **Listen address**
  - Flag: `--health.listen-addr`
  - Env: `HEALTH_LISTEN_ADDR`
  - Default: `:8080`
- **URL file (optional)**
  - Flag: `--health.url-file`
  - Env: `HEALTH_URL_FILE`
  - Use when binding to a random port (e.g., `:0`) and you need to publish the resolved base URL.

## Embedded web UI

When running `tunnel-client run`, the tunnel client serves a lightweight web UI from the same admin/health server.

- **UI entrypoints**: `GET /` or `GET /ui`
- **Static assets**: `GET /assets/*`
- **Remote access (optional)**
  - By default, UI + log endpoints only respond to loopback clients (127.0.0.1/::1).
  - Flag: `--allow-remote-ui`
  - Env: `ALLOW_REMOTE_UI`
  - Default: `false`
- **Open UI in browser (optional)**
  - Flag: `--open-web-ui`
  - Env: `OPEN_WEB_UI`
  - Default: `false`

## Process utilities

- **PID file (optional)**
  - Flag: `--pid.file`
  - Env: `PID_FILE`

## Admin (tunnel management) flags

Used with `tunnel-client admin tunnels ...`:

- **Admin key**
  - Flag: `--admin-key` (accepts raw value, `env:VAR`, or `file:/path`)
  - Env: `OPENAI_ADMIN_KEY`
  - Required.
- **Org/workspace scope**
  - Flags: `--organization-id`, `--workspace-id` (repeatable); at least one is required for `create`, and duplicates are rejected.
- **Base URL**
  - Flag: `--control-plane.base-url`
  - Env: `CONTROL_PLANE_BASE_URL`
  - Default: `https://api.openai.com`
- **Output**
  - Flag: `--json` (structured output)
- **Delete safety**
  - Flag: `--confirm` (required for `tunnels delete`)

## Example configurations

### Minimal env-var run

```bash
export CONTROL_PLANE_API_KEY="sk-..."
export CONTROL_PLANE_TUNNEL_ID="tunnel_<abc>"
export MCP_SERVER_URL="https://mcp.internal.example.com/mcp"

./bin/tunnel-client run --log.level=info --log.format=struct-text
```

### Stdio MCP command

```bash
export CONTROL_PLANE_API_KEY="sk-..."
export CONTROL_PLANE_TUNNEL_ID="tunnel_<abc>"

./bin/tunnel-client run \
  --mcp.command "python -m my_mcp_server --stdio" \
  --log.level=info \
  --log.format=struct-text
```

### API key via file

```bash
./bin/tunnel-client run \
  --control-plane.tunnel-id=tunnel_<abc> \
  --control-plane.api-key=file:/run/secrets/control-plane-api-key \
  --mcp.server-url=https://mcp.internal.example.com/mcp \
  --log.level=info \
  --log.format=json
```

### Outbound proxy CA bundle

If your outbound proxy presents certificates issued by an internal PKI, add the
proxy root CA bundle (additive to system trust) and keep TLS verification enabled:

```bash
./bin/tunnel-client run \
  --ca-bundle /etc/ssl/proxy-root.pem \
  --control-plane.tunnel-id "tunnel_<abc>" \
  --mcp.server-url "https://mcp.internal.example.com/mcp"
```

### Outbound proxy configuration

```bash
./bin/tunnel-client run \
  --http-proxy "http://proxy.internal:8080" \
  --control-plane.http-proxy "env:CONTROL_PROXY_URL" \
  --mcp.server-url "channel=main,url=https://mcp.internal.example.com/mcp,http-proxy=http://mcp-proxy.internal:8080" \
  --harpoon.http-proxy "http://harpoon-proxy.internal:8080" \
  --control-plane.tunnel-id "tunnel_<abc>" \
  --control-plane.api-key "env:CONTROL_PLANE_API_KEY"
```

### Multi-channel MCP bindings

```bash
export CONTROL_PLANE_API_KEY="sk-..."
export CONTROL_PLANE_TUNNEL_ID="tunnel_<abc>"

./bin/tunnel-client run \
  --mcp.server-url="channel=main,url=https://mcp.internal.example.com/mcp" \
  --mcp.server-url="channel=analytics,url=https://analytics.internal.example.com/mcp" \
  --mcp.command="channel=tools,command=npx -y @org/tools-mcp" \
  --log.level=info \
  --log.format=struct-text
```
