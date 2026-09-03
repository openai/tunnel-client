# Development & Testing

This document is for contributors working on `tunnel-client`.

## Build

```bash
./scripts/build_admin_ui.sh ./adminui ./pkg/adminui/assets
# or
make admin-ui
go build ./...
make tunnel-client
```

Use `./bin/tunnel-client` for local source-checkout runs unless `bin/` is on
your `PATH`. The Make target stamps the checkout Git SHA into the version sent
in `User-Agent` and `X-Tunnel-Client-Version`.

If you invoke Go directly, stamp the same metadata explicitly:

```bash
module_path="$(go list -m -f '{{.Path}}')"
git_sha="$(git rev-parse HEAD)"
mkdir -p bin

go build \
  -ldflags "-X ${module_path}/pkg/version.GitSHA=${git_sha}" \
  -o bin/tunnel-client \
  ./cmd/client
```

Before creating a release tag, stamp the source version so downloaded release
archives build with the tag semantic version:

```bash
make release-source-version VERSION=1.2.3
make release-tag VERSION=1.2.3
```

The release workflow validates the multi-architecture image on `master` and
publishes it to `ghcr.io/openai/tunnel-client` for Linux `amd64` and `arm64`
from release tags. Stable `v1.2.3` tags publish `v1.2.3`, `1.2.3`, `1.2`,
`latest`, and `sha-<full-commit-sha>`; prereleases publish only their exact
version tags and commit SHA. Semver build metadata (`+...`) is rejected
because Docker tags cannot represent it. The image build uses Buildx cache,
emits an SBOM attestation, and records signed GitHub artifact provenance.

The supported Homebrew installation path is documented in
[`../README.md#install-with-homebrew`](../README.md#install-with-homebrew).

Exercise the deterministic Formula renderer with:

```bash
bash ./scripts/generate_homebrew_formula_test.sh
```

The standalone `Homebrew formula smoke` workflow downloads checksums from an
already-published release, renders the Formula, and runs `brew readall`,
`brew install`, `brew test`, and the documented `tunnel-client --version`
and `tunnel-client help quickstart` startup commands on an arm64 macOS runner
without uploading artifacts or creating a release. `--allow-prerelease`
exists only for explicit test paths; stable Formula generation continues to
reject prereleases.

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

## Single-client local load benchmark

The E2E package also contains an opt-in benchmark that starts one real
`tunnel-client` runtime, a loopback local control plane, and a lightweight
local MCP server. It warms up one MCP session, then measures concurrent
`tools/call` requests through the full local ingress, polling, dispatch, MCP,
and response path:

```bash
TUNNEL_CLIENT_LOAD_WORKERS=64 \
TUNNEL_CLIENT_LOAD_MAX_INFLIGHT=256 \
TUNNEL_CLIENT_LOAD_MCP_CONCURRENCY=64 \
TUNNEL_CLIENT_LOAD_PAYLOAD_BYTES=1024 \
go test ./e2e -run '^$' -bench '^BenchmarkSingleTunnelClient$' -benchtime=30s -count=1 -benchmem
```

The benchmark reports requests per second and p50/p95/p99/max request latency,
along with the worker, in-flight queue, MCP concurrency, payload, transport,
and Go runtime settings used for the run. Adjust these optional environment
variables to explore a laptop's capacity:

- `TUNNEL_CLIENT_LOAD_WORKERS` controls concurrent caller requests (default `64`).
- `TUNNEL_CLIENT_LOAD_MAX_INFLIGHT` controls the tunnel-client queue capacity
  (default `256`, maximum `10000`).
- `TUNNEL_CLIENT_LOAD_MCP_CONCURRENCY` controls active MCP dispatch workers
  (default `64`).
- `TUNNEL_CLIENT_LOAD_PAYLOAD_BYTES` controls the argument payload size
  (default `1024`).
- `TUNNEL_CLIENT_LOAD_REQUEST_TIMEOUT` controls each request timeout
  (default `30s`).

Normal `go test` runs only a tiny smoke test for this harness; it does not run
the benchmark unless `-bench` selects it. Results are a loopback local
upper-bound that includes the local control plane and MCP server, not hosted
tunnel-service capacity.

The host-binary runtime replacement checks use the same in-repo fake services
while launching the real full-client, runtime, and runtime-cloudflared
binaries:

```bash
make test-runtime
```

That target includes shared flag/config/profile/environment parity, real
host-binary behavior comparisons, and native release-ZIP execution smoke. The
ZIP portion verifies packaging, extraction, flavor/version output, and
`run --help`; it does not run the extracted ZIP binary against the fake
services.

To smoke-test already-built runtime images with read-only
ConfigMap/Secret-style mounts, non-root execution, and both default and
explicit entrypoints, run:

```bash
make build-image-runtime build-image-runtime-cloudflared
make runtime-container-compatibility
```

The Kubernetes deployment smoke is deliberately opt-in because it needs a
local Docker daemon and `kind` or `k3d`:

```bash
TUNNEL_CLIENT_RUNTIME_K8S_COMPAT=1 make runtime-k8s-compatibility
```

Only the host-binary parity lane uses the in-repo fake services. The ZIP,
container, and Kubernetes lanes are packaging/deployment smokes; the
container and Kubernetes profiles intentionally use unreachable local
endpoints, so those lanes do not establish full-client behavioral parity or
`/readyz` readiness. None of the lanes contacts external services.

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
