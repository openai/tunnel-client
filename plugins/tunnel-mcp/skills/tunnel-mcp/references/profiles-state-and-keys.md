# Profiles, state dirs, and key split

Keep three pieces separate:

- `profile`: the generated `tunnel-client` YAML config that `run --profile` uses
- `runtime alias`: the named local runtime record managed by `tunnel-client runtimes ...`
- `admin profile`: the saved admin credential/base-url record managed by `tunnel-client admin-profiles ...`

Directory ownership:

- `TUNNEL_CLIENT_PROFILE_DIR`: where generated runtime YAML profiles are written
- `TUNNEL_CLIENT_STATE_DIR`: where runtime alias state, admin-profile state, logs, and process history live
- when `TUNNEL_CLIENT_STATE_DIR` is unset, tunnel-client uses the platform state directory and can reuse legacy `CODEX_HOME` / `~/.codex/tunnel-mcp` state if it already exists

Key split:

- `OPENAI_ADMIN_KEY`: admin CRUD for `tunnel-client admin tunnels ...`
- `CONTROL_PLANE_API_KEY`: runtime key used by the launched tunnel runtime
- `--admin-key env:NAME|file:/path`: store a non-default admin key reference in an admin profile
- `--runtime-api-key env:NAME|file:/path`: store a non-default runtime key reference in generated runtime config

Do not write literal admin keys or runtime keys into plugin state or generated
config files.
