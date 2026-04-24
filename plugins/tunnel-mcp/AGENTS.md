# Tunnel MCP Plugin Agent Instructions

- Keep user-facing install and upgrade docs in `README.md` easy to follow from
  this repository root and from a standalone `tunnel-client` checkout.
- Avoid referring to the larger repository structure in user-facing plugin
  installation instructions. Use terms like "this repository root", "source
  checkout", or "standalone `tunnel-client` checkout" instead.
- Document upgrade as rerunning the installer against the newer plugin source.
  The installer should replace only
  `$CODEX_HOME/plugins/cache/<marketplace>/tunnel-mcp/local` and keep runtime
  state under `TUNNEL_CLIENT_STATE_DIR`, the platform state directory, or
  reused legacy `CODEX_HOME` / `~/.codex/tunnel-mcp` roots intact.
- Include post-install and post-upgrade verification commands that check
  manifest presence, config enablement, and `scripts/tunnel_mcp --help`.
- If the prompt is about a missing `tunnel-client` binary, `command -v
  tunnel-client` failing, or how to install/download the binary for the
  plugin, include the exact public-safe anchors instead of generic
  "public distribution" wording:
  - `https://github.com/openai/tunnel-client/releases/latest`
  - `https://github.com/openai/tunnel-client`
  - `git clone https://github.com/openai/tunnel-client.git`
  - `go build -o bin/tunnel-client ./cmd/client`
  - Windows: `go build -o bin/tunnel-client.exe ./cmd/client`
  - `TUNNEL_CLIENT_BIN`
  - `--tunnel-client-bin /path/to/tunnel-client`
- Keep the no-auto-download rule explicit: the routed plugin commands do not
  auto-download, auto-clone, or auto-run remote binaries by themselves.
  Codex may only clone/build from the public repo when the user explicitly asks
  it to set up or install `tunnel-client`.
