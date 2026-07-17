# Secure MCP Tunnel client

`tunnel-client` is the customer-run agent behind Secure MCP Tunnel. It connects
a private or localhost MCP (Model Context Protocol) server to ChatGPT, Codex,
the Responses API, and AgentKit through an OpenAI-hosted MCP tunnel endpoint,
while keeping the MCP server off the public internet.

Use it when:

- You have an MCP server on a laptop, VM, Kubernetes cluster, or private
  network and need an OpenAI-hosted product to reach it.
- Security will not approve a new inbound firewall rule or public endpoint for
  the MCP server.
- You want an operator-visible daemon with `/healthz`, `/readyz`, `/metrics`,
  and `/ui` before a connector or API call depends on it.

If you searched for "secure MCP tunnel", "MCP tunnel ChatGPT", "connect local
MCP server to ChatGPT", "connect local MCP server to Codex", "localhost to
ChatGPT", or "Codex local MCP", start with `tunnel-client help quickstart`,
then read the onboarding guide below.

## Start Here

- **Need the shortest working path from localhost or a private MCP server to
  ChatGPT or Codex?** Start with [`docs/onboarding.md`](docs/onboarding.md).
- **Need the customer-shareable network and trust-boundary story?** Read
  [`docs/architecture.md`](docs/architecture.md).
- **Need roles, groups, tunnel IDs, or API keys?** Read
  [`docs/permissions.md`](docs/permissions.md).
- **Need Docker, Kubernetes, or VM deployment guidance?** Read
  [`docs/deployment/overview.md`](docs/deployment/overview.md).
- **Need to debug readiness, connector discovery, or OAuth?** Read
  [`docs/troubleshooting.md`](docs/troubleshooting.md).
- **Building a compatible client in another language?** Read
  [`docs/protocol.md`](docs/protocol.md) and use
  [`docs/openapi.json`](docs/openapi.json).
- **Embedding an MCP server directly in a Go process?** Use the Go SDK with
  the MCP SDK's in-memory transport; see
  [`examples/go-sdk-inmemory`](examples/go-sdk-inmemory).

## Embed as a Go SDK

The module can run in the same process as a Go MCP server. The MCP server does
not need to bind a port or use stdio: give the server side of an in-memory MCP
transport pair to your server and the client side to `tunnelclient.New`.

~~~bash
go get github.com/openai/tunnel-client
~~~

~~~go
import (
    "context"

    "github.com/modelcontextprotocol/go-sdk/mcp"
    tunnelclient "github.com/openai/tunnel-client"
)

ctx := context.Background()
server := mcp.NewServer(&mcp.Implementation{Name: "my-server", Version: "1.0.0"}, nil)
serverTransport, tunnelTransport := mcp.NewInMemoryTransports()
go server.Run(ctx, serverTransport)

client, err := tunnelclient.New(tunnelclient.Config{
    TunnelID: "tunnel_0123456789abcdef0123456789abcdef",
    APIKey:   apiKey,
}, tunnelTransport)
if err != nil {
    return err
}
return client.Run(ctx)
~~~

The runnable [Go SDK example](examples/go-sdk-inmemory) registers an `echo`
tool and connects it to the OpenAI Tunnel control plane.

## Documentation Map

