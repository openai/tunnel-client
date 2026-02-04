# Tunnel Client

The tunnel client is an enterprise-hosted agent that connects your internal MCP (Model Context Protocol) server to OpenAI-hosted products over a secure, outbound-only HTTPS channel.

## Documentation

- **Start here**: [`docs/onboarding.md`](docs/onboarding.md)
- **Architecture**: [`docs/architecture.md`](docs/architecture.md)
- **Configuration reference**: [`docs/configuration.md`](docs/configuration.md)
- **Deployment guides**: [`docs/deployment/overview.md`](docs/deployment/overview.md)
- **Troubleshooting**: [`docs/troubleshooting.md`](docs/troubleshooting.md)
- **Development & testing**: [`docs/development.md`](docs/development.md)
- **Roadmap / design notes**: [`docs/roadmap.md`](docs/roadmap.md)

## What it does (high level)

- The client **long-polls** the OpenAI tunnel control plane over HTTPS:
  - `GET /v1/tunnel/{tunnel_id}/poll`
  - `POST /v1/tunnel/{tunnel_id}/response`
- On startup, it fetches tunnel metadata for operator visibility:
  - `GET /v1/tunnels/{tunnel_id}`
- It forwards the received JSON-RPC requests to your configured MCP server over HTTP(S).
- It routes commands by channel: `main` always targets the configured MCP server, while `harpoon` is enabled only when Harpoon has registered targets.
- On startup, it fetches OAuth ProtectedResourceMetaData from the MCP server for diagnostics.
- It exposes an **admin/health server** (`/healthz`, `/readyz`, `/metrics`) and a lightweight **admin UI** (`/ui`) for operational status.
- The admin UI Overview reports channel availability and reasons when channels are disabled.
- It embeds the **harpoon MCP server** to provide a labeled, allowlisted outbound HTTP client for internal tooling.

## CLI

- `tunnel-client` shows help and available subcommands.
- `tunnel-client run` starts the client poller.

## License
This project is licensed under the [Apache License 2.0](LICENSE).
