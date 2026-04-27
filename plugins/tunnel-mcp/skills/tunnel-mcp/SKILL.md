---
name: tunnel-mcp
description: Create, connect, list, and inspect MCP tunnel runtimes through the local tunnel-client plugin. Use when Codex needs to manage secure MCP tunnels with aliases and native tunnel-client runtime processes.
---

# Tunnel MCP

Use `scripts/tunnel_mcp` from this plugin when a user asks Codex to manage MCP
tunnels through `tunnel-client`. The plugin entrypoint is a thin router onto
the public native `tunnel-client runtimes ...` and
`tunnel-client admin-profiles ...` command trees.

Before acting, consult only the relevant curated reference file under
`references/`:

- `references/binary.md`: how to find or obtain a public-safe `tunnel-client` binary
- `references/setup-and-install.md`: install, export, reset, binary-vs-bundle setup
- `references/profiles-state-and-keys.md`: profiles, state dirs, admin/runtime key split
- `references/runtime-flows.md`: create, connect, list, status, stop, rm, attach by tunnel id
- `references/troubleshooting.md`: `/healthz`, `/readyz`, `/ui`, status, logs, stale aliases

Do not open every reference by default. Pick the smallest relevant set for the
current prompt, use those files as the repository-specific source of truth, and
then route the action through native `tunnel-client` commands.
When the plugin is already installed and the user asks how to create, connect,
list, inspect, stop, remove, or debug a runtime, choose
`references/runtime-flows.md` and answer with `tunnel-client runtimes ...`
commands.

Binary setup order:

- first try the existing binary discovery path:
  `--tunnel-client-bin`, `TUNNEL_CLIENT_BIN`, the installed bundle hint,
  adjacent build outputs, then `PATH`
- when the plugin is already installed, prefer `scripts/tunnel_mcp ...` and the
  installed `.tunnel-client-bin` hint over `command -v tunnel-client`; ambient
  `PATH` can point at a different binary than the installed plugin bundle
- if the binary is missing, consult `references/binary.md`
- do not auto-download, auto-clone, or execute remote binaries just because the
  plugin cannot find `tunnel-client`
- if the user explicitly asks Codex to set up or install `tunnel-client`, Codex
  may clone and build it from `https://github.com/openai/tunnel-client`
- after building, set `TUNNEL_CLIENT_BIN` or reinstall the plugin with
  `--tunnel-client-bin`

Missing-binary response contract:

- If the prompt says the plugin cannot find `tunnel-client`, says
  `command -v tunnel-client` fails, or asks how to install/download the binary,
  do not answer with generic "public distribution" wording alone.
- Include these exact public-safe anchors in the answer:
  - `https://github.com/openai/tunnel-client/releases/latest`
  - `https://github.com/openai/tunnel-client`
  - `git clone https://github.com/openai/tunnel-client.git`
  - `go build -o bin/tunnel-client ./cmd/client`
  - Windows: `go build -o bin/tunnel-client.exe ./cmd/client`
  - `TUNNEL_CLIENT_BIN`
  - `--tunnel-client-bin /path/to/tunnel-client`
- Also say that routed plugin commands do not auto-download, auto-clone, or
  auto-run remote binaries. Codex should only clone/build when the user
  explicitly asks it to set up or install `tunnel-client`.
- If the user asks only for guidance, keep the answer guidance-only. If the
  user explicitly asks Codex to install or set up `tunnel-client`, Codex may
  follow the public repo build path above.

Preferred install surfaces:

- `tunnel-client codex plugin install` when the binary is available
- `tunnel-client codex plugin uninstall` when the installed plugin should be reset or removed
- `./plugins/tunnel-mcp/scripts/install_plugin.sh --tunnel-client-bin /path/to/tunnel-client` from a source checkout
- `sh scripts/install_plugin.sh --tunnel-client-bin /path/to/tunnel-client` from an exported plugin bundle root on macOS/Linux
- `powershell -NoProfile -ExecutionPolicy Bypass -File .\scripts\Install-Plugin.ps1 --tunnel-client-bin C:\path\to\tunnel-client.exe` from an exported plugin bundle root on Windows

## Rules

- Use `tunnel-client admin tunnels` for remote tunnel CRUD. Do not call raw
  tunnel-service HTTP endpoints from this plugin.
- Route operational actions through the public native CLI:
  `tunnel-client runtimes ...` and `tunnel-client admin-profiles ...`.
- Use native `tunnel-client run --profile <name>` for runtime processes. Do not
  use a helper shim that translates profile files into flags.
- Do not assume a specific source checkout, build system, helper, or
  tmux is available. The installed plugin must work with `tunnel-client` alone;
  the public fallback install path should stay shell/PowerShell-first and delegate to the selected `tunnel-client` binary.
- Use tmux when available; otherwise start `tunnel-client run --profile <name>`
  as a detached background process and report the PID/log path.
- Tunnel state is owned by `tunnel-client`. By default it lives under
  `TUNNEL_CLIENT_STATE_DIR` or the platform state directory, and legacy
  `CODEX_HOME` / `~/.codex/tunnel-mcp` state is reused when it already exists.
- Admin CRUD configuration is owned by the native `admin-profiles` commands.
  Use `--admin-profile <name>` to select a profile and `--admin-key env:NAME`
  or `--admin-key file:/path` to store a non-default admin key reference. Do
  not pass literal admin keys.
- Preserve the link between tunnels and admin CRUD credentials by keeping
  `admin_profile` on alias and process records.
- Write generated runtime YAML to the native tunnel-client profile directory:
  `TUNNEL_CLIENT_PROFILE_DIR` when set, otherwise `$XDG_CONFIG_HOME/tunnel-client`,
  otherwise `~/.config/tunnel-client`. Keep `profile_name` and `profile_path` on
  alias and process records.
- Use `--runtime-api-key env:NAME` or `--runtime-api-key file:/path` when a
  runtime tunnel key should differ from the default `env:CONTROL_PLANE_API_KEY`.
  Use `connect --tunnel-id <id>` to attach to a known existing tunnel without
  admin CRUD.
- For permissions, keep this split:
  - existing-tunnel runtime/connect flows need a runtime key whose principal has
    Tunnels Read + Use for that tunnel
  - create/list/update/delete remote tunnel flows need an admin key plus
    Tunnels Read + Manage
  - ChatGPT connector setup also needs Tunnels Read + Use and the tunnel's
    workspace ID attached to the tunnel metadata
  - users who create admin keys need Platform admin-key permission separately
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
  --profile sample_mcp_with_dcr \
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
