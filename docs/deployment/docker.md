# Docker deployment

## Build image

```bash
DOCKER_BUILDKIT=1 docker build \
  --build-arg GIT_SHA="$(git rev-parse HEAD)" \
  -t tunnel-client:latest \
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
  -p 8080:8080 \
  tunnel-client:latest
```

## Notes

- Prefer mounting secrets as files and using `--control-plane.api-key=file:/...` if you avoid environment variables.
- `HEALTH_LISTEN_ADDR=:0` explicitly asks the OS to assign an ephemeral port. Set `HEALTH_URL_FILE` so probes can discover the resolved base URL.
- If you set `PID_FILE`, the client writes its process ID to that path on startup and removes it on shutdown (useful for supervisors).
