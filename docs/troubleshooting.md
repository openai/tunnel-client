# Troubleshooting

## Common startup errors

- **"control plane API key is required"**
  - Set `CONTROL_PLANE_API_KEY`, set `OPENAI_API_KEY` as a fallback, or use
    `--control-plane.api-key=env:.../file:...`.

- **"tunnel ID is required"**
  - Set `CONTROL_PLANE_TUNNEL_ID` or `--control-plane.tunnel-id=...`.

- **"invalid tunnel ID ... must match tunnel_<32 lowercase hexadecimal characters>"**
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

## Debug why `/readyz` is failing

If you are debugging why `/readyz` is failing or why the client never becomes
"healthy/ready", start here:

- `tunnel-client health --url-file /tmp/tunnel-client-health.url` is the
  fastest structured probe when you already have a health URL file from
  `tunnel-client run`.
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
- `tunnel-client health --port 8080` is the quickest loopback check when the
  daemon is bound to the default port.

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

## Connector setup and runtime pitfalls

- **ChatGPT connector setup cannot discover tools**
  - Keep `tunnel-client run ...` running while creating or testing the connector.
    The remote tunnel object can exist even when no local runtime is polling it.
  - Confirm the connector selected the same `CONTROL_PLANE_TUNNEL_ID` that the
    daemon is using.
  - Check `/readyz`, not only `/healthz`; liveness does not prove MCP probing or
    OAuth discovery finished.

- **Connector URL returns 404 or does not stream on GET**
  - Connector MCP traffic is POST-based JSON-RPC. GET requests to `/v1/mcp/...`
    are not a diagnostic SSE stream.
  - If client logs show doubled paths, set `CONTROL_PLANE_BASE_URL` to the host
    root, for example `https://api.openai.com`, not a `/v1/tunnels/...` URL. <!-- citadel-ignore: public endpoint example for external tunnel-client config -->

- **`unsupported_channel` from the connector path**
  - The incoming command named a channel that is not configured. Add a
    channel-qualified `--mcp.server-url` / `--mcp.command` entry, or update the
    product configuration to send `main`.
  - For `harpoon`, register at least one `--harpoon.target` / `HARPOON_TARGETS`
    entry. Harpoon intentionally stays unroutable with an empty target registry.

- **OAuth-protected connector succeeds locally but fails in product**
  - The MCP server can stay private, but the authorization server itself is not
    automatically tunneled. It must be reachable wherever the OAuth browser flow
    and metadata fetches require it.
  - Issuer mismatch diagnostics are allowed for external enterprise IdPs; focus
    first on wrong URLs, unreachable metadata endpoints, or missing
    `Authorization` forwarding.

See [`connectors.md`](connectors.md) for the full connector request lifecycle,
channel routing model, and environment-variable checklist.

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

- Increase `--mcp.max-concurrent-requests` / `MCP_MAX_CONCURRENT_REQUESTS` to
  raise active MCP execution concurrency, but only if the MCP server can safely
  handle the additional parallelism. When every worker is busy, the dispatcher
  removes one command from the local queue and waits for a worker slot. It does
  not drain another command until a slot is free.
- Increase `--control-plane.max-inflight` /
  `CONTROL_PLANE_MAX_INFLIGHT_REQUESTS` only to increase the local prefetch
  backlog. It does not increase MCP execution concurrency. A full buffer pauses
  polling until a queue slot is free; each poll requests at most `25` commands.
- Account for both independent limits when sizing the process. With the
  defaults, tunnel-client can hold up to `10` active MCP requests, `20` commands
  in the local queue, and one dispatcher-held command waiting for a worker
  slot.