- **Public Secure MCP Tunnel guide**:
  [`developers.openai.com/api/docs/guides/secure-mcp-tunnels`](https://developers.openai.com/api/docs/guides/secure-mcp-tunnels)
- **Shareable end-user guide**: [`docs/end-user-guide.md`](docs/end-user-guide.md)
- **Start here**: [`docs/onboarding.md`](docs/onboarding.md)
- **Permissions, roles, and groups**: [`docs/permissions.md`](docs/permissions.md)
- **Architecture diagrams**: [`docs/architecture.md`](docs/architecture.md)
- **Connector behavior**: [`docs/connectors.md`](docs/connectors.md)
- **Wire protocol for client implementers**: [`docs/protocol.md`](docs/protocol.md)
- **OpenAPI contract**: [`docs/openapi.json`](docs/openapi.json)
- **Enterprise customer handoff**:
  [`docs/enterprise-customer-onboarding.md`](docs/enterprise-customer-onboarding.md)
- **Configuration reference**: [`docs/configuration.md`](docs/configuration.md)
- **Deployment guides**: [`docs/deployment/overview.md`](docs/deployment/overview.md)
- **Bundled Cloudflare companion**:
  [`docs/deployment/cloudflared.md`](docs/deployment/cloudflared.md)
- **Troubleshooting**: [`docs/troubleshooting.md`](docs/troubleshooting.md)
- **Development & testing**: [`docs/development.md`](docs/development.md)
- **In-memory Go SDK example**:
  [`examples/go-sdk-inmemory`](examples/go-sdk-inmemory)
- **Roadmap / design notes**: [`docs/roadmap.md`](docs/roadmap.md)

To generate the shareable guide output locally:

```bash
make end-user-guide-screenshots
make end-user-guide-html
make end-user-guide-slides
```

## For Codex / Claude / Copilot

If you want the shortest supported path from a local or localhost MCP server to
ChatGPT or Codex, start with `tunnel-client help quickstart`. For Codex plugin
lifecycle work, use the native `tunnel-client runtimes ...` and
`tunnel-client admin-profiles ...` command trees surfaced by
`tunnel-client help plugin`.

Supervision choice:

- Use `tunnel-client run ...` when you intentionally want a foreground daemon
  attached to the current terminal.
- For a long-lived local runtime managed by Codex, prefer
  `tunnel-client runtimes connect ...`. Do not use `nohup` or `disown` as the
  tunnel-client supervision path.
- After `runtimes connect`, check `tunnel-client runtimes status <alias>`
  before reporting success. Only report success when status shows the managed
  runtime running with health reported. Use `--json` when Codex needs the
  explicit `process_running`, `healthy`, and `ready` fields.

Use these exact setup pages during first use:

- Tunnels management and supported tunnel-client download:
  `https://platform.openai.com/settings/organization/tunnels`
- Organization roles: `https://platform.openai.com/settings/organization/people/roles`
- Organization groups: `https://platform.openai.com/settings/organization/people/groups`
- Runtime API keys: `https://platform.openai.com/settings/organization/api-keys`
- Admin API keys: `https://platform.openai.com/settings/organization/admin-keys`
- ChatGPT connector settings: `https://chatgpt.com/#settings/Connectors`

Which value comes from where:

- `CONTROL_PLANE_TUNNEL_ID`: create or inspect it in Tunnels management, or via
  `tunnel-client admin tunnels create|list|get ...` with `OPENAI_ADMIN_KEY`.
- `CONTROL_PLANE_API_KEY`: create it in Runtime API keys; this is the key used
  by `tunnel-client doctor` and `tunnel-client run`.
- `OPENAI_ADMIN_KEY`: only for `tunnel-client admin tunnels
  list|create|update|delete`. Do not use the admin key for the long-lived
  daemon.

Required tunnel permissions:

- Runtime users and the principal that creates `CONTROL_PLANE_API_KEY` need
  Tunnels **Read** + **Use**.
- Tunnel managers need Tunnels **Read** + **Manage**, plus **Use** if they also
  run the daemon or attach ChatGPT connectors.
- Admin-key creators need the Platform admin-key permission in addition to any
  tunnel permissions they need.

See [`docs/permissions.md`](docs/permissions.md) for the group/role workflow
and screenshots.

Binary-first flow:

```bash
tunnel-client help quickstart
tunnel-client profiles samples list
tunnel-client profiles samples show sample_mcp_enterprise_proxy
tunnel-client init --sample sample_mcp_stdio_local --profile local-stdio --tunnel-id tunnel_0123456789abcdef0123456789abcdef --mcp-command "python /path/to/server.py"
tunnel-client doctor --profile local-stdio --explain
tunnel-client run --profile local-stdio
tunnel-client run --profile-file ./profiles/local-stdio.yaml
```

If you need the tunnel id or runtime/admin keys first, open the matching URL
above before running `init`. If your rollout has self-serve tunnel access,
create the tunnel yourself in Tunnels management or with
`tunnel-client admin tunnels create`, then export the returned id as
`CONTROL_PLANE_TUNNEL_ID` and a separate runtime key as
`CONTROL_PLANE_API_KEY`. Create or verify the connector from the ChatGPT
settings URL above only while `tunnel-client run ...` is healthy, and keep the
daemon running for connector discovery and every MCP call from ChatGPT.

The Platform Tunnels page download button is sourced from tunnel-service's
gated tunnel metadata response. When a new public tunnel-client release becomes
the supported download, update tunnel-service's hard-coded public artifact URL
alongside the release handoff.

Validate a source checkout with native Go tooling:

```bash
go build ./...
go test ./...
```

Build the CLI binary:

```bash
make admin-ui
go build -o bin/tunnel-client ./cmd/client
./bin/tunnel-client help quickstart
```

Public releases use plain semantic-version tags such as `v0.0.10`. Source
archives from release tags carry the release version in
`pkg/version/VERSION`. A plain `go build` from a downloaded release `.tar.gz`
therefore reports the tag semantic version through `tunnel-client --version`,
`User-Agent`, and the explicit control-plane version headers.

Supported release archives also bundle pinned `cloudflared` `2026.7.2` beside
the CLI for Linux `amd64`/`arm64`, macOS `amd64`/`arm64`, and Windows
`amd64`/`arm64`.
Docker images bundle the Linux `amd64`/`arm64` companion. To run a
pre-provisioned Cloudflare tunnel without putting its token in argv:

```bash
export CLOUDFLARED_TOKEN='...'
tunnel-client run \
  --cloudflared.token env:CLOUDFLARED_TOKEN \
  --control-plane.tunnel-id tunnel_0123456789abcdef0123456789abcdef \
  --mcp.server-url https://mcp.example.com/mcp
```

See [`docs/deployment/cloudflared.md`](docs/deployment/cloudflared.md) for
platform coverage, readiness/failure behavior, Go module provenance, and
security-update ownership.

If an operator intentionally runs `cloudflared` without `tunnel-client`, print
a token-free production config and keep the token in a separate secret file:

```bash
tunnel-client cloudflared config \
  --token-file /run/secrets/cloudflared/token \
  > /etc/cloudflared/config.yml
cloudflared tunnel --config /etc/cloudflared/config.yml run
```

Fastest Codex terminal path:

```bash
tunnel-client codex assistant "Summarize what tunnel-client is doing in this checkout."
tunnel-client codex status
tunnel-client codex plugin install
tunnel-client runtimes list
tunnel-client help plugin
tunnel-client codex plugin uninstall
```

Choose the raw binary when you want the smallest possible setup surface.
Choose `tunnel-client codex assistant` when you want the fastest Codex-native
terminal path. Choose the plugin when you want a Codex-local entrypoint over
the native `runtimes` / `admin-profiles` command trees.

Starter prompts for Codex:

- `Figure out what tunnel-client is for from the binary help, then get me to /ui with the shortest local path.`
- `I only have the source checkout. Figure out how to build tunnel-client, then get me to /ui with the shortest local path.`
- `Use tunnel-client to create or reuse a profile, run doctor --explain, and then start the foreground daemon attached to this terminal.`
- `Run tunnel-client codex assistant and summarize what this checkout is for in one sentence.`
- `Install the Codex plugin from the tunnel-client binary, connect the provided tunnel id, and tell me whether the runtime is launched, healthy, or ready.`
- `For a long-lived local runtime, use tunnel-client runtimes connect to attach the provided tunnel id, then run tunnel-client runtimes status <alias> before reporting whether the runtime is launched, healthy, or ready.`
## What it does

- The client **long-polls** the OpenAI tunnel control plane over HTTPS:
  - `GET /v1/tunnels/{tunnel_id}/poll`
  - `POST /v1/tunnels/{tunnel_id}/response`
- Older tunnel-client releases may still use the singular `/v1/tunnel/...`
  aliases. Tunnel-service keeps those aliases during migration; removing them,
  if ever desired, is a separate later cleanup after telemetry shows no
  remaining legacy clients.
- Control-plane requests include `User-Agent: oai-tunnel-client/<version>` for
  compatibility, plus explicit `X-Tunnel-Client-Name` and
  `X-Tunnel-Client-Version` headers for service-side logs and metrics. Each
  process also generates a new opaque `client_instance_id`, sends it as
  `X-Tunnel-Client-Instance-Id`, and includes it in structured logs and the
  local admin UI for request correlation.
- Control-plane HTTPS requests can present a separate client certificate/key
  pair using `--control-plane.client-cert` and `--control-plane.client-key`
  (or `CONTROL_PLANE_CLIENT_CERT` / `CONTROL_PLANE_CLIENT_KEY`). When those are
  configured with the default `https://api.openai.com` host, the client
  automatically uses `https://mtls.api.openai.com` for control-plane calls.
- On startup, it fetches tunnel metadata for operator visibility:
  - `GET /v1/tunnels/{tunnel_id}`
- It forwards received JSON-RPC requests to your configured MCP server over
  Streamable HTTP, stdio, or in-memory transport, and relays explicit
  Streamable HTTP session termination requests when the control plane receives
  `DELETE /v1/mcp/{tunnel_id}`.
- It routes commands by channel: `main` targets the configured MCP binding,
  additional configured channels can target their own MCP bindings, and
  `harpoon` is routable only when Harpoon has registered targets.
- On startup, it fetches OAuth Protected Resource Metadata from the MCP server
  for diagnostics.
- For OAuth auth-server handling, `authorization_servers[0]` from PRMD is the
  only source of truth and metadata fetch target.
- Metadata is accepted even when `issuer` differs from
  `authorization_servers[0]` (external IdP issuer URLs are supported), with
  mismatch diagnostics preserved in logs/state.
- It exposes an **admin/health server** (`/healthz`, `/readyz`, `/metrics`) and
  a lightweight **admin UI** (`/ui`) for operational status.
- The admin UI Overview reports the process-scoped `client_instance_id`,
  channel availability, and reasons when channels are disabled.
- The admin UI Logs tab can switch the live runtime log level between `debug`,
  `info`, and `warn` without restarting the process.
- The admin UI log export returns a redacted support bundle with recent logs
  plus a point-in-time Prometheus snapshot from `/metrics` and a redacted
  runtime YAML snapshot containing argv, relevant environment, actual YAML
  config, and effective config.
- It embeds the **Harpoon MCP server** to provide a labeled, allowlisted
  outbound HTTP client for internal tooling.

## Admin UI build notes

The admin UI assets under `pkg/adminui/assets` are generated from the TypeScript/Svelte
source in `adminui/`. To rebuild them locally:

```bash
./scripts/build_admin_ui.sh ./adminui ./pkg/adminui/assets
# or
make admin-ui
```

## CLI

- `tunnel-client` shows help and available subcommands.
- `tunnel-client help <topic>` shows embedded task-oriented help for
  `quickstart`, `samples`, `doctor`, `oauth`, and `plugin`.
- `tunnel-client codex assistant [prompt...]` starts a terminal assistant
  session through the supervised `codex app-server`, using prompt args for
  one-shot mode and TTY stdin for REPL mode. It defaults to `medium`
  reasoning effort, and the REPL supports `/model` to inspect or change model
  and reasoning without restarting.
- `tunnel-client codex status|install|upgrade|uninstall` inspects local Codex
  CLI/app-server availability and prints the official install/upgrade/remove
  commands.
- `tunnel-client codex plugin install|uninstall|export` installs, removes, or
  exports the embedded Tunnel MCP plugin bundle.
- `tunnel-client dev mcp-stub` runs an embedded demo MCP + OAuth metadata server
  for one-binary end-to-end validation.
- `tunnel-client dev proxy` runs a local control plane plus tunnel-client for
  integration tests. TCP ingress is the default and prints `mcp_url`; pass
  `--listen-unix-socket PATH` for external MCP ingress over a Unix socket. Its
  `--backend auto|go|rust` flag defaults to `auto`, and
  `--engine-queue-backend inmem|redis` defaults to `inmem`. Ordinary public
  builds use the Go in-memory backend; `rust` and Redis require a binary with
  the optional linked Rust adapter. Redis also needs `--engine-redis-url` or
  `TUNNEL_ENGINE_REDIS_URL`.
- `tunnel-client init` writes a validated first-use profile.
- `tunnel-client doctor` validates config and explains what is missing before
  startup.
- `tunnel-client profiles samples list|show` exposes built-in sample profiles.
- `sample_mcp_enterprise_proxy` is the built-in starter for outbound proxies
  and private PKI, with env-backed proxy and CA bundle references.
- `tunnel-client admin-profiles list|set|delete` manages saved admin-key
  profiles for native runtime workflows.
- `tunnel-client runtimes create|connect|list|status|stop|rm` manages native
  alias state and local runtime supervision.
- `tunnel-client run` starts the foreground/manual client poller attached to
  the current terminal.
- `tunnel-client cloudflared version` prints the bundled companion pin,
  upstream release, and security-patch owner without starting a process.
- `tunnel-client cloudflared config --token-file <path>` prints a token-free
  production `cloudflared` config for operators who run `cloudflared` directly.
- `tunnel-client admin tunnels get <id>` is the read-only metadata lookup used
  on the runtime-user path; broader `admin tunnels` CRUD still requires an
  admin key. When you need admin CRUD scope, inspect the returned
  `organization_ids` / `workspace_ids` from `tunnel-client admin --json tunnels get <id>`
  and reuse those live values instead of guessing ids.

## License
This project is licensed under the [Apache License 2.0](LICENSE).
