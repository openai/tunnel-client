# Development & Testing

This document is for contributors working on `tunnel-client`.

## Build

```bash
./scripts/build_admin_ui.sh ./adminui ./pkg/adminui/assets
# or
make admin-ui
go build ./...
go build -o bin/tunnel-client ./cmd/client
```

Use `./bin/tunnel-client` for local source-checkout runs unless `bin/` is on
your `PATH`.

Before creating a release tag, stamp the source version so downloaded release
archives build with the tag semantic version:

```bash
make release-source-version VERSION=1.2.3
make release-tag VERSION=1.2.3
```

Homebrew Formula tooling is available for deterministic rendering and explicit
test publishes, but production tap publication is not wired into the release
workflow yet. Add that integration only after the tap bootstrap and scoped
GitHub App credential path are provisioned. Do not document
`brew install openai/tools/tunnel-client` as supported before a compatible
stable Formula is present in the tap.

Exercise the deterministic renderer and tap-PR publisher with:

```bash
bash ./scripts/generate_homebrew_formula_test.sh
bash ./scripts/publish_homebrew_formula_test.sh
```

The standalone `Homebrew formula smoke` workflow downloads checksums from an
already-published release, renders the Formula, and runs `brew readall`,
`brew install`, and `brew test` without uploading artifacts, creating a
release, or minting a tap write token. For an explicit remote draft publish,
run `publish_homebrew_formula.sh --draft` with human `gh` auth and a
`test-tunnel-client-*` branch. `--allow-prerelease` exists only for these
test paths; normal stable publication continues to reject prereleases.

## Unit tests

```bash
go test ./...
```

The wire-contract tests treat [`openapi.json`](openapi.json) as executable
documentation:

```bash
go test ./pkg/controlplane/wiretypes ./pkg/controlplane/internal
```

They validate the documented endpoint methods, OpenAPI examples, command
discriminators, and serialized response payloads against the published schema.

## E2E tests (in-repo harness)

The `e2e/` tests use in-repo test doubles under `testsupport/`:

- `testsupport/mocktunnelservice`: simulates the control plane poll/response endpoints.
- `testsupport/mockmcpserver`: a Streamable HTTP MCP server double.

Run:

```bash
go test ./e2e -count=1
```

## MCP tunnel proxy test patterns

There are two supported wrapper patterns for tests that start an MCP server and
need tunnel-client in the path:

- Remote control plane: start your MCP server, then start `tunnel-client run`
  with `CONTROL_PLANE_API_KEY`, `--control-plane.tunnel-id`, and
  `--mcp-server-url` or `--mcp-command`. Use this when a test should exercise a
  hosted control plane.
- Local control plane: start your MCP server, then start
  `tunnel-client dev proxy --mcp-server-url <url> --print-json`. This runs a
  local control plane plus tunnel-client in one process and prints connection
  JSON that tests can use for JSON-RPC requests.

`dev proxy` runs the local control plane and tunnel-client in one process. It
prefers a Unix-domain socket for tunnel-client control-plane traffic when the OS
supports it and falls back to TCP otherwise. It starts no health/admin listener
by default; pass `--health-listen-addr 127.0.0.1:0` or
`--health-url-file <path>` only when a test needs `/healthz`, `/readyz`,
`/metrics`, or `/ui`. The `--backend auto|go|rust` flag defaults to `auto`, and
`--engine-queue-backend inmem|redis` defaults to `inmem`. Public builds use the
Go backend unless an optional Rust backend adapter is linked into the binary;
explicit `--backend rust` fails clearly when unavailable. Redis selects the
linked Rust backend for `auto`, rejects `go`, and requires
`--engine-redis-url <url>` or `TUNNEL_ENGINE_REDIS_URL`.

External MCP ingress is TCP by default: `--listen` defaults to `127.0.0.1:0`,
`mcp_transport` is `tcp`, and `mcp_url` remains populated. Pass
`--listen-unix-socket <path>` instead for Unix ingress; it is mutually
exclusive with `--listen`, and the JSON contains `mcp_transport: "unix"`,
`mcp_unix_socket`, and `mcp_url_path`. This external socket is separate from
the temporary Unix socket used by tunnel-client for internal control-plane
traffic.

Stable touch points:

- Go tests can import `github.com/openai/tunnel-client/pkg/localproxy` and call
  `localproxy.Start`.
- Python tests can copy or import
  `wrappers/mcp-tunnel-client-proxy/python/mcp_tunnel_client_proxy.py`.
- TypeScript tests can copy or import
  `wrappers/mcp-tunnel-client-proxy/typescript/mcp_tunnel_client_proxy.ts`.
- Copyable example subprojects live under `examples/`.

## Repo structure (high level)

- `cmd/client`: CLI entrypoint
- `pkg/*`: implementation packages
- `e2e/`: end-to-end tests using in-repo mocks
- `testsupport/`: test helpers and doubles
