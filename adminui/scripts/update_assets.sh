#!/usr/bin/env bash
set -euo pipefail

if [[ $# -ne 4 ]]; then
  echo "usage: $0 <app_js> <styles_css> <index_html> <assets_dir>" >&2
  exit 1
fi

if [[ -z "${BUILD_WORKSPACE_DIRECTORY:-}" ]]; then
  echo "BUILD_WORKSPACE_DIRECTORY is not set; run with bazel run" >&2
  exit 1
fi

APP_JS="$1"
STYLES_CSS="$2"
INDEX_HTML="$3"
ASSETS_DIR="${BUILD_WORKSPACE_DIRECTORY}/$4"

mkdir -p "${ASSETS_DIR}"
cp "${APP_JS}" "${ASSETS_DIR}/app.js"
cp "${STYLES_CSS}" "${ASSETS_DIR}/styles.css"
cp "${INDEX_HTML}" "${ASSETS_DIR}/index.html"
