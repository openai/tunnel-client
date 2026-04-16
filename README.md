# Tunnel Client

The tunnel client is an enterprise-hosted agent that connects a private MCP
(Model Context Protocol) server to OpenAI-hosted products over a secure,
outbound-only HTTPS channel. It lets customers keep MCP servers inside their own
network while OpenAI products use an OpenAI-hosted MCP tunnel URL.

## Documentation

- **Start here**: [`docs/onboarding.md`](docs/onboarding.md)
- **Architecture diagrams**: [`docs/architecture.md`](docs/architecture.md)
- **Enterprise customer handoff**:
  [`docs/enterprise-customer-onboarding.md`](docs/enterprise-customer-onboarding.md)
- **Configuration reference**: [`docs/configuration.md`](docs/configuration.md)
- **Deployment guides**: [`docs/deployment/overview.md`](docs/deployment/overview.md)
- **Troubleshooting**: [`docs/troubleshooting.md`](docs/troubleshooting.md)
- **Development & testing**: [`docs/development.md`](docs/development.md)
- **Roadmap / design notes**: [`docs/roadmap.md`](docs/roadmap.md)

## What it does

- The client **long-polls** the OpenAI tunnel control plane over HTTPS:
  - `GET /v1/tunnel/{tunnel_id}/poll`
  - `POST /v1/tunnel/{tunnel_id}/response`
- On startup, it fetches tunnel metadata for operator visibility:
  - `GET /v1/tunnels/{tunnel_id}`
- It forwards received JSON-RPC requests to your configured MCP server over
  HTTP(S), stdio, or in-memory transport.
- It routes commands by channel: `main` always targets the configured MCP
  server, while `harpoon` is enabled only when Harpoon has registered targets.
- On startup, it fetches OAuth Protected Resource Metadata from the MCP server
  for diagnostics.
- For OAuth auth-server handling, `authorization_servers[0]` from PRMD is the
  only source of truth and metadata fetch target.
- Metadata is accepted even when `issuer` differs from
  `authorization_servers[0]` (external IdP issuer URLs are supported), with
  mismatch diagnostics preserved in logs/state.
- It exposes an **admin/health server** (`/healthz`, `/readyz`, `/metrics`) and
  a lightweight **admin UI** (`/ui`) for operational status.
- The admin UI Overview reports channel availability and reasons when channels
  are disabled.
- The admin UI log export returns a redacted support bundle with recent logs
  plus a point-in-time Prometheus snapshot from `/metrics`.
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
- `tunnel-client run` starts the client poller.

## License
This project is licensed under the [Apache License 2.0](LICENSE).
