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
- `/ui`: local admin UI
- `ui_url`: the explicit admin UI URL exposed in runtime status
- `control_plane_poll_health`: route-level poll health from the local admin UI,
  separate from `/healthz` and `/readyz`
- `repair_actions`: structured commands and reasons for branchable fixes
- `selected_tunnel_client_bin`: the binary selected for the app tool invocation
- `live_process_binary`: the binary recorded in the active runtime command, if
  the runtime has one
- `launch_diagnostics`: exit code and runtime log tail captured during launch
  failures

If `tmux` is available, tunnel-client prefers a tmux-managed runtime. When
`tmux` is unavailable, it falls back to a detached background process and
records PID/log path in local state.

If `readyz` is failing:

- confirm the runtime process is still alive
- read the runtime log path from `runtimes status`
- confirm the runtime key/admin profile split is correct
- verify the generated profile path and referenced secrets are the ones you expect

If `/healthz` and `/readyz` are green but control-plane updates are missing,
check `control_plane_poll_health`. A dead proxy can break control-plane polling
while local readiness stays green.

If a saved alias points at a dead health port, status scans live local admin UI
health URL files and maps any matching `control_plane_tunnel_id` back to the
alias. The payload reports both the stale recorded URL and the live admin URL.

Use `tunnel-client runtimes cleanup` to classify local inventory:

- `live_runtime`: a process, tmux session, or live admin UI still exists
- `valid_profile`: local profile exists but no runtime is running
- `missing_profile`: alias points at a profile that no longer exists
- `stale_alias`: no runtime and no usable profile metadata

Only `tunnel-client runtimes cleanup --apply` removes `stale_alias` entries.

If a stored alias points at a missing remote tunnel, `create` and `connect`
treat it as recoverable and continue with scoped lookup or creation, while
`status` reports the stale alias instead of silently creating a replacement.
