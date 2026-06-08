---
name: tunnel-mcp
description: Create, connect, list, and inspect MCP tunnel runtimes through the local tunnel-client plugin. Use when Codex needs to manage secure MCP tunnels with aliases and native tunnel-client runtime processes.
---

# Tunnel MCP

Use `scripts/tunnel_mcp` from this plugin when a user asks Codex to manage MCP
tunnels through `tunnel-client`. The plugin entrypoint is a thin router onto
the public native `tunnel-client runtimes ...` and
`tunnel-client admin-profiles ...` command trees.

When the `tunnel-mcp` MCP app tools are available, use them first instead of
manual shell routing:

- `install_or_select_tunnel_client`
- `create_tunnel_runtime`
- `connect_stdio_mcp`
- `list_runtime_aliases`
- `runtime_status`
- `stop_runtime`

The app tools are an operator surface over native `tunnel-client`; they
orchestrate `tunnel-client runtimes ...`, normalize structured output, and keep
tunnel protocol/runtime behavior in the Go binary.

Before acting, consult only the smallest relevant reference under `references/`:

- `references/binary.md`: how to find or obtain a public-safe `tunnel-client` binary
- `references/setup-and-install.md`: install, export, reset, binary-vs-bundle setup
- `references/profiles-state-and-keys.md`: profiles, state dirs, admin/runtime key split
- `references/runtime-flows.md`: create, connect, list, status, stop, rm, attach by tunnel id
- `references/troubleshooting.md`: `/healthz`, `/readyz`, `/ui`, status, logs, stale aliases

## Rules

- Use `tunnel-client admin tunnels` for remote tunnel CRUD. Do not call raw
  tunnel-service HTTP endpoints from this plugin.
- Route operational actions through `tunnel-client runtimes ...` and
  `tunnel-client admin-profiles ...`.
- Use `scripts/tunnel_mcp self-check` for plugin/binary/router compatibility;
  it must report secret reference presence without printing secret values.
- Use native `tunnel-client run --profile <name>` only when the user
  intentionally wants a foreground daemon attached to the current terminal;
  do not translate profile files into flags in the plugin layer.
- For a long-lived local runtime managed by Codex, use
  `tunnel-client runtimes connect ...`; do not use `nohup` or `disown` as the
  tunnel-client supervision path.
- After `runtimes connect`, run `tunnel-client runtimes status <alias>` before
  reporting success. Only report success when status shows the managed runtime
  running with health reported; use `--json` when Codex needs explicit
  `process_running`, `healthy`, and `ready` fields.
- Do not assume a source checkout, build system, helper, or tmux. The installed
  plugin must work with the selected `tunnel-client` binary alone.
- Treat ambient `PATH` binary candidates as diagnostics unless selected through
  `--tunnel-client-bin`, `TUNNEL_CLIENT_BIN`, or `.tunnel-client-bin`.
- Tunnel state, admin profiles, generated runtime profiles, stale-alias
  handling, cleanup classification, and local process management are owned by
  native `tunnel-client`; consult the relevant reference before explaining those
  details.
- Keep admin and runtime credentials split: admin CRUD uses
  `admin-profiles`; runtime attach/connect uses `--runtime-api-key env:NAME` or
  `file:/path`. Do not pass literal keys.
- Never write literal API keys, bearer tokens, cookies, or inline `sk-` style
  secret material into plugin state or generated configs.
- Surface `control_plane_poll_health` separately from `/healthz` and `/readyz`;
  local readiness can be green while control-plane polling fails through a dead
  proxy.
