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

## Install with Homebrew

Install `tunnel-client` from the official OpenAI tap:

```bash
brew install openai/tools/tunnel-client
```

Verify the installed version, then start with the guided setup:

```bash
tunnel-client --version
tunnel-client help quickstart
```

The Formula installs the matching `tunnel-client`, bundled `cloudflared`,
and companion manifest together, while exposing only the `tunnel-client`
command. For Docker, Kubernetes, or VM deployments, see
[`docs/deployment/overview.md`](docs/deployment/overview.md).

To generate the shareable guide output locally:

```bash
make end-user-guide-screenshots
make end-user-guide-html
make end-user-guide-slides
```

## For Codex / Copilot

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

## SBOMs

The public repository includes deterministic six-platform dependency baselines
for the
[full client](https://github.com/openai/tunnel-client/blob/master/compliance/tunnel-client.spdx.json),
[runtime](https://github.com/openai/tunnel-client/blob/master/compliance/tunnel-client-runtime.spdx.json),
and
[runtime with Cloudflared](https://github.com/openai/tunnel-client/blob/master/compliance/tunnel-client-runtime-cloudflared.spdx.json).
They inventory synthetic payloads built from declared offline source and vendor
snapshots, including pinned Cloudflared module versions, purls, and CPEs. Do
not hand-edit them; maintainers refresh them through the hermetic SBOM
generation check when dependency inputs or a Cloudflared pin changes. The
baselines are useful for dependency review and drift detection, but they do not
claim that a public release ZIP contains the same bytes.

From a checkout of this public repository, verify that the mirrored baseline
files match their mirrored manifest before importing them into a dependency
scanner:

```bash
./scripts/verify_sbom_baselines.sh
```

That command proves the checkout's three baseline documents match
`compliance/sbom-baseline-manifest.json` and parse as SPDX 2.3. It does not
prove that any release archive contains those bytes.

### Validate a downloaded release

Releases produced by the current release workflow publish a matching SPDX 2.3
sidecar for each ZIP, embed the same sidecar in the ZIP, cover both files in
`SHA256SUMS.txt`, and publish the workflow's signed Sigstore provenance bundle.
They also publish:

- `tunnel-client-vX.Y.Z-vulnerability-report.json`, generated by a
  checksum-pinned Grype binary from exactly 18 release-specific SPDX sidecars:
  three flavors across six platforms, each bound to its matching ZIP and
  license-report SHA256, with the exact vulnerability-database SHA256 recorded;
- `tunnel-client-vX.Y.Z.openvex.json` only when the scan has findings, with
  every automated statement set to `under_investigation` rather than claiming
  that a finding is fixed, exploitable, or not affected; and
- `tunnel-client-vX.Y.Z-enterprise-evidence.json`, which inventories the
  release evidence bytes, scanner/database identity, scan scope, and
  provenance boundary.

The vulnerability report explicitly records OCI images as `not_scanned`:
the release contract has multi-architecture OCI index digests, not exact
per-platform OCI manifest scan inputs. Every pre-bundle release artifact is
covered by `SHA256SUMS.txt`, `PUBLIC_URLS.txt`, and the signed provenance
bundle. The bundle is intentionally excluded from its own checksum and
signature subject set.
Older releases without `.spdx.json` sidecars cannot be archive/SBOM-validated
this way; releases without a `*-provenance.sigstore.json` bundle cannot be
provenance-validated this way. Use the release sidecar, not a checked-in
baseline, to validate downloaded bytes.

From a checkout of this public repository at the matching release tag, replace
the example tag and choose one of `client`, `runtime`, or
`runtime-cloudflared`. The commands require Bash, Python 3, `curl`, and the
GitHub CLI:

```bash
release=vX.Y.Z
platform=linux-amd64
flavor=runtime
prefix=tunnel-client-runtime
stem="${prefix}-${release}-${platform}"
base="https://github.com/openai/tunnel-client/releases/download/${release}"
bundle="tunnel-client-${release}-provenance.sigstore.json"
source_digest="$(git rev-parse HEAD)"

curl -fLO "${base}/${stem}.zip"
curl -fLO "${base}/${stem}.spdx.json"
curl -fLO "${base}/${stem}-licenses.txt"
curl -fLO "${base}/SHA256SUMS.txt"
curl -fLO "${base}/${bundle}"

gh attestation verify "${stem}.zip" \
  --bundle "${bundle}" \
  --repo openai/tunnel-client \
  --signer-workflow openai/tunnel-client/.github/workflows/release.yml \
  --source-ref "refs/tags/${release}" \
  --source-digest "${source_digest}" \
  --signer-digest "${source_digest}" \
  --predicate-type https://slsa.dev/provenance/v1 \
  --deny-self-hosted-runners
gh attestation verify "${stem}.spdx.json" \
  --bundle "${bundle}" \
  --repo openai/tunnel-client \
  --signer-workflow openai/tunnel-client/.github/workflows/release.yml \
  --source-ref "refs/tags/${release}" \
  --source-digest "${source_digest}" \
  --signer-digest "${source_digest}" \
  --predicate-type https://slsa.dev/provenance/v1 \
  --deny-self-hosted-runners
gh attestation verify "${stem}-licenses.txt" \
  --bundle "${bundle}" \
  --repo openai/tunnel-client \
  --signer-workflow openai/tunnel-client/.github/workflows/release.yml \
  --source-ref "refs/tags/${release}" \
  --source-digest "${source_digest}" \
  --signer-digest "${source_digest}" \
  --predicate-type https://slsa.dev/provenance/v1 \
  --deny-self-hosted-runners
gh attestation verify SHA256SUMS.txt \
  --bundle "${bundle}" \
  --repo openai/tunnel-client \
  --signer-workflow openai/tunnel-client/.github/workflows/release.yml \
  --source-ref "refs/tags/${release}" \
  --source-digest "${source_digest}" \
  --signer-digest "${source_digest}" \
  --predicate-type https://slsa.dev/provenance/v1 \
  --deny-self-hosted-runners

./scripts/verify_release_archive.sh \
  --flavor "${flavor}" \
  --archive "${stem}.zip" \
  --sbom "${stem}.spdx.json" \
  --checksums SHA256SUMS.txt
```

To verify the complete release evidence set, download every asset into a clean
directory and run both fail-closed contract verifiers:

```bash
mkdir release-evidence
gh release download "${release}" \
  --repo openai/tunnel-client \
  --dir release-evidence

./scripts/verify_release_provenance.sh \
  --bundle "release-evidence/${bundle}" \
  --artifact-dir release-evidence \
  --release "${release}" \
  --source-digest "${source_digest}"
./scripts/verify_release_evidence.sh \
  --artifact-dir release-evidence \
  --release "${release}" \
  --source-digest "${source_digest}"
```

Use `prefix=tunnel-client` with `flavor=client`, or
`prefix=tunnel-client-runtime-cloudflared` with
`flavor=runtime-cloudflared`. The bundle is the release workflow's signed
Sigstore evidence and supports verification without GitHub attestation lookup.
It is emitted after `SHA256SUMS.txt` is attested, so it is intentionally not
listed in that checksum file; `gh attestation verify` checks the bundle's
signature, transparency-log material, signer workflow, source ref, source
digest, and subject digest. The archive verifier then fails closed when the
published checksums do not match, the ZIP is too large or has unsafe,
duplicate, or non-regular members, its embedded sidecar differs from the
downloaded sidecar, or the SPDX SHA256 inventory does not match the extracted
payload. After it passes, import the matching `.spdx.json` file into the
dependency scanner of your choice. Runtime releases also publish a matching
`*-scan-manifest.json` that binds scanner scope, source archives, license
evidence, and the release sidecars.

For a fully disconnected verification environment, capture a trusted root from
an independently trusted online environment before disconnecting:

```sh
gh attestation trusted-root > trusted_root.jsonl
```

Transfer that file with the release evidence and add
`--custom-trusted-root trusted_root.jsonl` to each `gh attestation verify`
command above. The release bundle removes the GitHub attestation API
dependency; the trusted-root file removes the remaining online trust-root
lookup.

Build the CLI binary from a source checkout. The Make target stamps the
checkout Git SHA into the version sent in `User-Agent` and
`X-Tunnel-Client-Version`:

```bash
make admin-ui
make tunnel-client
./bin/tunnel-client help quickstart
```

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

## Narrow runtime artifacts

`tunnel-client-runtime` and `tunnel-client-runtime-cloudflared` are the
runtime-only customer surfaces. They intentionally expose only `run` plus
flag-based `--help` and `--version`; use the full `tunnel-client` binary for
onboarding, admin, Codex, and profile-management commands. The Cloudflare
flavor adds only the approved `cloudflared.*` settings and supervises a pinned
`cloudflared` companion.

Build either binary from a source checkout with its Make target (the shorter
aliases are equivalent):

```bash
make tunnel-client-runtime              # alias: make runtime
make tunnel-client-runtime-cloudflared  # alias: make runtime-cloudflared
```

The targets write platform-specific binaries under `bin/<goos>_<goarch>/` and
stable paths at `bin/tunnel-client-runtime` and
`bin/tunnel-client-runtime-cloudflared` (`.exe` on Windows). Inspect the exact
runtime flags with `./bin/tunnel-client-runtime run --help` or
`./bin/tunnel-client-runtime-cloudflared run --help`.

Run the narrow runtime against an HTTP MCP server:

```bash
export CONTROL_PLANE_API_KEY='...'
export CONTROL_PLANE_TUNNEL_ID='tunnel_0123456789abcdef0123456789abcdef'
export MCP_SERVER_URL='https://mcp.example.com/mcp'
./bin/tunnel-client-runtime run
```

For managed Cloudflare provisioning, use the Cloudflare flavor. Release
archives place the pinned `cloudflared` executable beside the runtime; for a
source-only build, point to an existing companion explicitly:

```bash
./bin/tunnel-client-runtime-cloudflared run \
  --cloudflared.managed \
  --cloudflared.path /path/to/cloudflared
```

To build the corresponding Linux images, use `make build-image-runtime` and
`make build-image-runtime-cloudflared`; the Cloudflare image includes its pinned
companion and both images use `run` as their entrypoint.

Contributors can run the compatibility suite with `make test-runtime`. Its
host-binary checks compare the full client and runtime flavors with the same
profile bytes, environment, flags, local control-plane/MCP/OAuth/proxy/TLS
fixtures, and shutdown signal. The same target also packages native
release-shaped ZIPs and checks that they verify, extract, identify the expected
flavor, and expose `run --help`; that ZIP smoke is not a second fake-service
parity run.

After building the runtime images, `make runtime-container-compatibility`
runs a deployment smoke for their default and overridden entrypoints with
read-only profile/Secret mounts and hardened container settings. It uses
intentionally unreachable local endpoints to check startup surfaces and
SIGTERM, not to compare image behavior with the full client or assert
`/readyz` readiness. An optional local Kubernetes deployment smoke is
available with `TUNNEL_CLIENT_RUNTIME_K8S_COMPAT=1 make
runtime-k8s-compatibility`; it requires Docker plus `kind` or `k3d`, checks
the runtime Pods' mounted profile/Secret and `/healthz` surface, and does not
contact external services.

Public releases use plain semantic-version tags such as `v0.0.10`. Source
archives from release tags carry the release version in
`pkg/version/VERSION`. A plain `go build` from a downloaded release `.tar.gz`
therefore reports the tag semantic version through `tunnel-client --version`,
`User-Agent`, and the explicit control-plane version headers. Source-checkout
builds made with the Make target or explicit linker flag above append the Git
SHA to that semantic version.

Supported release archives also bundle pinned `cloudflared` `2026.8.2` beside
the CLI for Linux `amd64`/`arm64`, macOS `amd64`/`arm64`, and Windows
`amd64`/`arm64`.
Official release images are published at `ghcr.io/openai/tunnel-client` for
Linux `amd64` and `arm64`; they bundle the matching companion. Pin an exact
`vX.Y.Z` tag or digest for production. Stable releases also update the
`X.Y` and `latest` aliases; prereleases do not. For a logical tunnel created
with managed Cloudflare provisioning, let the authenticated client fetch the
runtime token and start the companion without distributing a static token:

```bash
tunnel-client run \
  --cloudflared.managed \
  --control-plane.tunnel-id tunnel_0123456789abcdef0123456789abcdef \
  --mcp.server-url https://mcp.example.com/mcp
```

For a pre-provisioned Cloudflare tunnel, a static token remains available as an
explicit override and is never put in argv:

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
TUNNEL_MANAGEMENT_DIAGNOSTICS=false \
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
- For sidecar deployments whose local MCP listener may bind after
  `tunnel-client` starts, the optional `MCP_STARTUP_WAIT_TIMEOUT` gate delays
  the first control-plane poll and OAuth discovery until the main MCP listener
  is reachable.
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
- Control-plane polls routed through an HTTP proxy start at the configured
  `--control-plane.poll-timeout` / `CONTROL_PLANE_POLL_TIMEOUT`. If a proxied
  poll loses its connection before response headers with an EOF-style error while
  neither deadline has fired, tunnel-client automatically learns a shorter
  process-local timeout for future polls. The learned timeout only decreases,
  never below 5 seconds, while the configured poll timeout remains its ceiling.
  Direct routes and Unix sockets keep the configured timeout.
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
