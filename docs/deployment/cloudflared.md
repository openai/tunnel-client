# Bundled Cloudflare Tunnel companion

Supported release distributions include a pinned `cloudflared` executable next
to `tunnel-client`. Use this mode when a pre-provisioned, remotely managed
Cloudflare Tunnel token is available and `tunnel-client` should own the
companion process lifecycle.

## Supported distributions

The bundled companion is published for:

- Linux `amd64` and `arm64`
- macOS `amd64` and `arm64`
- Windows `amd64` and `arm64`
- Docker images for Linux `amd64` and `arm64`

Every target is built from the exact pinned upstream Go module source fetched
through an explicit proxy-only `GOPROXY`. The build verifies the pinned Go
module checksum, then compiles the module cache entry as the main module so
upstream `replace` directives remain effective. No supported path downloads
GitHub release assets, source tarballs, or direct VCS source. Windows `arm64`
uses the same source-build path as every other target.

Each platform archive contains:

- `tunnel-client` (or `tunnel-client.exe`)
- `cloudflared` (or `cloudflared.exe`)
- `cloudflared-manifest.json`
- `LICENSE`

The combined all-platform archive keeps each pair under
`bin/<os>_<arch>/`, so the two executables stay adjacent.

## Build the companion from a source checkout

Use a module proxy without a direct fallback:

```bash
GOPROXY=https://proxy.golang.org \
  ./scripts/build_cloudflared.sh \
  --goos "$(go env GOOS)" \
  --goarch "$(go env GOARCH)" \
  --output "bin/$(go env GOOS)_$(go env GOARCH)/cloudflared"
```

Replace the proxy URL with your organization's Go module proxy when one is
required. The script rejects `GOPROXY=direct`, `GOPROXY=off`, and proxy chains
that include a `direct` fallback.

## Run with a pre-provisioned token

Pass a reference instead of a literal token so the secret never lands in the
shell history or process arguments:

```bash
export CLOUDFLARED_TOKEN='...'
tunnel-client run \
  --cloudflared.token env:CLOUDFLARED_TOKEN \
  --control-plane.tunnel-id tunnel_0123456789abcdef0123456789abcdef \
  --mcp.server-url https://mcp.example.com/mcp
```

For file-backed secrets:

```bash
tunnel-client run \
  --cloudflared.token file:/run/secrets/cloudflared-token \
  --control-plane.tunnel-id tunnel_0123456789abcdef0123456789abcdef \
  --mcp.server-url https://mcp.example.com/mcp
```

`CLOUDFLARED_TUNNEL_TOKEN` is also accepted as a direct environment variable.
`--cloudflared.path` / `CLOUDFLARED_PATH` is an advanced source-build and test
override; supported release archives discover the adjacent bundled executable
without it.

Inspect the reviewed pin without starting a child process:

```bash
tunnel-client cloudflared version
```

## Generate a standalone production config

If an operator intentionally runs `cloudflared` without `tunnel-client`, print
a deterministic token-free config and store the tunnel token in a separate
secret file:

```bash
tunnel-client cloudflared config \
  --token-file /run/secrets/cloudflared/token \
  > /etc/cloudflared/config.yml
chmod 600 /run/secrets/cloudflared/token
cloudflared tunnel --config /etc/cloudflared/config.yml run
```

The generated YAML references only the token file path; it never reads or
embeds the token. It disables auto-update to preserve the reviewed pin, binds
metrics/readiness to loopback by default, keeps application logs at `info` and
transport logs at `warn`, uses automatic protocol/IP fallback, and sets
explicit retry and graceful-shutdown budgets. Override those defaults with
`tunnel-client cloudflared config --help` when the deployment requires a
different metrics listener or network policy.

## Lifecycle and readiness

When a token is configured, `tunnel-client run`:

1. launches bundled `cloudflared tunnel --no-autoupdate ... run`;
2. passes the token only through the child `TUNNEL_TOKEN` environment variable;
3. waits up to `--cloudflared.ready-timeout` (default `30s`) for the
   loopback `cloudflared` `/ready` endpoint to report an active connection;
4. keeps `/readyz` unavailable when the companion later loses readiness; and
5. stops the child on normal shutdown or exits nonzero when the child exits
   unexpectedly.

The child output is redacted before it enters tunnel-client logs. The token is
also redacted from support exports and effective config snapshots.

## Pin, provenance, and security updates

The pinned version is `2026.7.2`, from the official
[`cloudflare/cloudflared` release](https://github.com/cloudflare/cloudflared/releases/tag/2026.7.2)
at release commit `8679787525edc8575b2948a7c4a50b6292c6d426`. The checked-in
[`pkg/cloudflared/manifest.json`](../../pkg/cloudflared/manifest.json) records
the exact Go pseudo-version, package path, module checksum, `go.mod` checksum,
build timestamp, and supported platforms. `scripts/build_cloudflared.sh` uses
`go mod download` through the configured proxy-only `GOPROXY`, verifies both
checksums, and refuses to build if module metadata drifts.

The tunnel-client maintainers own routine and emergency security updates:
review each upstream release and security advisory, update the manifest,
rebuild every supported distribution, and rerun the cloudflared lifecycle and
package tests. Auto-update stays disabled so the runtime cannot silently drift
away from the reviewed pin.
