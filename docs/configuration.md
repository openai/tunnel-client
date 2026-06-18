# Configuration Reference

`tunnel-client` can be configured via CLI flags, environment variables, a YAML
config file, or a named YAML profile.

- **Precedence**: flags > environment variables > YAML config > defaults.
- **Requirement**: you must provide a control-plane API key, a tunnel ID, and a
  `main` MCP channel binding (via `--mcp.server-url` or `--mcp.command`).

## Agent-first commands

Use the CLI itself as the first discovery surface:

- `tunnel-client help quickstart`
- `tunnel-client help samples`
- `tunnel-client help doctor`
- `tunnel-client help oauth`
- `tunnel-client help plugin`

Use the first-run helpers before editing YAML by hand:

- `health_url_file="$(mktemp "${TMPDIR:-/tmp}/tunnel-client-health.XXXXXX.url")" && tunnel-client run --embedded-mcp-stub --control-plane.tunnel-id <tunnel_id> --health.listen-addr 127.0.0.1:0 --health.url-file "$health_url_file"`
- `tunnel-client init --sample <sample> --profile <name> --tunnel-id <tunnel_id> --mcp-server-url <url>`
- `tunnel-client doctor --profile <name>`
- `tunnel-client doctor --profile <name> --explain`
- `tunnel-client profiles samples list`
- `tunnel-client profiles samples show sample_mcp_with_dcr`
- `tunnel-client dev mcp-stub`
- `tunnel-client codex assistant "Summarize what tunnel-client is for."`
- `tunnel-client codex status`
- `tunnel-client runtimes list`
- `tunnel-client runtimes status <alias>`
- `tunnel-client admin-profiles list`
- `tunnel-client codex plugin install`
- `tunnel-client codex plugin uninstall`

Keep the key split straight during first use:

- `CONTROL_PLANE_TUNNEL_ID`: create or inspect it in Tunnels management, or via
  `tunnel-client admin tunnels create|list|get ...` with `OPENAI_ADMIN_KEY`.
- `CONTROL_PLANE_API_KEY`: create it in Runtime API keys; this is the key used
  by `tunnel-client doctor` and `tunnel-client run`.
- `OPENAI_ADMIN_KEY`: only for `tunnel-client admin tunnels
  list|create|update|delete`. Do not use the admin key for the long-lived
  daemon.

Tunnel permission split:

- Runtime daemon and ChatGPT connector users need Tunnels **Read** + **Use**.
- Tunnel CRUD operators need Tunnels **Read** + **Manage**.
- Admin-key creators need Platform admin-key permission separately.

See [`permissions.md`](permissions.md) before creating roles, groups, or keys.

`run --help` also advertises the config precedence, the sample-discovery path,
and the embedded UI convention `http://<health.listen-addr>/ui`.

Starter prompts for Codex:

- `Figure out what tunnel-client is for from the binary help, then get me to /ui with the shortest local path.`
- `Use tunnel-client to create or reuse a profile, run doctor --explain, and then start the daemon.`
- `Install the Codex plugin from the tunnel-client binary, connect the provided tunnel id, and tell me whether the runtime is launched, healthy, or ready.`
- `Use tunnel-client runtimes to attach a local MCP server to an existing tunnel id and report the ui_url.`

## YAML config file

Pass a config file with `--config /path/to/tunnel-client.yaml` or set
`TUNNEL_CLIENT_CONFIG=/path/to/tunnel-client.yaml`.

Named profiles use the same YAML schema. Run a profile with:

```bash
tunnel-client run --profile sample_mcp_with_dcr
```

Or point `run` at one checked-in or ad hoc profile file directly:

```bash
tunnel-client run --profile-file ./fixtures/sample_mcp_with_dcr.yaml
```

Profile lookup uses this precedence:

1. `--profile-dir /path/to/profiles`
2. `TUNNEL_CLIENT_PROFILE_DIR=/path/to/profiles`
3. `$XDG_CONFIG_HOME/tunnel-client`
4. `~/.config/tunnel-client`

