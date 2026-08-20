# Tunnel MCP Plugin Agent Instructions

- This `api/tunnel-client/plugins/tunnel-mcp` path is the public-safe plugin
  copy mirrored into the standalone `openai/tunnel-client` repo and exported by
  `tunnel-client codex plugin export`.
- Do not add internal package-manager wording, internal-repo install wording,
  or internal marketplace/install references in this path.
- Keep user-facing install and upgrade docs in `README.md` easy to follow from
  this repository root and from a standalone `tunnel-client` checkout.
- Avoid referring to the larger repository structure in user-facing plugin
  installation instructions. Use terms like "this repository root", "source
  checkout", or "standalone `tunnel-client` checkout" instead.
- Do not introduce non-public repository layout, package-manager,
  mirror-pipeline, or source-tree references in plugin code, docs,
  prompts, examples, or runtime output.
- Keep binary-owned install/uninstall paths primary, and use POSIX shell or
  PowerShell wrapper examples for exported/source-checkout fallback flows. Do
  not surface `python3 scripts/install_plugin.py` as the main public path.
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
- For installed-plugin runtime questions, answer from
  `skills/tunnel-mcp/references/runtime-flows.md` and use
  `tunnel-client runtimes ...` commands for create, connect, list, status,
  stop, and remove operations.
