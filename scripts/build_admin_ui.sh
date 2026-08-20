#!/usr/bin/env bash
set -euo pipefail

if [[ $# -ne 2 ]]; then
  echo "usage: $0 <ui_dir> <assets_dir>" >&2
  exit 1
fi

UI_DIR="$1"
ASSETS_DIR="$2"
PNPM_BIN="${PNPM:-pnpm}"
PNPM_INSTALL_FLAGS=(--config.shared-workspace-lockfile=false --config.confirmModulesPurge=false)

WORK_DIR="$(mktemp -d)"
cleanup() {
  rm -rf "${WORK_DIR}"
}
trap cleanup EXIT

cp -RL "${UI_DIR}" "${WORK_DIR}/ui"
chmod -R u+w "${WORK_DIR}/ui"

"${PNPM_BIN}" --dir "${WORK_DIR}/ui" install --frozen-lockfile "${PNPM_INSTALL_FLAGS[@]}"
ADMIN_UI_ASSETS_DIR="${ASSETS_DIR}" "${PNPM_BIN}" --dir "${WORK_DIR}/ui" build