For example, with the default XDG fallback, the command above loads:

```text
~/.config/tunnel-client/sample_mcp_with_dcr.yaml
```

`TUNNEL_CLIENT_PROFILE=sample_mcp_with_dcr` is equivalent to passing
`--profile sample_mcp_with_dcr`. `TUNNEL_CLIENT_PROFILE_FILE` is equivalent to
passing `--profile-file /path/to/profile.yaml`. `--config`, `--profile`, and
`--profile-file` are mutually exclusive, and `TUNNEL_CLIENT_CONFIG`,
`TUNNEL_CLIENT_PROFILE`, and `TUNNEL_CLIENT_PROFILE_FILE` are mutually
exclusive.

Example:

```yaml
config_version: 1
control_plane:
  base_url: https://api.openai.com # citadel-ignore: public endpoint example for external tunnel-client config
  # Optional path appended before tunnel-client adds its /v1/... routes.
  url_path: /chatgpttunnelgateway/dev/us
  tunnel_id: tunnel_0123456789abcdef0123456789abcdef
  api_key: env:CONTROL_PLANE_API_KEY
  # Optional. When configured with the default api.openai.com base URL,
  # tunnel-client automatically uses https://mtls.api.openai.com.
  client_cert: file:/run/secrets/control-plane-client.crt
  client_key: file:/run/secrets/control-plane-client.key
  max_inflight_requests: 20
  poll_timeout: 30000ms
  poll_deadline_guardrail: 5000ms
  extra_headers:
    X-Debug-Mode: "1"
    X-Internal-Auth: env:CONTROL_PLANE_HEADER_VALUE
log:
  level: info
  format: json
  file: /var/log/tunnel-client/tunnel-client.ndjson
health:
  listen_addr: 127.0.0.1:8080
  url_file: /run/tunnel-client/health-url
admin_ui:
  open_browser: false
  log_buffer_events: 2000
process:
  pid_file: /run/tunnel-client/tunnel-client.pid
mcp:
  server_urls:
    - channel: main
      url: https://mcp.example.com/mcp
      # Optional. Dial the logical HTTP URL over a local Unix socket instead of TCP.
      unix_socket: env:MCP_UNIX_SOCKET_PATH
  commands:
    - channel: tools
      command: python -m tools_mcp
  extra_headers:
    X-Internal-Auth: env:MCP_RUNTIME_HEADER_VALUE
  discovery_extra_headers:
    X-Discovery-Auth: file:/run/secrets/mcp-discovery-header
  connection_max_ttl: 10m
  max_concurrent_requests: 10
harpoon:
  targets:
    - label: auth
      url: https://auth.example.com
      description: Auth server
  additional_transports:
    - http-streamable
proxy:
  check_interval: 60s
```

Secret-bearing fields should use `env:VARNAME` or `file:/path/to/secret` when
possible. `control_plane.api_key` accepts either form and resolves it at
startup; direct literal values are accepted for compatibility but are not
recommended for checked-in configs.
For static header values, use `env:VARNAME` or `file:/path/to/secret` on the
value side to keep secrets out of argv, profiles, and checked-in YAML. This is
supported for control-plane, MCP runtime, and MCP discovery/probe extra
headers. The `env:` and `file:` prefixes are reserved for these references;
all other values are treated literally.

The admin UI log export includes `tunnel-client.runtime.yaml`, a redacted
snapshot of argv, relevant environment variables, the startup YAML config file
under `actual_config.contents` when present, and the effective startup config.
API keys, bearer tokens, cookies, shard tokens, URL credentials, and URL query
secrets are redacted before export.

## Commands

- `init`: create a validated first-use profile and print the exact next commands.
- `doctor`: validate the selected config or profile before daemon startup.
- `help <topic>`: show embedded operator guidance for `quickstart`, `samples`,
  `doctor`, `oauth`, or `plugin`.
