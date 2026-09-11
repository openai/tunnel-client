# Troubleshooting

Start with local runtime state, not remote guesses:

- `tunnel-client runtimes status <alias>`
- `tunnel-client codex diagnose <alias> --json`
- `tunnel-client doctor --profile <name>`
- `tunnel-client doctor --profile <name> --explain`

Health/debug signals:

- `health_url_file`: the file that stores the resolved local health base URL
- `/healthz`: liveness
- `/readyz`: readiness
- `health_details_url` or `/health?details=true`: local bounded component snapshots
- `mcp_health_url` or `/health/mcp`: observed discovery from the main stdio child
- `/ui`: local admin UI
- `ui_url`: the explicit admin UI URL exposed in runtime status
- `control_plane_poll_health`: route-level poll health from the local admin UI, separate from `/healthz` and `/readyz`
- `repair_actions`: structured commands and reasons for branchable fixes
- `selected_tunnel_client_bin`: the binary selected for the app tool invocation
- `live_process_binary`: the binary recorded in the active runtime command, if the runtime has one
- `launch_diagnostics`: exit code and runtime log tail captured during launch failures

New managed runtimes use a detached background process and record PID/log path in local state. Status and stop continue to recognize a tmux-managed runtime recorded by an older client; reconnect migrates only that exact owned session to process supervision.

If `readyz` is failing:

- confirm the runtime process is still alive
- read the runtime log path from `runtimes status`
- confirm the runtime key/admin profile split is correct
- verify the generated profile path and referenced secrets are the ones you expect

If `/healthz` and `/readyz` are green but control-plane updates are missing, check `control_plane_poll_health`. A dead proxy can break control-plane polling while local readiness stays green.

On runtimes supporting component health, check `control-plane` for polls and `response-delivery` for uploads separately. `queue` counts waiting local work; `dispatcher` counts actual active operations. A ready stdio runtime can have unobserved MCP discovery until initialize/tools-list traffic is forwarded. Health reads never trigger that traffic. Check timestamps, generation and catalog completeness before interpreting retained tool names. The new routes return 200 for a readable degraded snapshot and require loopback or Unix access. Older runtimes can return 404; continue using their existing probe fields.

If a saved alias points at a dead health port, status scans live local admin UI health URL files and maps any matching `control_plane_tunnel_id` back to the alias. The payload reports both the stale recorded URL and the live admin URL.

Use `tunnel-client runtimes cleanup` to classify local inventory:

- `live_runtime`: a process, tmux session, or live admin UI still exists
- `valid_profile`: local profile exists but no runtime is running
- `missing_profile`: alias points at a profile that no longer exists
- `stale_alias`: no runtime and no usable profile metadata

Only `tunnel-client runtimes cleanup --apply` removes `stale_alias` entries.

If a stored alias points at a missing remote tunnel, `create` and `connect` treat it as recoverable and continue with scoped lookup or creation, while `status` reports the stale alias instead of silently creating a replacement.
