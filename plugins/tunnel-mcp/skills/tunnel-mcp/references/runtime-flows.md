# Runtime flows

Use `tunnel-client runtimes ...` for native runtime lifecycle management. The
plugin router is only a thin wrapper over this command family.

Use `tunnel-client run ...` when you intentionally want a foreground daemon
attached to the current terminal. For a long-lived local runtime managed by
Codex, prefer `tunnel-client runtimes connect ...`; do not use `nohup` or
`disown` as the tunnel-client supervision path.

Create or reuse a remote tunnel alias:

- `tunnel-client runtimes create --alias docs-mcp --organization-id org_123`

Connect a local HTTP MCP server:

- `tunnel-client runtimes connect --alias docs-mcp --organization-id org_123 --mcp-server-url http://127.0.0.1:3001/mcp`

Connect a local stdio MCP server:

- `tunnel-client runtimes connect --alias docs-mcp --organization-id org_123 --mcp-command "python /path/to/server.py"`

Attach to an existing tunnel without admin CRUD:

- `tunnel-client runtimes connect --alias existing-mcp --tunnel-id tunnel_... --runtime-api-key env:TUNNEL_RUNTIME_KEY --mcp-command "python /path/to/server.py"`

Inspect, list, or stop managed local runtimes:

- `tunnel-client runtimes list`
- `tunnel-client runtimes status docs-mcp`
- `tunnel-client runtimes stop docs-mcp`
- `tunnel-client runtimes rm docs-mcp`
- `tunnel-client runtimes cleanup`
- `tunnel-client runtimes cleanup --apply`

`connect` success means the local runtime is actually launched and health is
reachable, not merely that a launch command was issued.

After `runtimes connect`, run `tunnel-client runtimes status <alias>` before
reporting success. Only report success when status shows the managed runtime
running with health reported. Use `--json` when Codex needs explicit
`process_running`, `healthy`, and `ready` fields.

The MCP app server exposes `list_runtime_aliases` as the first-class tool for
`tunnel-client runtimes list`.

The MCP app server tools do not accept control-plane URL overrides or runtime
key references. Use native `tunnel-client admin-profiles ...` or
`tunnel-client runtimes ...` commands when an operator intentionally needs a
trusted non-default control plane or key reference.

`status` reports structured `repair_actions`, live-admin reconciliation when a
stored health URL is stale, selected/live binary fields, launch diagnostics, and
`control_plane_poll_health` separately from local `/healthz` and `/readyz`.

`cleanup --apply` only removes aliases classified as `stale_alias`. It leaves
`live_runtime`, `valid_profile`, and `missing_profile` entries in place.
