#!/usr/bin/env bash
set -euo pipefail

if [[ $# -ne 1 ]]; then
  echo "usage: $0 <vitest-bin>" >&2
  exit 1
fi

VITEST_BIN="$1"
if [[ "${VITEST_BIN}" != /* ]]; then
  VITEST_BIN="$(pwd)/${VITEST_BIN}"
fi

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"

find_root() {
  local dir="$1"
  while [[ "${dir}" != "/" ]]; do
    if [[ -f "${dir}/package.json" && -f "${dir}/vitest.config.ts" ]]; then
      echo "${dir}"
      return 0
    fi
    dir="$(dirname "${dir}")"
  done
  return 1
}

ROOT_DIR="$(find_root "${SCRIPT_DIR}" || true)"
if [[ -z "${ROOT_DIR}" ]]; then
  ROOT_DIR="$(find_root "$(pwd)" || true)"
fi
if [[ -z "${ROOT_DIR}" ]]; then
  ROOT_DIR="$(find_root "$(dirname "${VITEST_BIN}")" || true)"
fi
if [[ -z "${ROOT_DIR}" ]]; then
  echo "failed to locate adminui root (package.json + vitest.config.ts)" >&2
  exit 1
fi

CACHE_ROOT="$(mktemp -d)"
cleanup() {
  rm -rf "${CACHE_ROOT}"
}
trap cleanup EXIT

cd "${ROOT_DIR}"
export VITE_CACHE_DIR="${CACHE_ROOT}/vite"
export VITEST_CACHE_DIR="${CACHE_ROOT}/vitest"

"${VITEST_BIN}" run \
  --root "${ROOT_DIR}" \
  --config "${ROOT_DIR}/vitest.config.ts" \
  --reporter=dot
