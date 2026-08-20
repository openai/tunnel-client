#!/usr/bin/env bash
set -euo pipefail

typescript_dir="$1"
types_node_dir="$2"

resolve_node() {
  if [[ -n "${NODE_BINARY:-}" && -x "${NODE_BINARY}" ]]; then
    printf '%s\n' "${NODE_BINARY}"
    return
  fi
  if command -v node >/dev/null 2>&1; then
    command -v node
    return
  fi
  local root
  for root in "${RUNFILES_DIR:-}" "${TEST_SRCDIR:-}" "$PWD"; do
    if [[ -z "${root}" || ! -d "${root}" ]]; then
      continue
    fi
    local candidate
    candidate="$(find "${root}" -path '*/bin/nodejs/bin/node' -print -quit 2>/dev/null || true)"
    if [[ -n "${candidate}" && -x "${candidate}" ]]; then
      printf '%s\n' "${candidate}"
      return
    fi
  done
  echo "node binary not found in PATH or Bazel runfiles" >&2
  exit 127
}

node_bin="$(resolve_node)"
workdir="$(mktemp -d)"
trap 'rm -rf "$workdir"' EXIT

cp "$PWD/api/tunnel-client/wrappers/mcp-tunnel-client-proxy/typescript/mcp_tunnel_client_proxy.ts" "$workdir/"
cp "$PWD/api/tunnel-client/wrappers/mcp-tunnel-client-proxy/typescript/package.json" "$workdir/"

cat > "$workdir/tsconfig.json" <<JSON
{
  "compilerOptions": {
    "module": "NodeNext",
    "moduleResolution": "NodeNext",
    "target": "ES2022",
    "strict": true,
    "types": ["node"],
    "typeRoots": ["$PWD/$types_node_dir/.."],
    "skipLibCheck": true,
    "outDir": "dist"
  },
  "include": ["mcp_tunnel_client_proxy.ts"]
}
JSON

"$node_bin" "$PWD/$typescript_dir/bin/tsc" -p "$workdir/tsconfig.json"
"$node_bin" "$PWD/api/tunnel-client/wrappers/mcp-tunnel-client-proxy/typescript/mcp_tunnel_client_proxy_test.mjs" \
  "$workdir/dist/mcp_tunnel_client_proxy.js" \
  "$PWD/api/tunnel-client/wrappers/mcp-tunnel-client-proxy/typescript/fake_tunnel_client.mjs"
