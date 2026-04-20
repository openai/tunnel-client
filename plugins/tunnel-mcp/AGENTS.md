# Tunnel MCP Plugin Agent Instructions

- Keep user-facing install and upgrade docs in `README.md` easy to follow from
  this repository root and from a standalone `tunnel-client` checkout.
- Avoid referring to the larger repository structure in user-facing plugin
  installation instructions. Use terms like "this repository root", "source
  checkout", or "standalone `tunnel-client` checkout" instead.
- Document upgrade as rerunning the installer against the newer plugin source.
  The installer should replace only
  `$CODEX_HOME/plugins/cache/<marketplace>/tunnel-mcp/local` and keep runtime
  state under `$CODEX_HOME/tunnel-mcp` or `~/.codex/tunnel-mcp` intact.
- Include post-install and post-upgrade verification commands that check
  manifest presence, config enablement, and `scripts/tunnel_mcp --help`.