- `run`: start the tunnel client poll loop.
- `profiles list`: list profile YAML files in the selected profile directory.
- `profiles samples list`: enumerate built-in sample profiles.
- `profiles samples show <name>`: print the sample plus required inputs and
  caveats.
- `profiles add <name>`: create a profile from `--from-file` or a built-in
  sample such as `--sample sample_mcp_with_dcr`.
- `profiles edit <name>`: open a profile in `$VISUAL` or `$EDITOR`, validate it,
  and only save it when the edited YAML parses.
- `codex assistant [prompt...]`: run a terminal assistant session through the
  supervised `codex app-server`; prompt args give one-shot mode and TTY stdin
  enters REPL mode. The default reasoning effort is `medium`, and the REPL
  supports `/model` to inspect or change model/reasoning without restarting.
- `codex status`: report Codex CLI/app-server discovery, login state, and
  plugin wiring.
- `codex install|upgrade|uninstall`: print the official Codex CLI package
  manager commands for this host.
- `codex plugin install`: install the embedded Tunnel MCP plugin bundle into
  `CODEX_HOME`.
- `codex plugin uninstall`: remove the embedded Tunnel MCP plugin bundle from
  `CODEX_HOME` and clean up its enablement section from `config.toml`.
- `codex plugin export --dir <path>`: export the embedded plugin bundle for
  inspection or manual installation.
- `admin-profiles list|set|delete`: manage saved admin-key profiles used by
  native runtime workflows.
- `runtimes create|connect|list|status|stop|rm`: manage native alias state and
  local tunnel-client runtime supervision.
- `admin tunnels`: manage tunnel metadata via the admin API (`/v1/tunnels*`).
- `admin tunnels get <id>`: read-only tunnel metadata lookup; accepts the
  runtime key or an admin key.
- `admin tunnels list|create|update|delete`: admin CRUD; requires an admin key
  and explicit org/workspace/tenant scope flags. After `create` succeeds, wait
  25-30 seconds before expecting the tunnel to be active and ready.
- `tunnel-client` with no subcommand prints help and available commands.

## Built-in profile samples

Built-in samples are stored as separate embedded files and validated in tests.
The starter sample set is:

- `sample_mcp_with_dcr`: general-purpose HTTP or stdio MCP target with the full
  OAuth/DCR-friendly contract and `channel=main` already wired.
- `sample_mcp_stdio_local`: shortest path for a local stdio MCP command.
- `sample_mcp_remote_no_auth`: remote HTTP MCP server that does not advertise
  OAuth/PRMD metadata.
- `sample_mcp_enterprise_proxy`: HTTP or stdio MCP target for outbound proxies
  or private PKI, with `http_proxy: env:HTTPS_PROXY`,
  `ca_bundle: env:ENTERPRISE_CA_BUNDLE`, and sample comments that separate the
  runtime key from the admin key.

Use the sample surfaces instead of guessing sample names:

```bash
tunnel-client profiles samples list
tunnel-client profiles samples show sample_mcp_with_dcr
tunnel-client profiles add my-profile --sample sample_mcp_with_dcr --tunnel-id tunnel_0123456789abcdef0123456789abcdef --mcp-server-url http://127.0.0.1:3001/mcp
tunnel-client profiles add corp-proxy --sample sample_mcp_enterprise_proxy --tunnel-id tunnel_0123456789abcdef0123456789abcdef --mcp-server-url https://mcp.internal.example.com/mcp
```

## Control plane

- **Base URL**
  - Flag: `--control-plane.base-url`
  - Env: `CONTROL_PLANE_BASE_URL`
  - Default: `https://api.openai.com`
  - With control-plane mTLS configured and this value left at the default API
    host, `tunnel-client` automatically uses `https://mtls.api.openai.com`.
    Set a non-default base URL explicitly for staging, development, or private
    control-plane hosts.
  - **Important**: this value is treated as the **host root**, not a pre-prefixed path.
    - Correct: `https://api.openai.com`
    - Incorrect: `https://api.openai.com/v1/tunnel`
