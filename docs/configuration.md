# Configuration Reference

`tunnel-client` can be configured via CLI flags, environment variables, a YAML
config file, or a named YAML profile.

- **Precedence**: flags > environment variables > YAML config > defaults.
- **Requirement**: you must provide a control-plane API key, a tunnel ID, and a
  `main` MCP channel binding (via `--mcp.server-url`, `--mcp.command`, or one of
  the [embedded demo modes](#embedded-demo-mcp-modes)).

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

## Embedded demo MCP modes

Both optional `run` flags start the same demo MCP tools (`server_info`, `echo`,
and `uppercase`) and OAuth metadata endpoints inside the tunnel-client process,
then bind its `main` MCP channel to that process's stub:

| Flag | MCP compatibility behavior |
| --- | --- |
| `--embedded-mcp-stub` | Preserves stateful handling for legacy initialization and session requests, including `initialize` and `notifications/initialized`. Self-contained modern discovery and tool requests use stateless handling. |
| `--embedded-stateless-mcp-stub` | Uses stateless handling for every MCP request, including `initialize` and `notifications/initialized`, without issuing `Mcp-Session-Id`. |

For example, after replacing the runtime key and tunnel ID:

```bash
export CONTROL_PLANE_API_KEY="sk-..."
export CONTROL_PLANE_TUNNEL_ID="tunnel_0123456789abcdef0123456789abcdef"
tunnel-client run \
  --embedded-stateless-mcp-stub \
  --embedded-mcp-listen-addr 127.0.0.1:0 \
  --embedded-mcp-server-name stateless-demo \
  --embedded-mcp-server-version 1.0.0 \
  --health.listen-addr 127.0.0.1:0
```

The modes are mutually exclusive. Neither can be combined with an explicit
`--mcp.server-url` or `--mcp.command` flag, including the aliases
`--mcp-server-url` and `--mcp-command`. Mode selection and embedded options are
CLI flags for `run`; they have no environment-variable or YAML equivalents.
Without either mode, the configured MCP target and existing defaults apply.

`--embedded-stateless-mcp-stub` replaces a `main` target resolved from the
environment, YAML config, or profile, and adds one if none is configured. It
retains other MCP channels. Its main binding does not inherit configured MCP or
global HTTP proxies. Replacing the target does not bypass validation of
configured targets or duplicate-channel checks. This behavior applies only to
the stateless flag; `--embedded-mcp-stub` keeps its existing configuration
behavior.

The stateless mode advertises `main` as stateless in v2 server-info metadata.
Other configured channels retain their own affinity requirements. The compatible
embedded mode retains its existing declaration.

Shared options:

| Option | Default | Meaning |
| --- | --- | --- |
| `--embedded-mcp-listen-addr` | `127.0.0.1:0` | Stub listen address; port `0` chooses an available port for each process. |
| `--embedded-mcp-unix-socket` | Empty | Bind the stub to a Unix socket instead of TCP; cannot be combined with an explicit `--embedded-mcp-listen-addr`. |
| `--embedded-mcp-server-name` | `mcp-stub` | Server name advertised by the demo. |
| `--embedded-mcp-server-version` | `0.1.0` | Server version advertised by the demo. |

For local socket-only MCP, add `--embedded-mcp-unix-socket /tmp/my-demo-mcp.sock`
to either mode. The parent directory must exist and the socket path must be
unused. The main channel and its OAuth discovery use the socket; the displayed
`http://localhost/mcp` URL is its logical HTTP address. The listener removes its
socket on shutdown. Combine this with `--health.unix-socket` to serve health and
admin endpoints over a separate socket. Existing TCP defaults remain available.

The stateless demo tools need no MCP session affinity: completed requests in
one interaction can be followed by requests to another process's embedded
stub. Clients do not need a session DELETE on exit; the stateless endpoint
returns HTTP 405 for session DELETE and standalone GET streams. This does not
provide replay of interrupted requests, shared OAuth state, or shared
application state. Authentication and any state used by other applications
must be handled separately.

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

The selected profile directory may itself be a symlink; named profile symlinks must use relative targets within that directory, while `--profile-file` and `--from-file` accept explicit file paths.

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
# Optional. Forward the original HTTP X-Request-Id to MCP HTTP targets (default).
# Use [mcp, harpoon, oauth] to opt in Harpoon REST and queued OAuth discovery
# at the configured MCP origin, or none to disable automatic forwarding.
forward_request_id: mcp
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
  base_path: ""
  public_origin: ""
  log_buffer_events: 2000
process:
  pid_file: /run/tunnel-client/tunnel-client.pid
cloudflared:
  # Optional. Fetch the managed runtime token from tunnel-service on startup.
  managed: true
  # Optional static override. Use an env: or file: reference; literal tokens are rejected.
  # When present, this takes precedence over managed fetch.
  token: env:CLOUDFLARED_TOKEN
  # Optional source-build/test override. Release archives discover the sibling binary.
  path: /opt/tunnel-client/cloudflared
  ready_timeout: 30s
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
  # Optional. Explicitly trust separate OAuth metadata/authorization origins.
  oauth_trusted_origins:
    - https://auth.example.com
  # Optional. Wait for a sidecar/local HTTP listener before the first poll.
  startup_wait_timeout: 60s
  connection_max_ttl: 10m
  max_concurrent_requests: 10
harpoon:
  # Optional. Authorize discovered OAuth endpoints on a separate HTTPS origin.
  # Discovery still requires mcp.oauth_trusted_origins above.
  oauth_forward_origins:
    - https://auth.example.com
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

`cloudflared.managed` (or `CLOUDFLARED_MANAGED`) asks `tunnel-client run`
to fetch the managed Cloudflare metadata and runtime token from the authenticated
control plane before starting the adjacent bundled `cloudflared`. The fetch
response is never sent through raw HTTP body logging. `cloudflared.token` is
the static override: it accepts only `env:VARNAME` or `file:/path/to/secret`,
and `CLOUDFLARED_TUNNEL_TOKEN` is the direct environment equivalent. A static
token takes precedence over managed fetch. In either mode, tunnel-client passes
the token only through the child environment, waits for its loopback readiness
endpoint, and propagates unexpected process exits.
`cloudflared.path` / `CLOUDFLARED_PATH` is only an advanced source-build or test
override; supported release archives do not require it.

The admin UI log export includes `tunnel-client.runtime.yaml`, a redacted
snapshot of argv, relevant environment variables, the startup YAML config file
under `actual_config.contents` when present, and the effective startup config.
API keys, bearer tokens, cookies, shard tokens, URL credentials, and URL query
secrets are redacted before export.

## AWS workload identity

The tunnel client can use an AWS role to authenticate to the production control plane.
Register the AWS issuer with OpenAI and map the role to a project service account
with tunnel read and use access. Set both IDs on the client:

```sh
CONTROL_PLANE_AWS_IDENTITY_PROVIDER_ID=<provider-id>
CONTROL_PLANE_AWS_SERVICE_ACCOUNT_ID=<service-account-id>
```

Leave static API key settings unset. The AWS SDK uses the configured region and
credential chain, including ECS task credentials. The role needs
`sts:GetWebIdentityToken` for audience `https://api.openai.com/v1` and a
300-second assertion. The client exchanges that assertion at
`https://auth.openai.com/oauth/token`. It keeps the access token in memory
and refreshes it before expiry while serving `https://api.openai.com`.
The same configuration is available under `control_plane.aws_identity_provider_id`
and `control_plane.aws_service_account_id` in YAML.

## Commands

- `init`: create a validated first-use profile and print the exact next commands.
- `doctor`: validate the selected config or profile before daemon startup.
- `help <topic>`: show embedded operator guidance for `quickstart`, `samples`,
  `doctor`, `oauth`, or `plugin`.
- `run`: start the tunnel client poll loop.
- `cloudflared version`: print the bundled companion version, pinned Go module,
  release commit, and security-patch owner.
- `cloudflared config --token-file <path>`: print a token-free production
  `cloudflared` YAML config for operators who run `cloudflared` directly.
- `profiles list`: list profile YAML files in the selected profile directory.
- `profiles samples list`: enumerate built-in sample profiles.
- `profiles samples show <name>`: print the sample plus required inputs and
  caveats.
- `profiles add <name>`: create a profile from `--from-file` or a built-in
  sample such as `--sample sample_mcp_with_dcr`.
- `profiles edit <name>`: open a profile in `$VISUAL` or `$EDITOR`, validate it,
  and only save it when the edited YAML parses. Only [supported editors and
  safe options](profile-editor.md) are accepted; shell wrappers and evaluation
  commands are rejected.
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

### Residency endpoint selection

Select a public control-plane endpoint with `--control-plane.residency`,
`CONTROL_PLANE_RESIDENCY`, or YAML `control_plane.residency`. Precedence remains
flags > environment > YAML/profile > defaults. Values are case-insensitive;
an explicitly empty or unknown value is rejected. Omit the setting to preserve
existing default and custom base-URL behavior. `global` means no geographic
constraint and is separate from `us`.

| Value | HTTPS base URL | mTLS base URL |
| --- | --- | --- |
| `global` | `https://api.openai.com` | `https://mtls.api.openai.com` |
| `us` | `https://us.api.openai.com` | `https://mtls-us.api.openai.com` |
| `eu` | `https://eu.api.openai.com` | `https://mtls-eu.api.openai.com` |
| `au` | `https://au.api.openai.com` | Unconfigured |
| `ca` | `https://ca.api.openai.com` | Unconfigured |
| `gb` | `https://gb.api.openai.com` | Unconfigured |
| `in` | `https://in.api.openai.com` | Unconfigured |
| `jp` | `https://jp.api.openai.com` | Unconfigured |
| `kr` | `https://kr.api.openai.com` | Unconfigured |
| `sg` | `https://sg.api.openai.com` | Unconfigured |
| `ae` | `https://ae.api.openai.com` | Unconfigured |

The catalog contains no regional enable flag. Customers can install this client
before a backend adds support. Selecting an endpoint does not enable regional
Tunnel service, move an existing tunnel, change workspace access, or guarantee
where requests, responses, metadata, logs, or inference are processed or stored.
EU Tunnel residency is not currently available as a supported customer deployment.
Service failures stay on the selected endpoint and retain the usual error and
retry behavior; there is no fallback to global or another residency.

Configure both existing `control_plane.client_cert` and `client_key` settings to
select the same residency's mTLS endpoint. Requesting mTLS where the catalog has
no mTLS endpoint fails configuration validation. MCP certificates remain separate.
Certificate files load at startup; restart after rotation. The runtime API key
remains required. AWS workload identity currently supports only the global
ordinary-TLS endpoint.

When residency is explicit, a supplied `base_url` must match its ordinary TLS
endpoint or selected mTLS endpoint; a matching ordinary endpoint upgrades to
mTLS when certificates are configured. Conflicting origins, credentials, paths,
query strings, fragments, and nonstandard ports are rejected. Control-plane
redirects are not followed. Outbound proxy selection and CA bundles retain their
existing settings; HTTP CONNECT proxies retain TLS to the selected endpoint.

For example, this configuration selects the EU mTLS endpoint once service support
is available; it does not make EU Tunnel service available today:

```yaml
control_plane:
  residency: eu
  tunnel_id: tunnel_0123456789abcdef0123456789abcdef
  api_key: env:CONTROL_PLANE_API_KEY
  client_cert: file:/run/secrets/client.crt
  client_key: file:/run/secrets/client.key
```

The same selector is accepted by `doctor` and `admin tunnels` commands. Native
`runtimes` commands use `--control-plane-residency`; generated runtime profiles
preserve the selection. Tunnel MCP lifecycle tools accept
`control_plane_residency`. The Go SDK accepts `Config.ControlPlaneResidency`
and an optional `Config.ControlPlaneClientCertificate` loaded with Go's
`tls.LoadX509KeyPair`.

For existing native admin profiles, the selector replaces a stored legacy
`https://api.openai.com` default. Explicit base URL overrides and custom profile
endpoints must still match the selected residency.

### Other control-plane settings

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
  - Format: `tunnel_<32 lowercase letters or digits>` or `tunnel_<namespace>_<32 lowercase letters or digits>`, where namespace is exactly four lowercase ASCII letters or digits. Preserve the complete ID returned by the service.
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
  - Behavior: tunnel-client sends this as the usual `/poll?timeout_ms=...`
    empty-poll wait budget. Together with `poll_deadline_guardrail`, the client
    poll HTTP/context deadline must stay at or below `600000ms`.
  - The first poll attempt with a positive command limit also applies the
    initial-poll timeout below. Later attempts use the usual wait, including
    after an initial failure.
  - On HTTP-proxied routes, if a poll loses its connection before response
    headers with an EOF-style error
    while neither deadline has fired, the process automatically learns a
    shorter wait for future proxied polls. The learned value only decreases,
    never below `5000ms`; configured `poll_timeout` remains the ceiling.
    Subsequent direct and Unix-socket polls keep the configured value.
- **Initial poll timeout**
  - Flag: `--control-plane.initial-poll-timeout`
  - Env: `CONTROL_PLANE_INITIAL_POLL_TIMEOUT`
  - Default: `30s`, matching the normal poll default; must be positive.
  - Behavior: the first poll attempt requests the shorter of this value and the
    normal poll wait. Set a lower value for a shorter first wait. With the
    default initial setting, a normal wait above `30s` is capped at `30s` on
    the first attempt. Its full normal client deadline is retained for services
    that clamp or ignore the requested wait.
    Local test services can allow a lower minimum to exercise shorter initial
    waits; the service's own minimum still determines the effective wait.
- **Poll deadline guardrail**
  - Flag: `--control-plane.poll-deadline-guardrail`
  - Env: `CONTROL_PLANE_POLL_DEADLINE_GUARDRAIL`
  - Default: `5000ms`
  - Max: less than `60000ms`
  - Behavior: tunnel-client adds this to the configured or proxy-learned wait
    when setting the HTTP/context deadline, including on the first poll attempt,
    so a normal `204 No Content` empty poll can complete without being classified
    as a client timeout. Test profiles
    can override it with a smaller millisecond duration such as `500ms`.
- **Poll channels (optional)**
  - Flag (repeatable): `--control-plane.poll-channel=main`
  - Env: `CONTROL_PLANE_POLL_CHANNELS=main,harpoon`
  - YAML: `control_plane.poll_channels: [main, harpoon]`
  - Precedence is flags, then environment, then YAML. When omitted, the client
    preserves legacy unscoped polling and sends no `channel` query parameters.
  - Once configured, this is an allowlist: omitted channels are disabled. The
    client rejects blanks, duplicates, non-canonical names, and channels without
    a local handler. Values are sorted before repeated `channel` query
    parameters are serialized.
  - A Harpoon-only client may set only `harpoon` and omit the main MCP binding,
    but it must configure at least one routable Harpoon target.
  - Unscoped polling and scopes that include `harpoon` wait for startup OAuth
    discovery and target registration to complete. A failed startup catalog
    prevents polling, and `/readyz` stays non-ready while registration is
    pending or failed. Explicit scopes that exclude `harpoon` do not wait for
    this catalog. This gate requires the standard runtime's acknowledged
    registration bus; custom embedded graphs using a legacy publication-only
    bus retain their existing behavior.
  - See [Supervising startup](health.md#supervising-startup) for the combined
    listener wait, catalog, readiness, and polling rules.
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
  - Header names must use valid HTTP field-name syntax and are
    case-insensitive. Identical case variants collapse to one header;
    conflicting values and invalid wire values are rejected before startup.
  - Reserved control-plane headers (`Authorization`, `Accept`, `User-Agent`,
    `X-Tunnel-Client-Name`, `X-Tunnel-Client-Version`,
    `X-Tunnel-Client-Wire-Protocol-Version`, `X-Tunnel-Client-Instance-Id`,
    and `X-Tunnel-MCP-Server-Info`) are managed by the client and cannot be
    overridden by extra headers.

## Request ID forwarding

`--forward-request-id` selects which outbound HTTP surfaces receive the
original request's `X-Request-Id` automatically:

- Flag: `--forward-request-id=mcp,harpoon`
- Env: `FORWARD_REQUEST_ID=mcp,harpoon`
- YAML: `forward_request_id: [mcp, harpoon]` (also accepts the scalar
  `forward_request_id: mcp,harpoon`)
- Default: `mcp`, preserving MCP Streamable HTTP request forwarding.

The supported surfaces are `mcp` (MCP HTTP requests), `harpoon` (Harpoon
`call_target` REST requests), `oauth` (queued OAuth discovery at the configured
MCP origin), and `none` (disable automatic request ID forwarding). For example,
`--forward-request-id=mcp,harpoon,oauth` enables all three named surfaces;
`--forward-request-id=oauth` enables only eligible OAuth forwarding, and
`--forward-request-id=none` disables automatic forwarding. An explicit value
replaces the default rather than adding to it. Repeated names are deduplicated.
Empty or unknown names, including `all`, and `none` combined with another name are
rejected. Flags override the environment variable, which overrides YAML.

Only an original inbound HTTP request ID is forwarded. The client never uses
its queue command ID or JSON-RPC ID as a replacement, and it adds no header
when the original ID is absent. Existing MCP header precedence and same-origin
limits still apply. Harpoon's explicit caller or template headers retain
ownership of `X-Request-Id`; automatic forwarding does not replace them or
cross redirect trust boundaries. This setting does not change stdio, OAuth
startup discovery requests, logging, or explicitly configured headers.

With `oauth` selected, queued OAuth discovery may forward a present original
inbound ID to the configured MCP server origin for its `WWW-Authenticate`
probe, protected-resource metadata, and authorization-server metadata hosted
there. Startup discovery has no inbound ID, and discovery at separate trusted
origins receives none. This setting does not forward an ID for OAuth shim
`call_target` requests. Existing same-origin redirect restrictions still apply.

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
  - Cross-origin redirects are still followed, but connector-forwarded
    request headers are removed.
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
  - Behavior: runs the command as a supervised child and uses its stdin/stdout for MCP frames
  - Deployment limit: multiple active `tunnel-client` instances sharing a
    tunnel ID with stdio bindings are **not supported**. See
    [stdio deployment limits](#stdio-deployment-limits).
  - Note: when using `MCP_COMMAND` with multiple entries, separate entries with
    newlines so semicolons remain part of the command.
- **Stdio failure policy**
  - Flag: `--mcp.stdio-failure-policy=restart|exit`
  - Env: `MCP_STDIO_FAILURE_POLICY`
  - YAML: `mcp.stdio_failure_policy`
  - Default: `restart` in all three tunnel-client flavors.
  - `restart` starts a replacement child after an unexpected exit, stdout EOF,
    or stdin write failure. In-flight calls can fail; callers must initialize
    the replacement child before retrying legacy calls.
  - `exit` makes tunnel-client exit nonzero on those failures so an external
    process supervisor can restart the whole client. An intentional client
    shutdown does not trigger either recovery path.
  - This setting applies to all stdio channel bindings in the instance and has
    no effect on HTTP MCP bindings. Before downgrading to a client without it,
    remove its YAML key and CLI flag; older clients reject unknown keys and flags.
- **Automatic stdio initialization guard**
  - Each request selects its protocol lifecycle automatically; no configuration
    is required. This applies to stdio channels only.
  - Reject legacy requests such as `tools/call`
    before the shared child completes a successful `initialize` exchange and
    receives `notifications/initialized`. This prevents requests from reaching
    a stateful stdio server before it is ready.
  - The caller owns initialization and request retry after child recovery. The
    guard does not synthesize `initialize`, replay rejected requests, or
    initialize a replacement child.
    The notification shim below can supply `notifications/initialized` after
    the caller's successful `initialize` response.
  - The client reports rejected calls with `status_code: 409`, JSON-RPC code `-32002`, and
    `error.data.error_type: "mcp_initialization_required"`; it is not written
    to the child. Send the handshake before retrying the call.
  - Self-contained requests with a protocol date of `2026-07-28` or later and
    a client-capabilities object in `params._meta` bypass this guard. The server
    validates the requested version and capabilities. These requests do not
    mark the legacy handshake complete.
  - Legacy callers must complete the handshake even if their server previously
    tolerated calls before initialization. Stateless HTTP targets are unaffected.
- **Stdio initialized notification shim (optional)**
  - Flag: `--mcp.stdio-send-initialized-notification`
  - Env: `MCP_STDIO_SEND_INITIALIZED_NOTIFICATION`
  - YAML: `mcp.stdio_send_initialized_notification`
  - Default: `false` (disabled)
  - When enabled, tunnel-client writes `notifications/initialized` after a
    successful forwarded stdio `initialize` response and suppresses a later
    duplicate from the caller. Enable it only for stdio servers that implement
    the MCP lifecycle notification and callers that can omit it; leaving it
    disabled forwards the caller's notifications without generating one.
- **Trace context forwarding (optional)**
  - Flag: `--mcp.forward-trace-context`
  - Env: `MCP_FORWARD_TRACE_CONTEXT`
  - YAML: `mcp.forward_trace_context`
  - Default: `false` in `tunnel-client`, `tunnel-client-runtime`, and
    `tunnel-client-runtime-cloudflared`. Explicit `false` overrides `true` from
    a lower-precedence source.
  - When enabled, valid tunnel-supplied W3C `traceparent` and optional
    `tracestate` are forwarded through MCP request `params._meta` over stdio
    and Streamable HTTP. Existing caller trace fields in metadata or request
    headers take precedence when local tracing is disabled. With local tracing
    enabled, the separate metadata handoff described below applies.
    This option adds no baggage and does not inject HTTP headers.
  - The MCP server must consume request metadata to connect its traces.
    Instrumentation that reads only HTTP tracing headers may not consume it.
  - Before downgrading to a client without this option, remove its YAML key
    and CLI flag; older clients reject unknown configuration keys and flags.
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
- **Startup listener wait (optional)**
  - Flag: `--mcp.startup-wait-timeout`
  - Env: `MCP_STARTUP_WAIT_TIMEOUT`
  - YAML: `mcp.startup_wait_timeout`
  - Default: `0s` (disabled)
  - When positive for an HTTP-streamable `main` MCP binding, tunnel-client
    delays its first control-plane poll and OAuth discovery until the
    MCP initialization probe completes. It retries pre-connect `connection
    refused`, missing Unix-socket errors, and HTTP `429`/`5xx` during this
    window. Authentication-required responses such as `401 Unauthorized`
    complete the probe without retrying.
  - Without this optional listener wait, HTTP `429`/`5xx` initialization
    responses retry in the background with backoff capped at 30 seconds.
    Readiness stays pending until the probe settles; other default probe
    failure handling is unchanged.
  - If the configured wait expires, `/readyz` remains non-ready. Polling still
    requires successful startup catalog completion when it is unscoped or its
    scope includes `harpoon`. Initialization retries never replay tunneled MCP
    commands. See [Supervising startup](health.md#supervising-startup) for the
    relationship between this wait, readiness, and polling.
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
  - Note: mTLS applies only to `http-streamable`; stdio has no TLS hop and
    channel-qualified mTLS on a non-HTTP binding is rejected.
- **Static MCP headers (optional)**
  - Flag (repeatable): `--mcp.extra-headers "Key: Value"`
  - Env: `MCP_EXTRA_HEADERS="Key: Value, Key2: Value2"`
  - YAML: `mcp.extra_headers`
  - Header values accept `env:VARNAME` and `file:/path/to/secret`; all other
    values are treated literally.
  - Header names must use valid HTTP field-name syntax and are
    case-insensitive. Identical case variants collapse to one header;
    conflicting values and invalid wire values are rejected before startup.
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
  - Header names and values follow the same validation and case-insensitive
    conflict rules as static MCP headers.
  - Scope: sent only to MCP discovery/probe requests for the configured MCP
    server origin, including OAuth Protected Resource Metadata discovery,
    WWW-Authenticate probing, and the startup MCP initialize probe.
  - Conflict behavior: these discovery/probe headers override
    `MCP_EXTRA_HEADERS` for discovery/probe requests. If connector-forwarded
    headers are present on a request, they are still applied last.
  - For log grouping, an operator-defined header such as
    `--mcp.discovery-extra-headers "X-Operator-Discovery: 1"` can label this
    traffic in MCP server logs. The same label can appear on startup OAuth,
    queued OAuth discovery, and the separate MCP startup initialization probe.
    It is static: it does not identify an individual probe or indicate whether
    a response allows discovery to continue. See
    [Supervising startup](health.md#supervising-startup) for interpreting the
    corresponding health states.

**OAuth-protected MCP notes:**

- OAuth discovery trusts the configured MCP server origin by default. Any
  additional origin advertised through `WWW-Authenticate resource_metadata`,
  or protected-resource metadata `authorization_servers` must be explicitly
  trusted. Redirects remain restricted to the selected metadata origin even
  when other origins are trusted. Trust is matched by scheme, host, and port;
  trusting a host does not trust its subdomains or other ports.
- Configure additional origins with repeated
  `--mcp.oauth-trusted-origin https://auth.example.com` flags, the newline-separated
  `MCP_OAUTH_TRUSTED_ORIGINS` environment variable, or the YAML
  `mcp.oauth_trusted_origins` list. Flags replace the environment or YAML list;
  the environment replaces the YAML list. Entries must be absolute `http://`
  or `https://` origins without credentials, a path (except an optional `/`),
  a query, or a fragment. Explicitly trusted private origins are supported.
  This setting authorizes discovery requests only; it does not add forwarding
  targets or expand the scope of configured credentials.
  To authorize discovered endpoints on a separate origin for forwarding, use
  the independent [OAuth forwarding allowlist](#oauth-forwarding-origins).
- When upgrading a configuration that discovers metadata or an authorization
  server on a separate origin, add each expected origin to this list before
  starting the client. For example, an MCP server at
  `https://mcp.example.com/mcp` whose metadata advertises
  `https://auth.example.com/tenant` needs `https://auth.example.com` in
  `mcp.oauth_trusted_origins`. An untrusted server cannot expand this list by
  advertising more origins.
- For a mixed-version rollout, first set `MCP_OAUTH_TRUSTED_ORIGINS` in the
  process environment and leave existing YAML and flags unchanged. Older
  releases ignore this environment variable; updated binaries enforce it.
  For the example above, use
  `MCP_OAUTH_TRUSTED_ORIGINS=https://auth.example.com`. Separate multiple origins
  with newlines. This single configuration supports both binary versions.
- Keep the environment setting throughout the mixed-version rollout and
  rollback window. Canary the new binary, validate discovery and authenticated
  MCP requests, then upgrade the remaining deployments independently. Only
  switch to the new YAML key or flag after no older binary needs to read that
  configuration: older releases reject those new settings. No coordinated
  tunnel-service upgrade is required. Updated binaries enforce explicit trust
  immediately; the compatibility window does not enable permissive discovery.
- Before promoting the upgrade, validate OAuth discovery and an authenticated
  MCP request using the deployment's configured transport. Retain the previous
  binary and compatible configuration until validation completes. Prefer
  correcting missing trusted origins. If a rollback is approved, restore the
  previous binary with the unchanged legacy YAML/flags and pre-staged environment
  variable, or restore its matching configuration if new YAML/flags were adopted.
  Running the previous binary temporarily removes the new protection.
- Forwards inbound `Authorization` headers and protected-resource discovery
  GETs through the tunnel client. Discovery payload `resource` values and
  `WWW-Authenticate resource_metadata` values are rewritten to tunnel-service
  URLs for the same `tunnel_id`.
- Uses `authorization_servers[0]` from PRMD as the source of truth and metadata
  fetch target for auth-server metadata enrichment and Harpoon OAuth target
  registration.
- Accepts auth-server metadata even when metadata `issuer` differs from
  `authorization_servers[0]` (external IdP issuers are supported). Mismatch
  details are preserved in diagnostics and logs.
- Registered `harpoon://` `registration_endpoint`, `token_endpoint`, and
  `revocation_endpoint` values are rewritten to Tunnel OAuth-shim routes.
  Their POST requests and responses traverse Tunnel and Harpoon; public
  `http(s)` endpoint URLs remain unchanged and are called by the product OAuth
  caller rather than through Tunnel.
- The OAuth shim does not rewrite `authorization_endpoint`; the supported
  auto-registered path leaves browser authorization direct to the upstream
  authorization server. Tunnel does not expose arbitrary authorization-server
  routes.

### Response timing compatibility

The client reports bounded, client-observed MCP elapsed time in optional
`resp_timing` response metadata. This includes downstream transport delays.
Reporting timing requires no customer MCP server changes. Trace forwarding
remains separately opt-in.

Fully deploy service readers that tolerate `resp_timing` before releasing a
client that emits it. After timing-enabled clients are released, every service
rollback target must retain that tolerant reader; older services are unsupported.
Legacy clients can continue omitting timing, and the original end-to-end latency
remains available.

## Channels

`tunnel-client` supports multiple logical channels:

- `main`: required; configured from `--mcp.server-url` or `--mcp.command`.
- `harpoon`: built-in and enabled only when Harpoon has at least one registered
  target (see Harpoon config below).
- additional channels: configured via channel-qualified `--mcp.server-url`
  and/or `--mcp.command` entries.

All response payloads posted to `/v1/tunnels/{tunnel_id}/response` include the
resolved `channel` value.

### Stdio deployment limits

Run only **one active `tunnel-client` instance per tunnel ID** when using
`--mcp.command` / `MCP_COMMAND`. Multiple active instances sharing that tunnel
ID are **not supported**, whether they run on the same host, on different
hosts, in containers, or in Kubernetes Pods. This also includes temporary
overlap during a rolling restart or upgrade.

Each instance starts a separate stdio MCP child with its own initialization
and session state. Tunnel requests are not pinned to the instance that
handled initialization: `initialize` can reach one child and a later
`tools/call` another. Calls can therefore time out even when every instance
reports healthy and ready. Setting `--mcp.max-concurrent-requests=1` limits
work within each instance; it does not provide routing between instances.

Stop the old instance before starting its replacement. For a Kubernetes
Deployment using stdio, use `replicas: 1` and `strategy.type: Recreate` to avoid
overlap during updates; `replicas: 1` alone can still permit a rolling-update
surge. To run independent instances, give each a distinct tunnel ID. Multiple
stdio bindings on different channels within one instance remain supported.

Stdio uses one shared child connection per channel, without independent MCP
session isolation. The `proc_affinity` capability declares a need for process
affinity; it does not implement routing affinity. The
`--mcp.stdio-send-initialized-notification` option only sends the notification
after a successful forwarded `initialize` response. It does not initialize
every replica or make multiple stdio instances safe.

By default, if a stdio child exits unexpectedly, closes stdout, or stops
accepting stdin writes, `tunnel-client` starts a replacement child. Set
`--mcp.stdio-failure-policy=exit` to exit nonzero and delegate recovery to an
external process supervisor. An intentional client shutdown still exits
successfully.

### Stdio failure policy upgrade and rollback

Deployments that rely on a process supervisor restarting the entire client
should select `exit` when upgrading to a version with the `restart` default.
Use `MCP_STDIO_FAILURE_POLICY=exit` while older and newer binaries may read the
same deployment configuration; add the YAML key or CLI flag only after older
binaries are no longer used. The `exit` policy remains supported throughout
this rollout and rollback window. Removing it would require a separate,
reviewed deprecation and migration plan.

Upgrade one stdio tunnel at a time without overlapping active clients. Before
promoting an upgraded deployment, verify that an unexpected child failure
either starts a replacement child under `restart` or exits nonzero under
`exit`. For `restart`, also verify that legacy callers can initialize the new
child and complete a subsequent request; requests in flight during failure
may fail and are not replayed. If that validation fails, select `exit` to
restore whole-client supervision or roll back the binary. Remove the new YAML
key or CLI flag before rolling back to a version that does not recognize it.

## Harpoon MCP (outbound HTTP allowlist)

`harpoon` is an embedded MCP server that exposes an allowlisted, buffered HTTP
client with labeled targets.

Harpoon's channel (`harpoon`) is enabled only when at least one target is
registered. If there are no targets, `harpoon` commands return
`unsupported_channel`.

Startup OAuth discovery retries timeouts, HTTP `429`/`5xx`, and malformed
successful metadata responses with jittered exponential backoff capped at
30 seconds. This includes transient failures fetching advertised authorization
server metadata, so polling does not begin with a partial discovered target
catalog. Discovery and target registration must complete before an unscoped
poller, or a poller whose scope includes Harpoon, consumes commands. Optional
OAuth discovery on plain MCP servers may complete with an empty catalog;
this does not enable the Harpoon channel. Permanent configuration and trust
errors are not bypassed, and readiness remains non-ready while retrying.

At startup, after configured targets and startup OAuth-discovered targets have
been processed, tunnel-client emits one INFO log named
`harpoon startup catalog digest`. The HMAC digest is a privacy-safe comparison
aid: it does not expose target labels, URLs, paths, or credentials. Compare
digests only between replicas using the same tunnel ID and the same resolved
control-plane runtime API key; a different key or key rotation intentionally
produces a different digest. The digest is startup-only and is not updated for
later OAuth discovery commands or registry mutations.

- **Target mappings**
  - Flag (repeatable): `--harpoon.target="label=auth,url=https://auth.example.com,desc=Auth server"`
  - Env: `HARPOON_TARGETS` (semicolon- or newline-delimited list of the same
    `label=...,url=...,desc=...` entries)
- **Outbound request headers**
  - The `call_target` tool drops transport proxy forwarding and client-managed
    identity headers, plus every caller-supplied field named by a `Connection`
    header.
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
  - `list_targets` continues to omit target URLs. The separate read-only
    `get_oauth_target_audience` tool is an explicit, narrow exception for
    auto-registered OAuth `token-endpoint` targets: it returns only the
    exact upstream token URL needed as a `private_key_jwt` audience.
    Generic configured targets, non-token OAuth targets, credentialed URLs,
    and fragment-bearing URLs are rejected.
- **Allow plaintext HTTP**
  - Flag: `--harpoon.allow-plaintext-http`
  - Env: `HARPOON_ALLOW_PLAINTEXT_HTTP`
  - Default: `false`
  - OAuth-discovered protected-resource targets use this same policy. For a
    trusted HTTP loopback MCP endpoint, prefer HTTPS or explicitly enable this
    setting so its PRMD source target can be registered.
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
    at `POST /harpoon/mcp` (loopback-only unless `--allow-remote-ui` is set).
    MCP 2026-07-28 self-contained POST requests are sessionless. Legacy
    `initialize` / `notifications/initialized` POST flows remain stateful,
    including standalone SSE `GET` and session-termination `DELETE` requests
    carrying their `Mcp-Session-Id`.
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

### OAuth forwarding origins

Use this independent opt-in when discovered OAuth endpoints on a separate HTTPS
origin need to be reachable through the tunnel:

```yaml
mcp:
  oauth_trusted_origins:
    - https://auth.example.com
harpoon:
  oauth_forward_origins:
    - https://auth.example.com
```

- Flag (repeatable): `--harpoon.oauth-forward-origin https://auth.example.com`
- Env: `HARPOON_OAUTH_FORWARD_ORIGINS` (newline-separated origins)
- YAML: `harpoon.oauth_forward_origins`
- Default: empty; existing discovery and registration behavior is unchanged.

Flags replace the environment or YAML list; the environment replaces YAML.
An explicit empty flag or environment value clears lower-precedence entries.
Each entry must be an absolute HTTPS origin without credentials, a path
(except an optional `/`), query, fragment, or IPv6 zone identifier. Matching uses the exact scheme,
hostname, and effective port. It does not include subdomains or other ports.

The list authorizes registration of discovered OAuth endpoint URLs on those
origins, preserving their OAuth source, endpoint roles, and group associations.
It does not register arbitrary URLs, grant discovery trust, widen configured
credential scope, or relax existing redirect and exact-target checks. The
discovery-only `mcp.oauth_trusted_origins` setting is still required for separate
metadata origins. An advertised endpoint on a third origin needs its own
forwarding approval if the existing registration policy does not admit it.

Include the protected-resource metadata source's origin as well as the required
authorization-server endpoint origins. Generic `harpoon.targets` have the
`config` category and do not replace these discovery-generated OAuth records.
Successful explicit admissions log `inclusion_reason=oauth-forwarding-origin`.

Upgrade every active poller for the tunnel and apply equivalent configuration
before relying on the new forwarding path. Commands may be consumed by any
poller; an older client does not gain this capability from the new environment
variable. Compare startup target counts and catalog digests using the same
tunnel ID and runtime key, then validate discovery, sign-in, and an authenticated
MCP call. Older binaries reject the new YAML key and flag; remove those settings
before downgrading. Downgrading removes the explicit forwarding capability.

## Local tracing

Local tracing is disabled by default in every binary and the Go SDK. Configure
`--tracing.enabled` / `TRACING_ENABLED` / `tracing.enabled` and
`--tracing.file` / `TRACING_FILE` / `tracing.file`. Flags override environment,
which overrides YAML; explicit `false` disables lower-precedence opt-ins.
Supplying a file alone does not enable tracing. The SDK uses `Config.Tracing`
and `Config.ForwardTraceContext`.

```yaml
tracing:
  enabled: true
  file: /private/new-trace.jsonl
mcp:
  forward_trace_context: true
```

The file must be a new regular file in an existing directory; existing files,
symlinks, and special files are rejected. Files use mode `0600` on Unix and
inherit the directory's access controls on Windows. Choose a new path each run.
Remove these options before downgrading to a client that does not support them.

Enabling tracing advertises `trace-context-v1`. The client accepts each command's
validated v00 `traceparent` as its parent, never the poll connection's parent.
Absent, invalid, or oversized context produces no local trace and leaves command
execution unchanged. The original parent's sampled flag controls recording;
the client never forces sampling or creates an unrelated root. Each request has
fixed `tunnel.client.dispatch`, `tunnel.client.mcp`, and
`tunnel.client.upload` spans. Each response POST attempt adds a
`tunnel.client.upload_attempt` child of the upload span. Its elapsed time covers
the HTTP request through the returned response or transport error; retry waits
appear as gaps between attempt spans inside the upload span.
With `mcp.forward_trace_context` enabled, the MCP span context is forwarded
through `params._meta`, preserving caller-owned metadata trace fields. Existing
Per-command forwarded HTTP headers remain byte-for-byte unchanged but do not
suppress this separately opted-in metadata handoff. Operator-configured
`mcp.extra_headers` containing `traceparent` or `tracestate` still suppress
metadata forwarding, including with both options enabled or an empty header
value. A metadata-aware target uses the MCP child; a
header-only target retains the original HTTP parent. The target must
extract that metadata to create its own execution span. Local tracing does not
inject HTTP tracing headers or propagate baggage or incoming `tracestate`.

JSONL contains `name`, `trace_id`, `span_id`, `parent_span_id`, `start_time`,
`end_time`, and OpenTelemetry `status`. Attempt records additionally contain
`attempt`, `elapsed_ms`, `outcome`, and `http_status` when an HTTP response was
received. `outcome` is one of `accepted`, `already_fulfilled_or_unknown` (HTTP
404), `http_error`, `timeout`, `canceled`, `network_error`, or `request_error`.
The file excludes payloads, prompts, credentials, headers, raw errors,
arbitrary attributes, resource fields, events, and exception descriptions.
For example, inspect one run with:

```sh
jq -r 'select(.name == "tunnel.client.upload_attempt") | [.trace_id, .parent_span_id, .attempt, .elapsed_ms, (.http_status // "-"), .outcome] | @tsv' /private/new-trace.jsonl
```

This is a local JSONL export, not a collector or OTLP export. Attempt records
require both opt-in local tracing and a valid sampled command parent, as above.
An isolated tracing provider leaves the application's global provider unchanged.
Export runs asynchronously with a 1024-record queue and a 64 MiB file limit.
Queue saturation drops records instead of delaying responses. A write failure or
file limit stops writes for that run; command execution continues. Shutdown
drains after command workers stop, with at most one second of exporter waiting.
A blocked operating-system write can retain the single writer until it returns.

Privacy-safe counters under `tunnel_client_tracing_*` report commands, missing
and invalid carriers, unsampled parents, exported records, dropped records, and
export failures. Trace IDs are never metric labels. Compare complete-trace
coverage and request latency, CPU, and memory with a flags-off baseline; stop a
canary on unexpected missing parents, drops, export failures, or overhead.

The client can ship with flags off before service support. Enabling complete
traces requires the service's queue/poll carrier support and upstream request
parent handoff to be deployed and validated first. Older services may ignore the
capability and omit the carrier; those commands continue without local spans.
Start with isolated synthetic traces, then a separately authorized small canary
before expanding. Roll back by disabling `tracing.enabled` and
`mcp.forward_trace_context`; retain compatible service support during rollback.

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

See [local health and component details](health.md) for the route contract,
HTTP and Unix examples, and component meanings. Existing `/healthz`, `/readyz`,
and `/metrics` behavior is unchanged.

- **Show component details by default**
  - Flag: `--health.show-details`
  - Env: `HEALTH_SHOW_DETAILS`
  - YAML: `health.show_details`
  - Default: `false`, in all three binary flavors.
  - Precedence: explicit flag, environment, YAML/profile, default. Explicit
    false overrides true from a lower-precedence source.
  - `/health?details=true` and `/health?details=false` override the default for
    one request. `/health/mcp` always returns component details.
  - All new `/health` routes require loopback TCP or the configured Unix
    socket. This setting controls output only; it does not initiate probes or
    expand access, including when `--allow-remote-ui` is set.

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

- **UI entrypoints**: `GET /`, `GET /ui`, or `GET /ui/`
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
- **Base path (optional)**
  - Flag: `--admin-ui.base-path`
  - Env: `ADMIN_UI_BASE_PATH`
  - YAML: `admin_ui.base_path`
  - Default: empty (the UI uses the root paths above).
  - Set this only when a reverse proxy preserves the prefix in requests to
    tunnel-client. For example, `/tools/tunnel` serves the UI at
    `/tools/tunnel/`, `/tools/tunnel/ui`, and `/tools/tunnel/ui/`, with its
    assets and admin API under the same prefix. The root `/healthz`, `/readyz`,
    and `/metrics` endpoints remain available for probes and monitoring.
  - A nonempty value must start with `/` and contain slash-separated,
    nonempty segments made only of unreserved ASCII characters
    (`A-Z`, `a-z`, `0-9`, `.`, `_`, `~`, `-`). The maximum configured length
    is 2048 bytes before normalization. `/` normalizes to empty, and one
    trailing slash is removed. Segments `.` and `..`, double slashes, percent
    escapes, query strings, fragments, wildcards, and braces are rejected. The
    `/health`, `/healthz`, `/readyz`, `/metrics`, and `/harpoon` namespaces
    (the exact paths and their descendants) are reserved and cannot be used
    as a base path.
- **Public browser origin (optional)**
  - Flag: `--admin-ui.public-origin`
  - Env: `ADMIN_UI_PUBLIC_ORIGIN`
  - YAML: `admin_ui.public_origin`
  - Default: unset.
  - Adds one exact HTTP(S) browser origin to the allowed `Origin` and `Referer`
    origins for admin UI requests protected by the same-origin check (currently
    Codex actions), after the existing UI network access check. Set it if the
    proxy connects over loopback or Unix and the browser hostname is neither
    `localhost` nor a loopback IP, even when Host is preserved. Also set it when
    TLS termination or Host rewriting makes the browser and upstream origins
    differ, for example `https://ops.example.com` behind a TLS-terminating
    proxy. Scheme, host, and effective port must match; the default ports are
    80 for HTTP and 443 for HTTPS.
  - Supply an origin only: no credentials, path except an optional `/`, query,
    fragment, wildcard, or IPv6 zone identifier. Hostnames must use ASCII;
    use the browser's punycode form for internationalized hostnames. A port,
    when present, must be between 1 and 65535. Every path on that origin receives
    the same browser-origin permission, so use an origin whose applications you
    trust.
  - This permission does not authenticate users or grant network access. The
    existing loopback/Unix default and `--allow-remote-ui` boundary still
    apply. It does not change generated URLs or trust `Forwarded`,
    `X-Forwarded-*`, or provider-specific prefix headers.
- **Runtime log level toggle**
  - The Logs tab can change the live runtime log level between `debug`, `info`,
    and `warn` through `GET`/`PUT /api/log-level`.
  - Use this for short troubleshooting windows without restarting the client.

The `base_path` and `public_origin` settings are available only in the full
client. Their precedence is flag, environment, YAML, then default; an explicitly
empty flag or environment value clears a value from a lower-precedence source.
Runtime-only binaries do not expose these flags and reject nonempty environment
or YAML values; empty environment and YAML values are accepted for compatibility.
Older clients do not support these options: remove the new flags and YAML keys
before downgrading, and do not expect the new environment variables to enable
prefix support on an older version.

### Reverse proxy paths

Choose the layout that matches how the proxy forwards requests:

| Proxy behavior | Browser UI URL | Upstream UI URL | Client setting |
| --- | --- | --- | --- |
| Strip `/tools/tunnel` | `/tools/tunnel/ui` | `/ui` | Leave `--admin-ui.base-path` empty |
| Preserve `/tools/tunnel` | `/tools/tunnel/ui` | `/tools/tunnel/ui` | Set `--admin-ui.base-path=/tools/tunnel` |

With a prefix-stripping proxy, the UI's relative URLs keep assets and admin API
requests under the browser's prefix without a base-path setting. Expose the
mount root with a trailing slash, such as `/tools/tunnel/`, or link to
`/tools/tunnel/ui` or `/tools/tunnel/ui/`. If users may visit the bare mount
`/tools/tunnel`, configure the proxy to redirect it to `/tools/tunnel/`; after
stripping the prefix, the client cannot distinguish those two browser URLs.

With a prefix-preserving proxy, set the base path to the prefix and forward it
unchanged. The UI and its admin routes move under that mount, while root
`/healthz`, `/readyz`, and `/metrics` keep their existing behavior. The UI uses
prefixed aliases for those endpoints. For either layout, use the canonical UI
URLs shown above; malformed paths containing dot segments or repeated slashes
may follow Go's standard path-cleaning redirects.

Keep the admin server on loopback or a Unix socket when the proxy runs locally.
When the proxy connects over a non-loopback network, use `--allow-remote-ui`
only on a trusted operator network and provide authentication at the proxy as
needed. Configure `admin_ui.public_origin` for a browser hostname other than
`localhost` or a loopback IP when the proxy connects locally, or when the proxy
changes the upstream scheme or host. The client does not infer the public origin
or prefix from proxy headers, so the proxy must enforce its own routing and
header policy.

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
