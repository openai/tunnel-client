# Obtaining a tunnel-client binary

# Binary discovery

First try existing binary discovery:

- `--tunnel-client-bin /path/to/tunnel-client`
- `TUNNEL_CLIENT_BIN`
- the installed plugin bundle's `.tunnel-client-bin` hint
- adjacent local build outputs
- `PATH` diagnostic candidate only; select it with `TUNNEL_CLIENT_BIN` or
  `--tunnel-client-bin`

If the plugin is already installed, prefer the installed router and
`.tunnel-client-bin` hint before trusting `command -v tunnel-client`. `PATH`
may point at a different binary than the plugin bundle was installed with.

If `tunnel-client` is still missing, use one of these public-safe setup paths:

- latest releases: `https://github.com/openai/tunnel-client/releases/latest`
- public repo: `https://github.com/openai/tunnel-client`

Source build from the public repo:

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

After you have a binary:

- set `TUNNEL_CLIENT_BIN` to the full path to the binary
- or rerun the plugin/install command with `--tunnel-client-bin /path/to/tunnel-client`
- or reinstall the plugin with `--tunnel-client-bin /path/to/tunnel-client`
- then run `scripts/tunnel_mcp self-check` from the installed or exported
  plugin root to verify the selected path, binary version, plugin version,
  router compatibility, active admin profile, and runtime key reference
  presence without printing secret values

Executable naming:

- macOS/Linux: `tunnel-client`
- Windows: `tunnel-client.exe`

Do not auto-download, auto-clone, or auto-run remote binaries just because the
plugin cannot find `tunnel-client`. Only clone/build when the user explicitly
asks Codex to set up or install it.
