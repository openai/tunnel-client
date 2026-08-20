#!/usr/bin/env bash
set -euo pipefail

if [[ $# -ne 0 ]]; then
  echo "usage: $0" >&2
  exit 1
fi

require_path() {
  local path="$1"
  local kind="$2"
  if [[ ! -e "${path}" ]]; then
    echo "missing ${kind}: ${path}" >&2
    exit 1
  fi
}

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
CLIENT_ROOT="$(cd "${SCRIPT_DIR}/.." && pwd)"
OUTPUT_ROOT="${CLIENT_ROOT}/docs/output"
PAGES_DIR="${OUTPUT_ROOT}/end-user-guide-pages"
PPTX_OUTPUT="${OUTPUT_ROOT}/end-user-guide-slides.pptx"
SLIDES_SCRIPT="${CLIENT_ROOT}/scripts/render_end_user_guide_slides.mjs"
HTML_RENDER_SCRIPT="${CLIENT_ROOT}/scripts/render_end_user_guide_html.sh"
PAGES_RENDER_SCRIPT="${CLIENT_ROOT}/scripts/render_end_user_guide_pages.py"
RUNTIME_NODE="${NODE_BIN:-/Users/denyska/.cache/codex-runtimes/codex-primary-runtime/dependencies/node/bin/node}"
PYTHON_BIN="${PYTHON_BIN:-/Users/denyska/.cache/codex-runtimes/codex-primary-runtime/dependencies/python/bin/python3}"
PRESENTATIONS_ROOT="/Users/denyska/.codex/plugins/cache/openai-primary-runtime/presentations/26.426.12240"
WORKSPACE_HELPER="${PRESENTATIONS_ROOT}/skills/presentations/scripts/create_presentation_workspace.js"
WORKSPACE_ROOT="${OUTPUT_ROOT}/.slides-workspace"

require_path "${HTML_RENDER_SCRIPT}" "HTML render script"
require_path "${PAGES_RENDER_SCRIPT}" "guide page render script"
require_path "${SLIDES_SCRIPT}" "slides deck source"
require_path "${RUNTIME_NODE}" "Node runtime"
require_path "${PYTHON_BIN}" "Python runtime"
require_path "${WORKSPACE_HELPER}" "Presentations workspace helper"

mkdir -p "${OUTPUT_ROOT}"

# Keep the HTML archive and rendered page images current before packaging slides.
"${HTML_RENDER_SCRIPT}"
"${PYTHON_BIN}" "${PAGES_RENDER_SCRIPT}"

require_path "${PAGES_DIR}" "guide page image directory"
if ! find "${PAGES_DIR}" -maxdepth 1 -type f -name '*.png' | grep -q .; then
  echo "no rendered guide page images found under ${PAGES_DIR}" >&2
  exit 1
fi

rm -rf "${WORKSPACE_ROOT}"
"${RUNTIME_NODE}" "${WORKSPACE_HELPER}" \
  --deck-id tunnel-end-user-guide \
  --workspace "${WORKSPACE_ROOT}" \
  --force

cp "${SLIDES_SCRIPT}" "${WORKSPACE_ROOT}/src/render_end_user_guide_slides.mjs"
mkdir -p "${WORKSPACE_ROOT}/scratch/pages"
cp "${PAGES_DIR}"/*.png "${WORKSPACE_ROOT}/scratch/pages/"

(
  cd "${WORKSPACE_ROOT}"
  GUIDE_PAGES_DIR="scratch/pages" \
  GUIDE_PPTX_TITLE="Tunnel End-User Guide" \
    "${RUNTIME_NODE}" "${WORKSPACE_ROOT}/src/render_end_user_guide_slides.mjs"
)

"${PYTHON_BIN}" - "${WORKSPACE_ROOT}/output/output.pptx" "${WORKSPACE_ROOT}/scratch/pages" <<'PY'
from pathlib import Path
import sys
import zipfile

pptx_path = Path(sys.argv[1])
pages_dir = Path(sys.argv[2])
page_images = sorted(pages_dir.glob("*.png"))
if not page_images:
    raise SystemExit(f"no page images found under {pages_dir}")

tmp_path = pptx_path.with_suffix(".tmp")
with zipfile.ZipFile(pptx_path, "r") as zin, zipfile.ZipFile(tmp_path, "w") as zout:
    for info in zin.infolist():
        data = zin.read(info.filename)
        if info.filename.startswith("ppt/media/image") and info.filename.endswith(".png"):
            media_name = Path(info.filename).name
            if media_name == "image.png":
                image_index = 0
            else:
                image_index = int(media_name.removeprefix("image").split(".")[0]) - 1
            if image_index < 0 or image_index >= len(page_images):
                raise SystemExit(f"media slot {media_name} has no matching page image")
            data = page_images[image_index].read_bytes()
        zout.writestr(info, data)

tmp_path.replace(pptx_path)
PY

require_path "${WORKSPACE_ROOT}/output/output.pptx" "generated slide deck"
cp "${WORKSPACE_ROOT}/output/output.pptx" "${PPTX_OUTPUT}"

if [[ ! -s "${PPTX_OUTPUT}" ]]; then
  echo "render failed; slides output is missing or empty: ${PPTX_OUTPUT}" >&2
  exit 1
fi

echo "slides: ${PPTX_OUTPUT}"
