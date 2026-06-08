# Tunnel MCP Plugin

Tunnel MCP is a local Codex plugin for creating and running MCP tunnels with
`tunnel-client`. The plugin is intentionally thin: Codex loads the plugin and
skill guidance, then the router delegates tunnel work to the native
`tunnel-client runtimes ...` and `tunnel-client admin-profiles ...` command
trees. The Go binary owns tunnel protocol logic, alias state, admin profiles,
runtime config generation, process management, and control-plane polling.

When Codex has loaded the plugin's MCP app server, prefer the first-class tools
over manual shell routing:

- `install_or_select_tunnel_client`
- `create_tunnel_runtime`
- `connect_stdio_mcp`
- `list_runtime_aliases`
- `runtime_status`
- `stop_runtime`

Those tools are an operator surface over native `tunnel-client`. They run the
same `tunnel-client runtimes ...` lifecycle commands and normalize structured
fields such as `tunnel_id`, `alias`, `profile_path`, `/healthz`, `/readyz`,
`control_plane_poll_health`, `session_name`, `repair_actions`, selected binary,
live process command, live process binary, and launch diagnostics. They are not
a replacement control-plane client and do not reimplement tunnel protocol,
runtime profile, or process-management behavior.

Use this README for install and operating quick start. For detailed agent
guidance, open only the relevant curated file under
`skills/tunnel-mcp/references/`:

- `binary.md`: find or build a public-safe `tunnel-client` binary.
- `setup-and-install.md`: install, export, reset, and binary-vs-bundle setup.
- `profiles-state-and-keys.md`: profile dirs, state dirs, and key references.
- `runtime-flows.md`: create, connect, list, status, stop, rm, cleanup, attach.
- `troubleshooting.md`: `/healthz`, `/readyz`, `/ui`, logs, stale aliases.

## Install

If a `tunnel-client` binary is already available, prefer the binary-owned
surface so the installed plugin bundle matches the binary version:

```bash
tunnel-client codex plugin install
tunnel-client codex status
tunnel-client codex diagnose --json
tunnel-client codex plugin uninstall
```

For a terminal assistant over the Codex app-server bridge:

```bash
tunnel-client codex assistant "Summarize the current tunnel setup."
```

To inspect or install an exported bundle:

```bash
tunnel-client codex plugin export --dir /tmp/tunnel-mcp
cd /tmp/tunnel-mcp
sh scripts/install_plugin.sh --tunnel-client-bin /path/to/tunnel-client
```

Windows PowerShell from an exported bundle root:

```powershell
Set-Location C:\tmp\tunnel-mcp
powershell -NoProfile -ExecutionPolicy Bypass -File .\scripts\Install-Plugin.ps1 --tunnel-client-bin C:\path\to\tunnel-client.exe
```

If the binary is missing, use the public repo or latest release first:

- `https://github.com/openai/tunnel-client`
- `https://github.com/openai/tunnel-client/releases/latest`

Source build:

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

After a binary exists, either set `TUNNEL_CLIENT_BIN` to its full path or pass
`--tunnel-client-bin /path/to/tunnel-client`. The routed plugin commands do not
auto-download, auto-clone, or auto-run remote `tunnel-client` binaries by
themselves.

Source-checkout fallback installers:

```bash
./plugins/tunnel-mcp/scripts/install_plugin.sh --tunnel-client-bin /path/to/tunnel-client
```

```powershell
.\plugins\tunnel-mcp\scripts\Install-Plugin.ps1 --tunnel-client-bin C:\path\to\tunnel-client.exe
```

To install into a non-default Codex config directory, add
`--codex-home /path/to/codex-home`. The wrappers delegate to
`tunnel-client codex plugin install` and, when possible, persist a matching
`.tunnel-client-bin` hint into the installed bundle so Codex can use the plugin
from an empty working directory.

Verify install or upgrade:

```bash
CODEX_HOME_DIR="${CODEX_HOME:-$HOME/.codex}"
test -f "$CODEX_HOME_DIR/plugins/cache/debug/tunnel-mcp/local/.codex-plugin/plugin.json"
grep -A2 '^\[plugins\."tunnel-mcp@debug"\]' "$CODEX_HOME_DIR/config.toml"
"$CODEX_HOME_DIR/plugins/cache/debug/tunnel-mcp/local/scripts/tunnel_mcp" --help
tunnel-client codex status --json
tunnel-client codex diagnose --json
scripts/tunnel_mcp self-check
```

If the plugin is installed on disk but missing from the current Codex session,
start a new Codex session so plugin and skill inventory reloads. If plugins are
disabled globally, add:

```toml
[features]
plugins = true
```

## Upgrade And Uninstall

Upgrade by rerunning the same install command against the newer plugin source.
The installer replaces only
`$CODEX_HOME/plugins/cache/debug/tunnel-mcp/local` and keeps runtime state under
`TUNNEL_CLIENT_STATE_DIR`, the platform state directory, or reused legacy
`CODEX_HOME` / `~/.codex/tunnel-mcp` roots.

