#!/usr/bin/env bash
set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
if [[ "$#" -ne 3 ]]; then
  echo "usage: $0 <typescript-package-dir> <node-types-package-dir> <tunnel-client>" >&2
  exit 2
fi

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

TYPESCRIPT_DIR="$1"
NODE_TYPES_DIR="$2"
TUNNEL_CLIENT_BIN="$3"
NODE_BIN="$(resolve_node)"
if [[ "${TYPESCRIPT_DIR}" != /* ]]; then
  TYPESCRIPT_DIR="${PWD}/${TYPESCRIPT_DIR}"
fi
if [[ "${NODE_TYPES_DIR}" != /* ]]; then
  NODE_TYPES_DIR="${PWD}/${NODE_TYPES_DIR}"
fi
if [[ "${TUNNEL_CLIENT_BIN}" != /* ]]; then
  TUNNEL_CLIENT_BIN="${PWD}/${TUNNEL_CLIENT_BIN}"
fi

TMP_DIR="$(mktemp -d)"
cleanup() {
  rm -rf "${TMP_DIR}"
}
trap cleanup EXIT

printf '{"type":"module"}\n' > "${TMP_DIR}/package.json"
"${NODE_BIN}" "${TYPESCRIPT_DIR}/bin/tsc" \
  --target ES2022 \
  --module NodeNext \
  --moduleResolution NodeNext \
  --strict \
  --skipLibCheck \
  --types node \
  --typeRoots "$(dirname "${NODE_TYPES_DIR}")" \
  --rootDir "${PWD}/api/tunnel-client" \
  --outDir "${TMP_DIR}" \
  "${SCRIPT_DIR}/example.ts"

"${NODE_BIN}" \
  "${SCRIPT_DIR}/example_test.mjs" \
  "${TMP_DIR}/examples/typescript-mcp-tunnel-client-proxy/example.js" \
  "${TUNNEL_CLIENT_BIN}"
