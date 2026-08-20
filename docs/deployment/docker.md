# Docker deployment

## Use the published image

Release tags publish multi-architecture images for Linux `amd64` and `arm64`
at `ghcr.io/openai/tunnel-client`.

For a stable release such as `v1.2.3`, the workflow publishes these tags:

- `v1.2.3` and `1.2.3` for the exact release
- `1.2` and `latest` as moving stable aliases
- `sha-<full-commit-sha>` for the exact release commit

Prereleases publish only their exact version tags and commit SHA; they never
move `1.2` or `latest`. Pin an exact version or digest for production:

```bash
docker pull ghcr.io/openai/tunnel-client:v1.2.3
```

Published images include OCI labels, an SBOM attestation, and signed GitHub
artifact provenance. Verify provenance for an exact tag with:

```bash
gh attestation verify \
  oci://ghcr.io/openai/tunnel-client:v1.2.3 \
  -R openai/tunnel-client
```

## Build from a source checkout

```bash
DOCKER_BUILDKIT=1 docker build \
  --build-arg GIT_SHA="$(git rev-parse HEAD)" \
  --build-arg PNPM_PACKAGE_MANAGER="$(sed -n 's/^[[:space:]]*"packageManager"[[:space:]]*:[[:space:]]*"\([^"]*\)".*/\1/p' "$(git rev-parse --show-toplevel)/package.json")" \
  --build-arg GOPROXY=https://proxy.golang.org \
  -t tunnel-client:local \
  -f Dockerfile .
```

## Run container

```bash
docker run --rm \
  -e CONTROL_PLANE_API_KEY="sk-..." \
  -e CONTROL_PLANE_TUNNEL_ID="tunnel_0123456789abcdef0123456789abcdef" \
  -e MCP_SERVER_URL="https://mcp.internal.example.com/mcp" \
  -e LOG_LEVEL="info" \
  -e LOG_FORMAT="json" \
  -e HEALTH_LISTEN_ADDR=":8080" \
  -p 8080:8080 \
  ghcr.io/openai/tunnel-client:v1.2.3
```

## Docker Compose

```yaml
services:
  tunnel-client:
    image: ghcr.io/openai/tunnel-client:v1.2.3
    restart: unless-stopped
    environment:
      CONTROL_PLANE_API_KEY: ${CONTROL_PLANE_API_KEY}
      CONTROL_PLANE_TUNNEL_ID: ${CONTROL_PLANE_TUNNEL_ID}
      MCP_SERVER_URL: https://mcp.internal.example.com/mcp
      LOG_LEVEL: info
      LOG_FORMAT: json
      HEALTH_LISTEN_ADDR: :8080
    ports:
      - "8080:8080"
```

## Notes

- Replace the example `v1.2.3` tag with a released version or digest. Use
  `latest` only when automatically following stable releases is intentional.
- Prefer mounting secrets as files and using `--control-plane.api-key=file:/...` if you avoid environment variables.
- `GOPROXY` must name a Go module proxy without a `direct` fallback. Replace
  the example value with your organization's module proxy when required; the
  image build never downloads the bundled `cloudflared` source directly from
  GitHub.
- The default health listener is `127.0.0.1:8080`. Set `HEALTH_LISTEN_ADDR=:8080`
  only when publishing the container health port to a trusted network.
- `HEALTH_LISTEN_ADDR=:0` explicitly asks the OS to assign an ephemeral port. Set `HEALTH_URL_FILE` so probes can discover the resolved base URL.
- If you set `PID_FILE`, the client writes its process ID to that path on startup and removes it on shutdown (useful for supervisors).
