#!/usr/bin/env bash
set -euo pipefail

if [[ $# -ne 0 ]]; then
  echo "usage: $0" >&2
  exit 1
fi

require_command() {
  local command_name="$1"
  if ! command -v "${command_name}" >/dev/null 2>&1; then
    echo "missing required command: ${command_name}" >&2
    exit 1
  fi
}

require_command pandoc

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
CLIENT_ROOT="$(cd "${SCRIPT_DIR}/.." && pwd)"
GUIDE_PATH="${CLIENT_ROOT}/docs/end-user-guide.md"
HTML_OUTPUT="${CLIENT_ROOT}/docs/output/end-user-guide.html"
LEGACY_PDF_OUTPUT="${CLIENT_ROOT}/docs/output/end-user-guide.pdf"
CSS_PATH="${CLIENT_ROOT}/docs/pdf/tunnel-guide.css"

if [[ ! -f "${GUIDE_PATH}" ]]; then
  echo "missing guide source: ${GUIDE_PATH}" >&2
  exit 1
fi

if [[ ! -f "${CSS_PATH}" ]]; then
  echo "missing guide stylesheet: ${CSS_PATH}" >&2
  exit 1
fi

mkdir -p "$(dirname "${HTML_OUTPUT}")"
rm -f "${LEGACY_PDF_OUTPUT}"

cd "${CLIENT_ROOT}"

pandoc "${GUIDE_PATH}" \
  --from=gfm+smart \
  --to=html5 \
  --standalone \
  --embed-resources \
  --resource-path="${CLIENT_ROOT}/docs:${CLIENT_ROOT}/docs/pdf" \
  --css="../pdf/tunnel-guide.css" \
  --metadata pagetitle="Tunnel End-User Guide" \
  --output="${HTML_OUTPUT}"

if [[ ! -s "${HTML_OUTPUT}" ]]; then
  echo "render failed; HTML output is missing or empty: ${HTML_OUTPUT}" >&2
  exit 1
fi

echo "html: ${HTML_OUTPUT}"