- **URL path**
  - Flag: `--control-plane.url-path`
  - Env: `CONTROL_PLANE_URL_PATH`
  - YAML: `control_plane.url_path`
  - Optional. Set this when an enterprise gateway needs a workspace or environment path appended to the base URL before tunnel-client adds `/v1/...` routes.
  - The same path is honored by `tunnel-client admin tunnels ...` via `--control-plane.url-path`, and by native `runtimes` / tunnel-mcp flows via `--control-plane-url-path` or `control_plane_url_path`.
    - Example base URL: `https://gateway.example.com`
    - Example URL path: `/workspace/dev/us`
    - Effective poll URL: `https://gateway.example.com/workspace/dev/us/v1/tunnels/<tunnel_id>/poll`
- **Tunnel ID**
  - Flag: `--control-plane.tunnel-id`
  - Env: `CONTROL_PLANE_TUNNEL_ID`
  - Required: yes
  - Format: `tunnel_` followed by 32 lowercase hexadecimal characters (for example `tunnel_0123456789abcdef0123456789abcdef`)
- **API key**
  - Flag: `--control-plane.api-key=env:VARNAME` or `--control-plane.api-key=file:/path/to/secret`
  - Env (preferred): `CONTROL_PLANE_API_KEY`
  - Env (fallback): `OPENAI_API_KEY` (used only if `CONTROL_PLANE_API_KEY` is unset)
  - Required: yes
- **Client certificate for mTLS (optional)**
  - Flags: `--control-plane.client-cert=/path/to/client.crt` and `--control-plane.client-key=/path/to/client.key`
  - Env: `CONTROL_PLANE_CLIENT_CERT` and `CONTROL_PLANE_CLIENT_KEY`
  - YAML: `control_plane.client_cert` and `control_plane.client_key`
  - Values may be plain paths, `env:VARNAME` path references, or
    `file:/path/to/pem` file references.
  - Configure both fields together. Cert-only, key-only, unreadable files,
    invalid PEM, and mismatched key/cert pairs fail startup and `doctor`.
  - The runtime API key is still required; mTLS only adds TLS client-certificate
    presentation to the control-plane HTTPS connection.
- **HTTP proxy (optional)**
  - Flag: `--control-plane.http-proxy=<url|env:VAR>`
  - Env: `CONTROL_PLANE_HTTP_PROXY`
- **Poll timeout**
  - Flag: `--control-plane.poll-timeout`
  - Env: `CONTROL_PLANE_POLL_TIMEOUT`
  - Default: `30000ms`
  - Behavior: tunnel-client sends this as the requested `/poll?timeout_ms=...`
    empty-poll wait budget. Together with `poll_deadline_guardrail`, the client
    poll HTTP/context deadline must stay at or below `600000ms`.
- **Poll deadline guardrail**
  - Flag: `--control-plane.poll-deadline-guardrail`
  - Env: `CONTROL_PLANE_POLL_DEADLINE_GUARDRAIL`
  - Default: `5000ms`
  - Max: less than `60000ms`
  - Behavior: tunnel-client adds this after the requested service wait when
    setting the HTTP/context deadline so a normal `204 No Content` empty poll
    can complete without being classified as a client timeout. Test profiles
    can override it with a smaller millisecond duration such as `500ms`.
- **Polled-command buffer capacity**
  - Flag: `--control-plane.max-inflight`
  - Env: `CONTROL_PLANE_MAX_INFLIGHT_REQUESTS`
  - Default: `20` (max `10000`)
  - This is the number of prefetched commands that can wait in the local queue;
    it does not include requests already dispatched to the MCP server.
  - Each control-plane poll requests the smaller of the local buffer's free
    capacity and `25`, matching the tunnel-service API contract. When the
    buffer is full, tunnel-client skips polling until a queue slot is free.
