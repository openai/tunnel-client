# Tunnel MCP Plugin

Tunnel MCP is a local Codex plugin for creating and running MCP tunnels with
`tunnel-client`. The plugin is intentionally thin: it routes Codex plugin calls
onto the public native `tunnel-client runtimes ...` and
`tunnel-client admin-profiles ...` command trees. The Go `tunnel-client` binary
owns alias state, admin-profile state, remote tunnel CRUD, runtime config
generation, and the long-running poll loop.

The bundled skill also ships curated reference docs under
`skills/tunnel-mcp/references/` for binary acquisition, setup/install,
profiles and state dirs, admin/runtime key split, runtime lifecycle flows, and
troubleshooting. Those references are intended to be consulted selectively
based on the user prompt, not dumped wholesale into every response.

## Install

If you already have a `tunnel-client` binary, prefer the binary-owned install
surface so the plugin bundle always matches the binary version:

```bash
tunnel-client codex plugin install
tunnel-client codex plugin uninstall
```

If you only need a terminal assistant over the same Codex app-server bridge,
start with:

```bash
tunnel-client codex assistant "Summarize the current tunnel setup."
```

If you want to inspect the embedded bundle before installing it:

```bash
tunnel-client codex plugin export --dir /tmp/tunnel-mcp
```

Install that exported bundle from the export directory itself:

```bash
cd /tmp/tunnel-mcp
sh scripts/install_plugin.sh --tunnel-client-bin /path/to/tunnel-client
```

Windows PowerShell:

```powershell
Set-Location C:\tmp\tunnel-mcp
powershell -NoProfile -ExecutionPolicy Bypass -File .\scripts\Install-Plugin.ps1 --tunnel-client-bin C:\path\to\tunnel-client.exe
```

If you do not already have a `tunnel-client` binary, use one of these
public-safe setup paths first:

- public repo: `https://github.com/openai/tunnel-client`
- latest releases: `https://github.com/openai/tunnel-client/releases/latest`

Build from source from the public repo:

```bash
git clone https://github.com/openai/tunnel-client.git
cd tunnel-client
go build -o bin/tunnel-client ./cmd/client
```

Windows source build:

```powershell
git clone https://github.com/openai/tunnel-client.git
cd tunnel-client
go build -o bin/tunnel-client.exe ./cmd/client
```

After you have a binary, either set `TUNNEL_CLIENT_BIN` to the full path or
reinstall the plugin with `--tunnel-client-bin /path/to/tunnel-client`.
The routed plugin commands do not auto-download, auto-clone, or auto-run remote
`tunnel-client` binaries by themselves.

If you are installing from a source checkout instead, use the local installer in
this plugin directory.

Install this directory as a local Codex plugin from either this repository root
or a standalone `tunnel-client` checkout.

Prerequisites:

- macOS/Linux shell or Windows PowerShell
- a `tunnel-client` binary available via `--tunnel-client-bin`, `TUNNEL_CLIENT_BIN`, adjacent build outputs, or `PATH`
- a Codex config directory, normally `~/.codex`

From a `tunnel-client` module root:

```bash
./plugins/tunnel-mcp/scripts/install_plugin.sh --tunnel-client-bin /path/to/tunnel-client
```

From Windows PowerShell in a `tunnel-client` module root:

```powershell
.\plugins\tunnel-mcp\scripts\Install-Plugin.ps1 --tunnel-client-bin C:\path\to\tunnel-client.exe
```

To install into a non-default Codex config directory, add `--codex-home /path/to/codex-home`.
The wrapper scripts locate the requested `tunnel-client` binary and delegate to
`tunnel-client codex plugin install`, so the installed bundle matches the
selected binary. When possible the binary install path also persists a matching
`.tunnel-client-bin` hint into the installed plugin bundle so Codex can use the
plugin from an empty working directory without separately setting
`TUNNEL_CLIENT_BIN`.

The installer should print output like:

```text
Installed tunnel-mcp into /Users/you/.codex/plugins/cache/debug/tunnel-mcp/local
Updated Codex config /Users/you/.codex/config.toml
```

