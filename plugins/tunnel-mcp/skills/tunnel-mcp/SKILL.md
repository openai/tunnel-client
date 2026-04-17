---
name: tunnel-mcp
description: Create, connect, list, and inspect MCP tunnels through the local tunnel-client plugin. Use when Codex needs to manage secure MCP tunnels with aliases and native tunnel-client runtime processes.
---

# Tunnel MCP

Use `scripts/tunnel_mcp` from this plugin when a user asks Codex to manage MCP
tunnels through `tunnel-client`.

## Rules

- Use `tunnel-client admin tunnels` for remote tunnel CRUD. Do not call raw
  tunnel-service HTTP endpoints from this plugin.
- Use native `tunnel-client run --config <path>` for runtime processes. Do not
  use a helper shim that translates config files into flags.
- Do not assume a specific source checkout, build system, internal helper, or
  tmux is available. The plugin must work from an installed plugin directory
  with only `python3` and `tunnel-client`.
- Use tmux when available; otherwise start `tunnel-client run --config <path>`
  as a detached background process and report the PID/log path.
- Store alias and process state under `$CODEX_HOME/tunnel-mcp` when
  `CODEX_HOME` is set, otherwise under `~/.codex/tunnel-mcp`.
- Store admin CRUD configuration in `$CODEX_HOME/tunnel-mcp/admin_profiles.yaml`.
  Use `--admin-profile <name>` to select a profile and `--admin-key env:NAME` or
  `--admin-key file:/path` to store a non-default admin key reference. Do not
  pass literal admin keys.
- Preserve the link between tunnels and admin CRUD credentials by keeping
  `admin_profile` on alias and process records.
- Use `--runtime-api-key env:NAME` or `--runtime-api-key file:/path` when a
  runtime tunnel key should differ from the default `env:CONTROL_PLANE_API_KEY`.
  Use `connect --tunnel-id <id>` to attach to a known existing tunnel without
  admin CRUD.
- Never write literal API keys, bearer tokens, cookies, or inline `sk-` style
  secret material into plugin state or generated configs.
- Treat stale local aliases as recoverable for `create` and `connect`. If a
  stored tunnel id no longer exists remotely, record `stale-alias` in history
  and continue with scoped remote lookup or creation.
- Treat stale local aliases as reportable for `status`. Do not silently create a
  replacement tunnel from `status`.

## Examples

```bash
scripts/tunnel_mcp create \
  --alias docs-mcp \
  --admin-profile default \
  --organization-id org_123
```

```bash
scripts/tunnel_mcp connect \
  --alias docs-mcp \
  --admin-profile default \
  --organization-id org_123 \
  --mcp-server-url http://127.0.0.1:3001/mcp
```

```bash
scripts/tunnel_mcp connect \
  --alias docs-mcp \
  --tunnel-id tunnel_0123456789abcdef0123456789abcdef \
  --runtime-api-key env:TUNNEL_RUNTIME_KEY \
  --mcp-command "python /path/to/server.py"
```

```bash
scripts/tunnel_mcp status docs-mcp
```