- **Extra headers (optional)**
  - Flag (repeatable): `--control-plane.extra-headers "Key: Value"`
  - Env: `CONTROL_PLANE_EXTRA_HEADERS="Key: Value, Key2: Value2"`
  - YAML: `control_plane.extra_headers`
  - Header values accept `env:VARNAME` and `file:/path/to/secret`; all other
    values are treated literally.
  - Reserved control-plane headers (`Authorization`, `Accept`, `User-Agent`,
    `X-Tunnel-Client-Name`, and `X-Tunnel-Client-Version`) are managed by the
    client and cannot be overridden by extra headers.

## TLS trust (custom CA bundle)

Use a PEM CA bundle to extend (additive to system trust) the trust store for
**all** outbound TLS connections (control plane, MCP HTTP, OAuth discovery, and
Harpoon).

- **CA bundle**
  - Flag: `--ca-bundle /path/to/ca-bundle.pem`
  - Env: `CA_BUNDLE`
  - Bundle format: PEM file containing one or more CA certificates.

## Outbound HTTP proxy

Use explicit proxy flags to force tunnel-client traffic through a corporate
proxy. Each flag and matching tunnel-client-specific proxy env var accepts a
proxy URL or `env:VAR` reference.

If you want a ready-made profile instead of wiring the YAML by hand, start from
`sample_mcp_enterprise_proxy` and export `HTTPS_PROXY` plus
`ENTERPRISE_CA_BUNDLE` before `tunnel-client doctor` or `run`.

- **Global proxy (all outbound HTTP)**
  - Flag: `--http-proxy=<url|env:VAR>`
  - Env: `TUNNEL_CLIENT_HTTP_PROXY`
  - Applies to control plane, MCP HTTP, OAuth discovery, and Harpoon unless overridden.
- **Control plane proxy**
  - Flag: `--control-plane.http-proxy=<url|env:VAR>`
  - Env: `CONTROL_PLANE_HTTP_PROXY`
- **MCP proxy default**
  - Flag: `--mcp.http-proxy=<url|env:VAR>`
  - Env: `MCP_HTTP_PROXY`
  - Per-channel override: `--mcp.server-url="channel=...,url=...,http-proxy=<url|env:VAR>"`
  - Note: stdio MCP bindings ignore proxy settings.
- **Harpoon proxy**
  - Flag: `--harpoon.http-proxy=<url|env:VAR>`
  - Env: `HARPOON_HTTP_PROXY`

- **Proxy health checks**
  - Flag: `--proxy.check-interval=60s`
  - Env: `PROXY_CHECK_INTERVAL`
  - Default: `60s`

**Precedence (highest to lowest):**
1. Per-target/per-channel proxy flag.
2. MCP default proxy (`--mcp.http-proxy`).
3. Global proxy (`--http-proxy`).
4. Environment (`HTTP_PROXY` / `HTTPS_PROXY` / `NO_PROXY`).

When an explicit proxy flag is set for a target, environment proxy variables
(including `NO_PROXY`) are ignored for that target.

## Connector and MCP routing

For a contributor-focused walkthrough of connector request flow, channel
routing, streaming, OAuth discovery, and common setup pitfalls, see
[`connectors.md`](connectors.md).

## MCP server

- **Server URL**
  - Flag (repeatable): `--mcp.server-url`
  - Env: `MCP_SERVER_URL`
  - Required: yes for the `main` channel (unless `--mcp.command` supplies `main`)
  - Legacy form: `--mcp.server-url=https://main.example.com/mcp` (defaults to `main`)
  - Channel-qualified form:
    `--mcp.server-url="channel=foo,url=https://foo.example.com/mcp,unix-socket=<path|env:VAR>,http-proxy=<url|env:VAR>,client-cert=<path|env:VAR>,client-key=<path|env:VAR>"`
  - Unix socket dial (optional): set `unix-socket=<path|env:VAR>` on a
    channel-qualified entry, or `unix_socket:` in YAML, to dial the logical
    HTTP(S) MCP URL over a local Unix-domain socket instead of TCP.
  - Note: per-channel `unix-socket` cannot be combined with per-channel
    `http-proxy`; MCP/global proxy defaults are ignored for that binding.
