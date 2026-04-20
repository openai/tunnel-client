# Tunnel MCP Plugin

Tunnel MCP is a local Codex plugin for creating and running MCP tunnels with
`tunnel-client`. The plugin handles Codex-facing concerns such as human aliases,
local state files, process supervision, and native runtime config generation. The
Go `tunnel-client` binary remains the source of truth for remote tunnel CRUD and
the long-running poll loop.

## Install

Install this directory as a local Codex plugin from either this repository root
or a standalone `tunnel-client` checkout.

Prerequisites:

- `python3`
- a Codex config directory, normally `~/.codex`
- the Codex plugin installer script:
  `skills/skills/install-codex-plugin/scripts/install_plugin.py`

From this repository root:

```bash
python skills/skills/install-codex-plugin/scripts/install_plugin.py \
  --source "$PWD/api/tunnel-client/plugins/tunnel-mcp"
```

From a standalone `tunnel-client` checkout:

```bash
python /path/to/install_plugin.py \
  --source "$PWD/plugins/tunnel-mcp"
```

To install into a non-default Codex config directory, add
`--codex-home /path/to/codex-home`.

The installer should print output like:

```text
Installed tunnel-mcp@debug
Target: /Users/you/.codex/plugins/cache/debug/tunnel-mcp/local
Config: /Users/you/.codex/config.toml
```

Verify the install:

```bash
CODEX_HOME_DIR="${CODEX_HOME:-$HOME/.codex}"
test -f "$CODEX_HOME_DIR/plugins/cache/debug/tunnel-mcp/local/.codex-plugin/plugin.json"
grep -A2 '^\[plugins\."tunnel-mcp@debug"\]' "$CODEX_HOME_DIR/config.toml"
python "$CODEX_HOME_DIR/plugins/cache/debug/tunnel-mcp/local/scripts/tunnel_mcp" --help
```

If the plugin is installed on disk but does not appear in the current Codex
session, start a new Codex session so the plugin and skill inventory is loaded.
If plugins are disabled globally, add this to `config.toml`:

```toml
[features]
plugins = true
```

The manifest lives at `.codex-plugin/plugin.json`, and the routing skill lives
under `skills/`. The plugin is standalone Python and uses only the Python
standard library plus the `tunnel-client` executable at runtime. It does not
require repository-specific Python packages or build-system runfiles.

Runtime prerequisites:

- `tunnel-client` in `PATH`, or set `TUNNEL_CLIENT_BIN` to the binary path

## Upgrade

Upgrade the plugin by rerunning the same install command with the newer plugin
source:

```bash
python skills/skills/install-codex-plugin/scripts/install_plugin.py \
  --source "$PWD/api/tunnel-client/plugins/tunnel-mcp"
```

The installer replaces the cached plugin copy at
`$CODEX_HOME/plugins/cache/debug/tunnel-mcp/local` and keeps
`[plugins."tunnel-mcp@debug"] enabled = true` in `config.toml`. Local runtime
state is stored separately under `$CODEX_HOME/tunnel-mcp`, or
`~/.codex/tunnel-mcp` when `CODEX_HOME` is unset, so aliases, admin profiles,
process history, logs, and generated native `tunnel-client` profiles are not
rewritten by the plugin cache upgrade.

After upgrading, start a new Codex session and rerun the install verification
commands above so Codex reloads the updated plugin and skill inventory.

Optional:

- `tmux` for tmux-managed background sessions. When `tmux` is unavailable, the
  plugin starts `tunnel-client run --profile <name>` directly as a detached
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
- `TUNNEL_CLIENT_PROFILE_DIR` overrides where generated native tunnel-client
  profiles are written. When unset, the plugin follows tunnel-client defaults:
  `$XDG_CONFIG_HOME/tunnel-client`, then `~/.config/tunnel-client`.
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
  --profile sample_mcp_with_dcr \
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
- `health/<alias>.url`
- `logs/<alias>.log` when the fallback detached-process launcher is used

Generated runtime profiles are written to the native tunnel-client profile
directory as `<profile>.yaml`, use `tunnel-client run --profile <profile>`, and
include `control_plane`, `mcp`, `health`, `admin_ui`, and `log` sections. They
do not persist admin keys, bearer tokens, cookies, or literal `sk-` style API
keys. Alias and process records include `profile_name` and `profile_path`;
`config_path` is kept as a compatibility alias for older local state consumers.

`admin_profiles.yaml` stores admin profile names, control-plane base URLs, and
admin key references such as `env:OPENAI_ADMIN_KEY` or `file:/path/to/key`. Alias
records include `admin_profile` so each locally known tunnel records which admin
profile was used to create or attach to it.
