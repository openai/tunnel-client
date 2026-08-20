# Setup and install

Use the binary-owned install path when a `tunnel-client` binary is available:

- `tunnel-client codex plugin install`
- `tunnel-client codex plugin uninstall`
- `tunnel-client codex status`
- `tunnel-client codex diagnose --json`

Use the exported bundle only when the binary is not already installed or when
you need to inspect the plugin contents first:

- `tunnel-client codex plugin export --dir /tmp/tunnel-mcp`
- `cd /tmp/tunnel-mcp && sh scripts/install_plugin.sh --tunnel-client-bin /path/to/tunnel-client`
- `Set-Location C:\tmp\tunnel-mcp; powershell -NoProfile -ExecutionPolicy Bypass -File .\scripts\Install-Plugin.ps1 --tunnel-client-bin C:\path\to\tunnel-client.exe`

After install or export, verify the persisted `.tunnel-client-bin` hint before
using the router:

- `test -f "$CODEX_HOME/plugins/cache/<marketplace>/tunnel-mcp/<version>/.tunnel-client-bin"`
- `scripts/tunnel_mcp self-check`

Prefer the installed plugin router and persisted `.tunnel-client-bin` hint over
an ambient `tunnel-client` found on `PATH`.
If `.tunnel-client-bin` is missing, routed commands report any ambient
`PATH` candidate but do not execute it. Set `TUNNEL_CLIENT_BIN` or pass
`--tunnel-client-bin` to select that binary explicitly.

`tunnel-client codex status` reports the enabled plugin config keys and
understands marketplace cache keys such as `tunnel-mcp@example-marketplace`
as well as local debug installs. It flags enabled config entries whose cache
manifest is missing.

`tunnel-client codex diagnose --json` reports the loaded plugin source,
enabled config keys, cache path, resolved binary, binary version,
`.tunnel-client-bin` hint, state root, profile dir, Codex bridge state, and
current health URL when an alias or health URL is supplied.

`scripts/tunnel_mcp diagnose` negotiates binary compatibility. If the selected
binary lacks `tunnel-client codex diagnose --plugin-root`, the router falls
back to the supported `codex diagnose --json` or `codex status --json` path and
reports the exact unsupported flag, `--plugin-root`, in JSON.

The plugin is a thin router. It does not own protocol logic. After install,
use the native CLI for runtime operations:

- `tunnel-client runtimes create ...`
- `tunnel-client runtimes connect ...`
- `tunnel-client runtimes list`
- `tunnel-client runtimes cleanup`
- `tunnel-client admin-profiles list`

If Codex already had the plugin loaded, restart the existing Codex session
after install or uninstall so the loaded plugin inventory matches the on-disk
bundle.

If `tunnel-client` itself is missing, consult `binary.md` for public-safe
release and source-build paths before trying to use the plugin.