Verify the install:

```bash
CODEX_HOME_DIR="${CODEX_HOME:-$HOME/.codex}"
test -f "$CODEX_HOME_DIR/plugins/cache/debug/tunnel-mcp/local/.codex-plugin/plugin.json"
grep -A2 '^\[plugins\."tunnel-mcp@debug"\]' "$CODEX_HOME_DIR/config.toml"
"$CODEX_HOME_DIR/plugins/cache/debug/tunnel-mcp/local/scripts/tunnel_mcp" --help
```

If the plugin is installed on disk but does not appear in the current Codex
session, start a new Codex session so the plugin and skill inventory is loaded.
If plugins are disabled globally, add this to `config.toml`:

```toml
[features]
plugins = true
```

The manifest lives at `.codex-plugin/plugin.json`, and the routing skill lives
under `skills/`. The installed plugin runtime is a thin shell router on
macOS/Linux plus Windows-native launcher scripts. It invokes the native
`tunnel-client` executable directly and does not implement tunnel protocol
logic itself.

Runtime prerequisites:

- a `tunnel-client` binary discoverable in this order:
  `--tunnel-client-bin`, `TUNNEL_CLIENT_BIN`, an installed bundle hint,
  adjacent source/build outputs, then `PATH`
- once the plugin is installed, prefer the installed router and persisted
  `.tunnel-client-bin` hint over an ambient `tunnel-client` on `PATH`
- executable naming:
  - macOS/Linux: `tunnel-client`
  - Windows: `tunnel-client.exe`
- the public install path does not require Python
- cross-platform router entrypoints:
  - macOS/Linux: `scripts/tunnel_mcp`
  - Windows: `scripts\\tunnel_mcp.cmd` or `powershell -File scripts\\tunnel_mcp.ps1`

## Upgrade

Upgrade the plugin by rerunning the same install command with the newer plugin
source:

```bash
./plugins/tunnel-mcp/scripts/install_plugin.sh --tunnel-client-bin /path/to/tunnel-client
```

Windows PowerShell:

```powershell
.\plugins\tunnel-mcp\scripts\Install-Plugin.ps1 --tunnel-client-bin C:\path\to\tunnel-client.exe
```

The installer replaces the cached plugin copy at
`$CODEX_HOME/plugins/cache/debug/tunnel-mcp/local` and keeps
`[plugins."tunnel-mcp@debug"] enabled = true` in `config.toml`. Local runtime
state is stored separately under `TUNNEL_CLIENT_STATE_DIR` when set, otherwise
the platform state directory such as `$XDG_STATE_HOME/tunnel-client` or
`~/.local/state/tunnel-client`. Existing legacy `CODEX_HOME/tunnel-mcp` /
`~/.codex/tunnel-mcp` state is reused when it already exists, so aliases,
admin profiles, process history, logs, and generated native `tunnel-client`
profiles are not rewritten by the plugin cache upgrade.

After upgrading, start a new Codex session and rerun the install verification
commands above so Codex reloads the updated plugin and skill inventory.

## Uninstall

If the plugin was installed from the `tunnel-client` binary, prefer the
matching binary-owned uninstall path:

```bash
tunnel-client codex plugin uninstall
```

That removes the cached plugin bundle plus the `tunnel-mcp@debug` enablement
section from `config.toml` without touching unrelated Codex plugins. If you
want a clean reinstall, rerun:

```bash
tunnel-client codex plugin uninstall
tunnel-client codex plugin install
```

Optional:

- `tmux` for tmux-managed background runtimes. When `tmux` is unavailable, the
  plugin starts `tunnel-client run --profile-dir <dir> --profile <name>`
  directly as a detached background process and records its PID and log path in
  local state.

## Environment

