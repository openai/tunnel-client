#!/usr/bin/env bash
set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"

find_root_dir() {
  local dir
  dir="$1"
  while [[ "${dir}" != "/" ]]; do
    if [[ -d "${dir}/adminui" && -d "${dir}/pkg/adminui/assets" ]]; then
      echo "${dir}"
      return 0
    fi
    dir="$(dirname "${dir}")"
  done
  return 1
}

ROOT_DIR="$(find_root_dir "${SCRIPT_DIR}" || true)"
if [[ -z "${ROOT_DIR}" ]]; then
  echo "could not locate api/tunnel-client root from ${SCRIPT_DIR}" >&2
  exit 1
fi
SCRIPTS_DIR="${ROOT_DIR}/scripts"
UI_DIR="${ROOT_DIR}/adminui"
ASSETS_DIR="${ROOT_DIR}/pkg/adminui/assets"
BUILD_SCRIPT="${SCRIPTS_DIR}/build_admin_ui.sh"
REBUILD_HINT="${ADMIN_UI_REBUILD_HINT:-}"

TMP_DIRS=()
cleanup() {
  for dir in "${TMP_DIRS[@]}"; do
    rm -rf "${dir}"
  done
}
trap cleanup EXIT

if [[ $# -gt 0 ]]; then
  GENERATED_ASSETS_DIR="$(mktemp -d)"
  TMP_DIRS+=("${GENERATED_ASSETS_DIR}")
  for generated_asset in "$@"; do
    if [[ ! -f "${generated_asset}" ]]; then
      echo "missing generated asset: ${generated_asset}" >&2
      exit 1
    fi
    cp "${generated_asset}" "${GENERATED_ASSETS_DIR}/$(basename "${generated_asset}")"
  done
else
  if [[ ! -x "${BUILD_SCRIPT}" ]]; then
    echo "missing helper script: ${BUILD_SCRIPT}" >&2
    exit 1
  fi
  GENERATED_ASSETS_DIR="$(mktemp -d)"
  TMP_DIRS+=("${GENERATED_ASSETS_DIR}")
  "${BUILD_SCRIPT}" "${UI_DIR}" "${GENERATED_ASSETS_DIR}"
fi

if ! diff -ru "${ASSETS_DIR}" "${GENERATED_ASSETS_DIR}" >/dev/null; then
  echo "Admin UI assets are out of date."
  if [[ -n "${REBUILD_HINT}" ]]; then
    echo "Rebuild command: ${REBUILD_HINT}"
  fi
  exit 1
fi
