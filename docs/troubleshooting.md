# Troubleshooting

## Common startup errors

- **"control plane API key is required"**
  - Set `CONTROL_PLANE_API_KEY`, set `OPENAI_API_KEY` as a fallback, or use
    `--control-plane.api-key=env:.../file:...`.

- **"tunnel ID is required"**
  - Set `CONTROL_PLANE_TUNNEL_ID` or `--control-plane.tunnel-id=...`.

- **"invalid tunnel ID ... must match tunnel_<32 lowercase letters or digits>"**
  - Use a tunnel ID shaped like `tunnel_0123456789abcdef0123456789abcdef`.

- **"MCP server URL is required"**
  - Set `MCP_SERVER_URL` or `--mcp.server-url=...`.

## Unexpected URLs / 404s

- Ensure `CONTROL_PLANE_BASE_URL` is the host root (for example
  `https://api.openai.com`) and not a pre-prefixed path.

## Auth failures (401/403)

- Confirm the API key is correct and permitted to access the tunnel control plane.
- If using `--control-plane.api-key=env:VARNAME`, ensure that env var is set
  and non-empty.

## Client never becomes "healthy/ready"

- `/healthz` is liveness only. A `200 live` response means the process is up.
- `/readyz` includes startup gating:
  - `503 oauth discovery pending` while OAuth discovery is still in flight.
  - `503 oauth discovery failed: ...` when required OAuth discovery fails.
  - `503 mcp probe failed: ...` when the MCP startup probe fails.
  - `200 ready (mcp initialize requires auth: ...)` when the MCP endpoint is
    reachable but requires auth during `initialize`.
  - `200 ready (mcp startup probe timed out: ...)` when the probe times out but
    startup should continue.
- If the process is live but `/readyz` stays non-`200`, check logs for:
  - OAuth discovery failures
  - control-plane connectivity errors
  - MCP server connectivity errors

## Export recent logs

- The admin UI logs panel can download a redacted support archive from
  `/api/logs/export?minutes=30`.
- To save the same archive into the current working directory without using a browser:

```bash
curl -fsSJO "http://127.0.0.1:8080/api/logs/export?minutes=30"
```

- To capture one archive every five minutes until stopped:

```bash
while :; do
  curl -fsSJO "http://127.0.0.1:8080/api/logs/export?minutes=30"
  sleep 300
done
```

- The archive contains `manifest.json`, `README.txt`,
  `tunnel-client.logs.ndjson`, `tunnel-client.metrics.prom`,
  `admin/status.json`, `admin/system.json`, and `admin/oauth.json`.
- `tunnel-client.metrics.prom` is a point-in-time Prometheus text snapshot
  captured from `/metrics` at export time.
- The `admin/*.json` files are point-in-time copies of `/api/status`,
  `/api/system`, and `/api/oauth` at export time, so support can review the
  configured `tunnel_id`, route state, probe status, and OAuth discovery state
  alongside the log stream.
- The archive is redacted before it is returned.

## MCP connectivity issues

- Verify `MCP_SERVER_URL` is reachable from where `tunnel-client` runs.
- If your MCP server uses a private CA, ensure the OS/container trust store includes it.
- Consider temporarily enabling `--log.http-raw-unsafe` and
  `--log.level=debug` in a controlled environment to debug handshake issues.

## Harpoon channel disabled (`unsupported_channel`)

- `harpoon` commands return `unsupported_channel` when there are no registered
  Harpoon targets.
- Confirm you have configured at least one `--harpoon.target` or
  `HARPOON_TARGETS` entry and that it passes validation.

## Performance / backlog

- Increase `--control-plane.max-inflight` /
  `CONTROL_PLANE_MAX_INFLIGHT_REQUESTS` to buffer more commands.
- Increase `--mcp.max-concurrent-requests` / `MCP_MAX_CONCURRENT_REQUESTS` if
  your MCP server can handle parallelism.