Prefer binary-owned uninstall:

```bash
tunnel-client codex plugin uninstall
```

That removes the cached plugin bundle plus `tunnel-mcp@debug` enablement from
`config.toml` without touching unrelated Codex plugins.

## Runtime Quick Start

Native command surface:

```bash
tunnel-client runtimes create ...
tunnel-client runtimes connect ...
tunnel-client runtimes list
tunnel-client runtimes status <alias>
tunnel-client runtimes stop <alias>
tunnel-client runtimes rm <alias>
tunnel-client runtimes cleanup
tunnel-client codex diagnose [alias]
tunnel-client admin-profiles list
tunnel-client admin-profiles set <name> --admin-key env:OPENAI_ADMIN_KEY
```

Common flows:

```bash
tunnel-client admin-profiles set sandbox \
  --admin-key env:SANDBOX_OPENAI_ADMIN_KEY \
  --control-plane-base-url https://api.openai.com

tunnel-client runtimes create \
  --alias awesome-mcp \
  --name "Awesome MCP" \
  --admin-profile sandbox \
  --organization-id org_123

tunnel-client runtimes connect \
  --alias awesome-mcp \
  --admin-profile sandbox \
  --organization-id org_123 \
  --mcp-server-url http://127.0.0.1:3001/mcp

tunnel-client runtimes connect \
  --alias existing-mcp \
  --tunnel-id tunnel_0123456789abcdef0123456789abcdef \
  --runtime-api-key env:TUNNEL_RUNTIME_KEY \
  --mcp-command "python /path/to/server.py"

tunnel-client runtimes status awesome-mcp
tunnel-client runtimes stop awesome-mcp
tunnel-client runtimes rm awesome-mcp
```

Use `tunnel-client run ...` when you intentionally want a foreground daemon
attached to the current terminal. For a long-lived local runtime managed by
Codex, prefer `tunnel-client runtimes connect ...`; do not use `nohup` or
`disown` as the tunnel-client supervision path.

`status` reports local runtime state first and also surfaces `ui_url`, logs,
tmux/process state, stale recorded URLs, live admin URLs, `/healthz`,
`/readyz`, and `control_plane_poll_health`. `connect` success means a usable
local runtime exists: the managed process or tmux session is alive, the health
URL file is populated, and `/healthz` is reachable. After `runtimes connect`,
run `tunnel-client runtimes status <alias>` before reporting success. Only
report success when status shows the managed runtime running with health
reported; use `--json` when Codex needs explicit `process_running`, `healthy`,
and `ready` fields.

`stop` and `disconnect` are local runtime controls only. They stop the managed
tmux runtime or detached process, clear the local health URL file, and leave
the remote tunnel intact. `runtimes cleanup --apply` removes only
`stale_alias` entries; `missing_profile` entries are left for reconnect or
manual review.

## Auth, State, And Environment

Required Platform permissions:

- Runtime-only users need Tunnels **Read** + **Use** on the target tunnel.
- Remote tunnel CRUD users need Tunnels **Read** + **Manage** plus an admin key.
- ChatGPT connector admins need Tunnels **Read** + **Use**, and the tunnel must
  include the target workspace ID to appear in the connector picker.

Key split:

- Runtime key: `CONTROL_PLANE_API_KEY`, `OPENAI_API_KEY`, or
  `--runtime-api-key env:NAME|file:/path`.
- Admin CRUD key: `OPENAI_ADMIN_KEY`, `TUNNEL_MCP_ADMIN_KEY`, or
  `--admin-key env:NAME|file:/path`.
- Literal API keys, bearer tokens, cookies, and inline `sk-` style secrets are
  rejected and must not be written into plugin state or generated configs.

Main environment knobs:

- `TUNNEL_CLIENT_BIN`: selected `tunnel-client` binary path.
- `CONTROL_PLANE_BASE_URL`: tunnel control-plane host root; default
  `https://api.openai.com`.
- `CONTROL_PLANE_URL_PATH`: optional path appended to the control-plane host
  root before tunnel-client adds its `/v1/...` routes.
- `control_plane_url_path`: optional tunnel-mcp tool argument that stores the
  same path on the native admin profile for create/list/connect flows.
- `TUNNEL_MCP_ADMIN_PROFILE`: admin profile name; default `default`.
- `TUNNEL_CLIENT_PROFILE_DIR`: generated native profile directory.
- `TUNNEL_CLIENT_STATE_DIR`: native local runtime/admin-profile state root.

State files include `aliases.yaml`, `admin_profiles.yaml`, `processes.yaml`,
`history.md`, `health/<alias>.url`, and `logs/<alias>.log` when the fallback
detached-process launcher is used. Generated runtime profiles are native
`tunnel-client` profiles and include control plane, MCP, health, admin UI, and
log sections without persisting literal secrets.
