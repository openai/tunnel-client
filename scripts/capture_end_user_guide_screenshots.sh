#!/usr/bin/env bash
set -euo pipefail

usage() {
  cat >&2 <<'EOF'
usage: capture_end_user_guide_screenshots.sh [--output-dir <dir>] [--port <port>] [--chrome-bin <path>] [--python-bin <path>]
EOF
}

require_command() {
  local command_name="$1"
  if ! command -v "${command_name}" >/dev/null 2>&1; then
    echo "missing required command: ${command_name}" >&2
    exit 1
  fi
}

OUTPUT_DIR=""
PORT="18080"
CHROME_BIN="${CHROME_BIN:-/Applications/Google Chrome.app/Contents/MacOS/Google Chrome}"
PYTHON_BIN="${PYTHON_BIN:-/Users/denyska/.cache/codex-runtimes/codex-primary-runtime/dependencies/python/bin/python3}"

while [[ $# -gt 0 ]]; do
  case "$1" in
    --output-dir)
      OUTPUT_DIR="$2"
      shift 2
      ;;
    --port)
      PORT="$2"
      shift 2
      ;;
    --chrome-bin)
      CHROME_BIN="$2"
      shift 2
      ;;
    --python-bin)
      PYTHON_BIN="$2"
      shift 2
      ;;
    -h|--help)
      usage
      exit 0
      ;;
    *)
      usage
      echo "unknown argument: $1" >&2
      exit 1
      ;;
  esac
done

require_command git
require_command curl
require_command go
require_command make

if [[ ! -x "${CHROME_BIN}" ]]; then
  echo "missing Chrome binary; set CHROME_BIN or pass --chrome-bin" >&2
  exit 1
fi

if [[ ! -x "${PYTHON_BIN}" ]]; then
  echo "missing Python runtime with Pillow; set PYTHON_BIN or pass --python-bin" >&2
  exit 1
fi

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
CLIENT_ROOT="$(cd "${SCRIPT_DIR}/.." && pwd)"
OUTPUT_DIR="${OUTPUT_DIR:-${CLIENT_ROOT}/docs/screenshots}"
if [[ "${OUTPUT_DIR}" != /* ]]; then
  OUTPUT_DIR="${CLIENT_ROOT}/${OUTPUT_DIR}"
fi
TMP_DIR="$(mktemp -d)"
TC_PID=""

cleanup() {
  if [[ -n "${TC_PID}" ]] && kill -0 "${TC_PID}" >/dev/null 2>&1; then
    kill "${TC_PID}" >/dev/null 2>&1 || true
    wait "${TC_PID}" >/dev/null 2>&1 || true
  fi
  rm -rf "${TMP_DIR}"
}
trap cleanup EXIT

mkdir -p "${OUTPUT_DIR}"
cd "${CLIENT_ROOT}"

make admin-ui
go build -o bin/tunnel-client ./cmd/client

CONTROL_PLANE_API_KEY=dummy-local-demo-key \
./bin/tunnel-client run \
  --embedded-mcp-stub \
  --control-plane.tunnel-id tunnel_0123456789abcdef0123456789abcdef \
  --health.listen-addr "127.0.0.1:${PORT}" \
  --health.url-file "${TMP_DIR}/health.url" \
  >"${TMP_DIR}/tunnel-client.log" 2>&1 &
TC_PID=$!

for _ in $(seq 1 60); do
  if curl -fsS "http://127.0.0.1:${PORT}/readyz" >/dev/null 2>&1; then
    break
  fi
  if ! kill -0 "${TC_PID}" >/dev/null 2>&1; then
    echo "tunnel-client exited before readiness; log follows:" >&2
    sed -n '1,200p' "${TMP_DIR}/tunnel-client.log" >&2
    exit 1
  fi
  sleep 1
done

if ! curl -fsS "http://127.0.0.1:${PORT}/readyz" >/dev/null 2>&1; then
  echo "tunnel-client never became ready; log follows:" >&2
  sed -n '1,240p' "${TMP_DIR}/tunnel-client.log" >&2
  exit 1
fi

capture_tab() {
  local hash_name="$1"
  local output_name="$2"
  local chrome_profile_dir="${TMP_DIR}/chrome-${hash_name}"
  local output_path="${OUTPUT_DIR}/${output_name}"
  local chrome_pid=""
  mkdir -p "${chrome_profile_dir}"
  rm -f "${output_path}"
  "${CHROME_BIN}" \
    --headless \
    --disable-gpu \
    --no-first-run \
    --no-default-browser-check \
    --disable-background-networking \
    --disable-default-apps \
    --disable-extensions \
    --disable-sync \
    --hide-scrollbars \
    --window-size=1600,1200 \
    --run-all-compositor-stages-before-draw \
    --virtual-time-budget=4000 \
    --timeout=5000 \
    --user-data-dir="${chrome_profile_dir}" \
    --screenshot="${output_path}" \
    "http://127.0.0.1:${PORT}/ui#${hash_name}" >/dev/null 2>&1 &
  chrome_pid=$!
  for _ in $(seq 1 30); do
    if [[ -s "${output_path}" ]]; then
      break
    fi
    if ! kill -0 "${chrome_pid}" >/dev/null 2>&1; then
      break
    fi
    sleep 1
  done
  if kill -0 "${chrome_pid}" >/dev/null 2>&1; then
    kill -9 "${chrome_pid}" >/dev/null 2>&1 || true
    wait "${chrome_pid}" >/dev/null 2>&1 || true
  fi
  if [[ ! -s "${output_path}" ]]; then
    echo "missing screenshot output: ${output_path}" >&2
    exit 1
  fi
}

capture_tab overview admin-overview.png
capture_tab metrics admin-metrics.png
capture_tab logs admin-logs.png
capture_tab codex admin-codex.png

"${PYTHON_BIN}" - "${OUTPUT_DIR}/admin-logs.png" <<'PY'
from pathlib import Path
import sys

from PIL import Image

path = Path(sys.argv[1])
img = Image.open(path).convert("P", palette=Image.Palette.ADAPTIVE, colors=128)
img.save(path, format="PNG", optimize=True)
PY

echo "screenshots:"
printf '  %s\n' \
  "${OUTPUT_DIR}/admin-overview.png" \
  "${OUTPUT_DIR}/admin-metrics.png" \
  "${OUTPUT_DIR}/admin-logs.png" \
  "${OUTPUT_DIR}/admin-codex.png"