- Required Platform permissions:
  - Runtime-only users need Tunnels **Read** + **Use** on the target tunnel.
  - Remote tunnel CRUD users need Tunnels **Read** + **Manage** plus an admin
    API key.
  - Users who create admin keys need Platform admin-key permission separately.
  - ChatGPT connector admins need Tunnels **Read** + **Use** and the tunnel
    must include the target workspace ID to appear in the connector tunnel
    picker.
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
- `TUNNEL_CLIENT_STATE_DIR` overrides the native local runtime/admin-profile
  state root. When unset, `tunnel-client` uses the platform state directory and
  falls back to legacy `CODEX_HOME` / `~/.codex/tunnel-mcp` state when it
  already exists.

## Native Commands

The public native command tree is:

```bash
tunnel-client runtimes create ...
tunnel-client runtimes connect ...
tunnel-client runtimes list
tunnel-client runtimes status <alias>
tunnel-client runtimes stop <alias>
tunnel-client runtimes rm <alias>
tunnel-client admin-profiles list
tunnel-client admin-profiles set <name> --admin-key env:OPENAI_ADMIN_KEY
```

The plugin entrypoint `scripts/tunnel_mcp ...` remains available inside Codex,
prints its own thin-router help, and forwards to the native commands above
while preserving JSON output for the plugin contract.

## Runtime Examples

Create or reuse a remote tunnel:

```bash
tunnel-client runtimes create \
  --alias awesome-mcp \
  --name "Awesome MCP" \
  --admin-profile default \
  --organization-id org_123
```

Create or reuse a tunnel with a separate admin profile and admin key reference:

```bash
tunnel-client admin-profiles set sandbox \
  --admin-key env:SANDBOX_OPENAI_ADMIN_KEY \
  --control-plane-base-url https://api.openai.com

tunnel-client runtimes create \
  --alias awesome-mcp \
  --admin-profile sandbox \
  --organization-id org_123
```

Connect a local HTTP MCP server:

```bash
tunnel-client runtimes connect \
  --alias awesome-mcp \
  --profile sample_mcp_with_dcr \
  --admin-profile sandbox \
  --organization-id org_123 \
  --mcp-server-url http://127.0.0.1:3001/mcp
```

Connect a local stdio MCP server:

```bash
tunnel-client runtimes connect \
  --alias awesome-mcp \
  --organization-id org_123 \
  --mcp-command "python /path/to/server.py"
```

Attach to an existing tunnel id without admin CRUD and run it with a specific
runtime key reference:

```bash
tunnel-client runtimes connect \
  --alias existing-mcp \
  --tunnel-id tunnel_0123456789abcdef0123456789abcdef \
  --runtime-api-key env:TUNNEL_RUNTIME_KEY \
  --mcp-command "python /path/to/server.py"
```

Inspect local and remote state:

```bash
tunnel-client runtimes status awesome-mcp
tunnel-client runtimes stop awesome-mcp
# or:
tunnel-client runtimes disconnect awesome-mcp
tunnel-client runtimes list --organization-id org_123
```

`status` always reports local runtime state first. When admin auth is missing or
the remote tunnel no longer exists, the output still includes local profile,
health, explicit `ui_url`, tmux/process, and log diagnostics. `connect` also reuses a locally known
tunnel id when remote admin lookup fails. `connect` success now means a usable
local runtime exists: the managed process or tmux session is still alive, the
health URL file is populated, and `/healthz` is reachable. The payload exposes
`launched`, `started`, `healthy`, and `ready` so agents can distinguish "launch
command issued" from "healthy tunnel runtime exists". If the runtime dies
immediately or never becomes healthy, `connect` returns a non-zero JSON payload
instead of claiming `started=true`.

`stop` and `disconnect` are local runtime controls only. They stop the managed
tmux runtime or detached process, clear the local health URL file, and leave the
remote tunnel itself intact.

Auth split to keep straight:

- runtime key: `CONTROL_PLANE_API_KEY` / `OPENAI_API_KEY`
- runtime-key principal: needs Tunnels **Read** + **Use** on the target tunnel
- read-only lookup: `tunnel-client admin tunnels get <tunnel_id>` can use the
  runtime key
- admin CRUD: `list`, `create`, `update`, and `delete` still require
  `OPENAI_ADMIN_KEY` or `--admin-key` plus Tunnels **Manage**

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