- **Command (stdio transport)**
  - Flag (repeatable): `--mcp.command`
  - Env: `MCP_COMMAND`
  - Required: yes for the `main` channel (unless `--mcp.server-url` supplies `main`)
  - Legacy form: `--mcp.command="npx -y @org/main-mcp"` (defaults to `main`)
  - Channel-qualified form: `--mcp.command="channel=bar,command=npx -y @org/bar-mcp"`
  - Behavior: spawns the command once and uses the child process stdin/stdout for MCP frames
  - Note: stdio transport does not support MCP sessions
  - Note: when using `MCP_COMMAND` with multiple entries, separate entries with
    newlines so semicolons remain part of the command.
- **Multiple entries**
  - Flags are repeatable; each entry can target a different channel.
  - Environment variables accept newline-delimited entries.
  - Configuring both `--mcp.server-url` and `--mcp.command` is allowed as long
    as they target **different** channels.
  - If no `main` binding is configured, startup fails with `main channel is required`.
- **Connection max TTL**
  - Flag: `--mcp.connection-max-ttl`
  - Env: `MCP_CONNECTION_MAX_TTL`
  - Default: `10m`
- **Max concurrent requests**
  - Flag: `--mcp.max-concurrent-requests`
  - Env: `MCP_MAX_CONCURRENT_REQUESTS`
  - Default: `10`
  - This caps requests actively dispatched to the MCP server. When all worker
    slots are occupied, the dispatcher removes one command from the local queue
    and waits for a worker slot. It does not drain another command until a slot
    is free.
  - This limit is independent of `--control-plane.max-inflight`. With the
    defaults, tunnel-client can hold up to `10` active MCP requests, `20`
    commands in the local queue, and one dispatcher-held command waiting for a
    worker slot.
- **HTTP proxy default (optional)**
  - Flag: `--mcp.http-proxy=<url|env:VAR>`
  - Env: `MCP_HTTP_PROXY`
- **mTLS client certificate default (optional)**
  - Flag: `--mcp.client-cert=<path|env:VAR>`
  - Env: `MCP_CLIENT_CERT`
- **mTLS client private key default (optional)**
  - Flag: `--mcp.client-key=<path|env:VAR>`
  - Env: `MCP_CLIENT_KEY`
  - Behavior: both values are required together.
  - Scope: applies to all `http-streamable` MCP channels unless a
    channel-qualified `--mcp.server-url` entry provides its own `client-cert` +
    `client-key`.
  - Note: stdio channels ignore mTLS settings.
- **Static MCP headers (optional)**
  - Flag (repeatable): `--mcp.extra-headers "Key: Value"`
  - Env: `MCP_EXTRA_HEADERS="Key: Value, Key2: Value2"`
  - YAML: `mcp.extra_headers`
  - Header values accept `env:VARNAME` and `file:/path/to/secret`; all other
    values are treated literally.
  - Scope: sent only to the configured MCP server origin for outbound MCP HTTP
    traffic. These headers are not sent to the OpenAI control plane or
    unrelated authorization-server hosts.
  - Conflict behavior: connector-forwarded request headers are applied last and
    override these static values case-insensitively.
- **Static discovery/probe headers (optional)**
  - Flag (repeatable): `--mcp.discovery-extra-headers "Key: Value"`
  - Env: `MCP_DISCOVERY_EXTRA_HEADERS="Key: Value, Key2: Value2"`
  - YAML: `mcp.discovery_extra_headers`
  - Header values accept `env:VARNAME` and `file:/path/to/secret`; all other
    values are treated literally.
  - Scope: sent only to MCP discovery/probe requests for the configured MCP
    server origin, including OAuth Protected Resource Metadata discovery,
    WWW-Authenticate probing, and the startup MCP initialize probe.
  - Conflict behavior: these discovery/probe headers override
    `MCP_EXTRA_HEADERS` for discovery/probe requests. If connector-forwarded
    headers are present on a request, they are still applied last.

**OAuth-protected MCP notes:**

