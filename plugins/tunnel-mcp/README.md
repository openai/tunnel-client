# Tunnel MCP Plugin

Tunnel MCP is a local Codex plugin for creating and running MCP tunnels with
`tunnel-client`. The plugin handles Codex-facing concerns such as human aliases,
local state files, process supervision, and native runtime config generation. The
Go `tunnel-client` binary remains the source of truth for remote tunnel CRUD and
the long-running poll loop.

## Install

Install this directory as a local Codex plugin from a `tunnel-client` checkout
or from an extracted plugin directory.

Example local installs:

```bash
python /path/to/openai/skills/skills/install-codex-plugin/scripts/install_plugin.py \
  --source "$PWD/plugins/tunnel-mcp"
```

The manifest lives at `.codex-plugin/plugin.json`, and the routing skill lives
under `skills/`. The plugin is standalone Python and uses only the Python
standard library plus the `tunnel-client` executable at runtime. It does not
require repository-specific Python packages or build-system runfiles.

Runtime prerequisites:

- `python3`
- `tunnel-client`

Optional:

- `tmux` for tmux-managed background sessions. When `tmux` is unavailable, the
  plugin starts `tunnel-client run --config <path>` directly as a detached
  background process and records its PID and log path in local state.

## Environment

- `TUNNEL_CLIENT_BIN` overrides the `tunnel-client` binary path.
- `CONTROL_PLANE_BASE_URL` overrides the tunnel control-plane host root. The
  default is `https://api.openai.com`.
- `TUNNEL_MCP_ADMIN_PROFILE` selects the admin profile name used for
  `tunnel-client admin tunnels` commands. The default profile is `default`.
- `OPENAI_ADMIN_KEY` is referenced by the default admin profile as
  `env:OPENAI_ADMIN_KEY`.
- `TUNNEL_MCP_ADMIN_KEY` or `--admin-key env:VARNAME` / `--admin-key
  file:/path/to/key` stores a different admin key reference in the selected
  admin profile. Literal admin keys are rejected.
- `CONTROL_PLANE_API_KEY` supplies the runtime key consumed by generated native
  config files. Generated configs store `env:CONTROL_PLANE_API_KEY`, not the
  literal key.
- `TUNNEL_MCP_RUNTIME_API_KEY` or `--runtime-api-key env:VARNAME` /
  `--runtime-api-key file:/path/to/key` changes the runtime key reference stored
  in generated native configs. Literal runtime keys are rejected.
- `CODEX_HOME` controls local plugin state. State is stored under
  `$CODEX_HOME/tunnel-mcp` when set, otherwise under `~/.codex/tunnel-mcp`.

## Commands

Create or reuse a remote tunnel:

```bash
scripts/tunnel_mcp create \
  --alias awesome-mcp \
  --name "Awesome MCP" \
  --admin-profile default \
  --organization-id org_123
```

Create or reuse a tunnel with a separate admin profile and admin key reference:

```bash
scripts/tunnel_mcp create \
  --alias awesome-mcp \
  --admin-profile sandbox \
  --admin-key env:SANDBOX_OPENAI_ADMIN_KEY \
  --control-plane-base-url https://api.openai.com \
  --organization-id org_123
```

Connect a local HTTP MCP server:

```bash
scripts/tunnel_mcp connect \
  --alias awesome-mcp \
  --admin-profile sandbox \
  --organization-id org_123 \
  --mcp-server-url http://127.0.0.1:3001/mcp
```

Connect a local stdio MCP server:

```bash
scripts/tunnel_mcp connect \
  --alias awesome-mcp \
  --organization-id org_123 \
  --mcp-command "python /path/to/server.py"
```

Attach to an existing tunnel id without admin CRUD and run it with a specific
runtime key reference:

```bash
scripts/tunnel_mcp connect \
  --alias existing-mcp \
  --tunnel-id tunnel_0123456789abcdef0123456789abcdef \
  --runtime-api-key env:TUNNEL_RUNTIME_KEY \
  --mcp-command "python /path/to/server.py"
```

Inspect local and remote state:

```bash
scripts/tunnel_mcp status awesome-mcp
scripts/tunnel_mcp list --organization-id org_123
```

## State Files

The plugin writes JSON syntax to `.yaml` files so the files stay dependency-free
and human-inspectable:

- `aliases.yaml`
- `admin_profiles.yaml`
- `processes.yaml`
- `history.md`
- `configs/<alias>.yaml`
- `health/<alias>.url`
- `logs/<alias>.log` when the fallback detached-process launcher is used

Generated runtime configs use native `tunnel-client run --config <path>` and
include `control_plane`, `mcp`, `health`, `admin_ui`, and `log` sections. They
do not persist admin keys, bearer tokens, cookies, or literal `sk-` style API
keys.

`admin_profiles.yaml` stores admin profile names, control-plane base URLs, and
admin key references such as `env:OPENAI_ADMIN_KEY` or `file:/path/to/key`. Alias
records include `admin_profile` so each locally known tunnel records which admin
profile was used to create or attach to it.
