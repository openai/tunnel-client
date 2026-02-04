# Troubleshooting

## Common startup errors

- **“control plane API key is required”**
  - Set `CONTROL_PLANE_API_KEY`, or set `OPENAI_API_KEY` (fallback), or use `--control-plane.api-key=env:.../file:...`.

- **“tunnel ID is required”**
  - Set `CONTROL_PLANE_TUNNEL_ID` or `--control-plane.tunnel-id=...`.

- **“MCP server URL is required”**
  - Set `MCP_SERVER_URL` or `--mcp.server-url=...`.

## Unexpected URLs / 404s

- Ensure `CONTROL_PLANE_BASE_URL` is the host root (e.g. `https://api.openai.com`) and not a pre-prefixed path.

## Auth failures (401/403)

- Confirm the API key is correct and permitted to access the tunnel control plane.
- If using `--control-plane.api-key=env:VARNAME`, ensure that env var is set and non-empty.

## Client never becomes “healthy/ready”

- `/healthz` and `/readyz` report basic process status. If the process is running but not making progress, check logs for:
  - control-plane connectivity errors
  - MCP server connectivity errors

## MCP connectivity issues

- Verify `MCP_SERVER_URL` is reachable from where `tunnel-client` runs.
- If your MCP server uses a private CA, ensure the OS/container trust store includes it.
- Consider temporarily enabling `--log.http-raw-unsafe` (and `--log.level=debug`) in a controlled environment to debug handshake issues.

## Harpoon channel disabled (`unsupported_channel`)

- `harpoon` commands return `unsupported_channel` when there are no registered Harpoon targets.
- Confirm you have configured at least one `--harpoon-target` (or `HARPOON_TARGETS`) entry and that it passes validation.

## Performance / backlog

- Increase `--control-plane.max-inflight` / `CONTROL_PLANE_MAX_INFLIGHT_REQUESTS` to buffer more commands.
- Increase `--mcp.max-concurrent-requests` / `MCP_MAX_CONCURRENT_REQUESTS` if your MCP server can handle parallelism.