- Forwards inbound `Authorization` headers and discovery GETs through the
  tunnel client. Discovery payload `resource` values and
  `WWW-Authenticate resource_metadata` values are rewritten to tunnel-service
  URLs for the same `tunnel_id`.
- Uses `authorization_servers[0]` from PRMD as the source of truth and metadata
  fetch target for auth-server metadata enrichment and Harpoon OAuth target
  registration.
- Accepts auth-server metadata even when metadata `issuer` differs from
  `authorization_servers[0]` (external IdP issuers are supported). Mismatch
  details are preserved in diagnostics and logs.
- The authorization server is not tunneled. If it is only reachable on-premises
  or behind a firewall, and not accessible from the internet or the
  tunnel-client host, the OAuth flow can fail.

## Channels

`tunnel-client` supports multiple logical channels:

- `main`: required; configured from `--mcp.server-url` or `--mcp.command`.
- `harpoon`: built-in and enabled only when Harpoon has at least one registered
  target (see Harpoon config below).
- additional channels: configured via channel-qualified `--mcp.server-url`
  and/or `--mcp.command` entries.

All response payloads posted to `/v1/tunnels/{tunnel_id}/response` include the
resolved `channel` value.

## Harpoon MCP (outbound HTTP allowlist)

`harpoon` is an embedded MCP server that exposes an allowlisted, buffered HTTP
client with labeled targets.

Harpoon's channel (`harpoon`) is enabled only when at least one target is
registered. If there are no targets, `harpoon` commands return
`unsupported_channel`.

- **Target mappings**
  - Flag (repeatable): `--harpoon.target="label=auth,url=https://auth.example.com,desc=Auth server"`
  - Env: `HARPOON_TARGETS` (semicolon- or newline-delimited list of the same
    `label=...,url=...,desc=...` entries)
- **Harpoon target metadata (`list_targets`)**
  - Each target includes `category`, `source`, and `tags` fields.
  - Config-provided targets default to `category=source=config`.
  - OAuth auto-registered targets derive `category`/`source` from discovery
    tags (currently `oauth`) and derive `tags` from the OAuth role (for example,
    `auth-server-metadata`, `registration-endpoint`, or
    `protected-resource-metadata`).
  - The `list_targets` tool accepts optional filters:
    - `categories`: OR match within categories.
    - `sources`: OR match within sources.
    - `tags`: ALL requested tags must be present on the target.
    - Filters combine with AND across fields.
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
  - Env: `HARPOON_HTTP_PROXY`
- **Additional transport (optional)**
  - Flag: `--harpoon.additional-transport=http-streamable`
  - Env: `HARPOON_ADDITIONAL_TRANSPORTS` (semicolon- or newline-delimited list)
  - Behavior: exposes the Harpoon MCP server over the admin/health HTTP server
    at `GET/POST /harpoon/mcp` (loopback-only unless `--allow-remote-ui` is
    set).
- **Capture payloads (debug only)**
  - Flag: `--harpoon.capture-payloads`
  - Env: `HARPOON_CAPTURE_PAYLOADS`
  - Default: `false`
  - Behavior: stores request/response payloads in the Harpoon admin UI call
    history.
- **Private host auto-registration filters**
  - Flag (repeatable): `--harpoon.hosts-include-suffix`
  - Env: `HARPOON_HOSTS_INCLUDE_SUFFIX` (semicolon- or newline-delimited list)
  - Default: empty
  - Behavior: treat matching host suffixes as private for auto-registration.
- **Private host regex filters**
  - Flag (repeatable): `--harpoon.hosts-include-regex`
  - Env: `HARPOON_HOSTS_INCLUDE_REGEX` (semicolon- or newline-delimited list)
  - Default: empty
  - Behavior: treat matching hostnames as private for auto-registration
    (case-insensitive).
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
  - Default: unset (uses Go's default logger behavior)
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
  - Default: `127.0.0.1:8080`
  - The default keeps `/healthz`, `/readyz`, `/metrics`, and the embedded UI on loopback.
  - Set `HEALTH_LISTEN_ADDR=:8080` only when a container orchestrator, sidecar, or trusted operator network must reach the health endpoints remotely.
  - Set the port to `0` only when you explicitly want the OS to assign an ephemeral port at startup.
- **URL file (optional)**
  - Flag: `--health.url-file`
  - Env: `HEALTH_URL_FILE`
  - Recommended with `HEALTH_LISTEN_ADDR=:0` when another process needs the resolved `/healthz`, `/readyz`, or `/ui` base URL.
  - Use a private per-run path such as `health_url_file="$(mktemp "${TMPDIR:-/tmp}/tunnel-client-health.XXXXXX.url")"` instead of a fixed shared `/tmp` filename.

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
- **Runtime log level toggle**
  - The Logs tab can change the live runtime log level between `debug`, `info`,
    and `warn` through `GET`/`PUT /api/log-level`.
  - Use this for short troubleshooting windows without restarting the client.

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
  - Flags: `--organization-id`, `--workspace-id` (repeatable). At least one is
    required for `create`, and duplicates are rejected.
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
export CONTROL_PLANE_TUNNEL_ID="tunnel_0123456789abcdef0123456789abcdef"
export MCP_SERVER_URL="https://mcp.internal.example.com/mcp"

./bin/tunnel-client run --log.level=info --log.format=struct-text
```

### Stdio MCP command

```bash
export CONTROL_PLANE_API_KEY="sk-..."
export CONTROL_PLANE_TUNNEL_ID="tunnel_0123456789abcdef0123456789abcdef"

./bin/tunnel-client run \
  --mcp.command "python -m my_mcp_server --stdio" \
  --log.level=info \
  --log.format=struct-text
```

### API key via file

```bash
./bin/tunnel-client run \
  --control-plane.tunnel-id=tunnel_0123456789abcdef0123456789abcdef \
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
  --control-plane.tunnel-id "tunnel_0123456789abcdef0123456789abcdef" \
  --mcp.server-url "https://mcp.internal.example.com/mcp"
```

### Outbound proxy configuration

```bash
./bin/tunnel-client run \
  --http-proxy "http://proxy.internal:8080" \
  --control-plane.http-proxy "env:CONTROL_PROXY_URL" \
  --mcp.server-url "channel=main,url=https://mcp.internal.example.com/mcp,http-proxy=http://mcp-proxy.internal:8080" \
  --harpoon.http-proxy "http://harpoon-proxy.internal:8080" \
  --control-plane.tunnel-id "tunnel_0123456789abcdef0123456789abcdef" \
  --control-plane.api-key "env:CONTROL_PLANE_API_KEY"
```

### MCP mTLS configuration

```bash
./bin/tunnel-client run \
  --control-plane.tunnel-id "tunnel_0123456789abcdef0123456789abcdef" \
  --control-plane.api-key "env:CONTROL_PLANE_API_KEY" \
  --mcp.client-cert "/etc/tunnel-client/mtls/default-client.crt" \
  --mcp.client-key "/etc/tunnel-client/mtls/default-client.key" \
  --mcp.server-url "channel=main,url=https://mcp.internal.example.com/mcp" \
  --mcp.server-url "channel=analytics,url=https://analytics.internal.example.com/mcp,client-cert=/etc/tunnel-client/mtls/analytics-client.crt,client-key=/etc/tunnel-client/mtls/analytics-client.key"
```

### Multi-channel MCP bindings

```bash
export CONTROL_PLANE_API_KEY="sk-..."
export CONTROL_PLANE_TUNNEL_ID="tunnel_0123456789abcdef0123456789abcdef"

./bin/tunnel-client run \
  --mcp.server-url="channel=main,url=https://mcp.internal.example.com/mcp" \
  --mcp.server-url="channel=analytics,url=https://analytics.internal.example.com/mcp" \
  --mcp.command="channel=tools,command=npx -y @org/tools-mcp" \
  --log.level=info \
  --log.format=struct-text
```
