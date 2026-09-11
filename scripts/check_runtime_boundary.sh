#!/usr/bin/env bash
set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
if [[ -n "${TEST_SRCDIR:-}" && -n "${TEST_WORKSPACE:-}" &&
  -f "${TEST_SRCDIR}/${TEST_WORKSPACE}/api/tunnel-client/scripts/runtime_runfiles.sh" ]]; then
  SCRIPT_DIR="${TEST_SRCDIR}/${TEST_WORKSPACE}/api/tunnel-client/scripts"
fi
# shellcheck source=runtime_runfiles.sh
source "${SCRIPT_DIR}/runtime_runfiles.sh"
materialize_tunnel_client_runfiles "${SCRIPT_DIR}" || exit 1
SCRIPT_DIR="${RUNTIME_SCRIPT_DIR}"
PROJECT_ROOT="${RUNTIME_PROJECT_ROOT}"
readonly SCRIPT_DIR
readonly PROJECT_ROOT
trap runtime_runfiles_cleanup EXIT
use_tunnel_client_bazel_go_sdk || exit 1
readonly -a PLATFORMS=(
  "linux/amd64"
  "linux/arm64"
  "darwin/amd64"
  "darwin/arm64"
  "windows/amd64"
  "windows/arm64"
)

usage() {
  cat <<'EOF'
Usage:
  ./scripts/check_runtime_boundary.sh [--flavor runtime|runtime-cloudflared|all] [--platform <goos>/<goarch>] [--binary <path>] [--dependency-json-dir <directory>]

Checks the first-party Go dependency closure for every release platform by
default, or for one explicit release platform when --platform is supplied.
The normal runtime must stay free of support, development, and companion
packages. The companion runtime may add only its runtime-specific package.
When --binary is supplied, the same public-safe gate validates the already
built artifact's first-party markers instead of rebuilding its closure.
With one explicit flavor, --dependency-json-dir writes fresh Go dependency
JSON into a new absolute directory after each platform passes its gate.
EOF
}

die() {
  echo "check_runtime_boundary.sh: $*" >&2
  exit 1
}

[[ -f "${PROJECT_ROOT}/go.mod" ]] ||
  die "project root is missing go.mod: ${PROJECT_ROOT}"
[[ -x "${SCRIPT_DIR}/check_runtime_boundary.sh" ]] ||
  die "script runfile is missing: ${SCRIPT_DIR}/check_runtime_boundary.sh"

flavor="all"
binary=""
platform=""
dependency_json_dir=""
while [[ $# -gt 0 ]]; do
  case "$1" in
    --flavor)
      flavor="${2:-}"
      shift 2
      ;;
    --binary)
      binary="${2:-}"
      shift 2
      ;;
    --platform)
      platform="${2:-}"
      shift 2
      ;;
    --platform=*)
      platform="${1#*=}"
      shift
      ;;
    --dependency-json-dir)
      [[ $# -ge 2 && -n "$2" ]] || die "--dependency-json-dir requires a directory"
      dependency_json_dir="$2"
      shift 2
      ;;
    -h|--help)
      usage
      exit 0
      ;;
    *)
      usage >&2
      die "unknown argument: $1"
      ;;
  esac
done

case "${flavor}" in
  runtime|runtime-cloudflared|all) ;;
  *) die "--flavor must be runtime, runtime-cloudflared, or all" ;;
esac
case "${platform}" in
  ""|linux/amd64|linux/arm64|darwin/amd64|darwin/arm64|windows/amd64|windows/arm64) ;;
  *) die "--platform must be one supported goos/goarch release platform" ;;
esac

if [[ -n "${dependency_json_dir}" ]]; then
  [[ "${flavor}" != "all" ]] || die "--dependency-json-dir requires one explicit flavor"
  [[ -z "${binary}" ]] || die "--dependency-json-dir cannot be combined with --binary"
  [[ "${dependency_json_dir}" == /* ]] || die "--dependency-json-dir must be absolute"
  [[ ! -e "${dependency_json_dir}" && ! -L "${dependency_json_dir}" ]] ||
    die "--dependency-json-dir must not already exist"
  if [[ -z "${TUNNEL_CLIENT_RUNTIME_PYTHON:-}" ]]; then
    command -v python3 >/dev/null 2>&1 || die "python3 is required"
  fi
fi

selected_platforms=("${PLATFORMS[@]}")
if [[ -n "${platform}" ]]; then
  selected_platforms=("${platform}")
fi

if [[ -n "${binary}" ]]; then
  [[ "${flavor}" != "all" ]] || die "--binary requires one explicit flavor"
  [[ -z "${platform}" ]] || die "--platform cannot be combined with --binary"
  marker_script="${SCRIPT_DIR}/check_runtime_binary_markers.sh"
  if [[ ! -x "${marker_script}" && -x "${SCRIPT_DIR}/scripts/check_runtime_binary_markers.sh" ]]; then
    marker_script="${SCRIPT_DIR}/scripts/check_runtime_binary_markers.sh"
  fi
  [[ -x "${marker_script}" ]] || die "check_runtime_binary_markers.sh is required"
  "${marker_script}" --flavor "${flavor}" --binary "${binary}"
  printf '%s dependency boundary: built-artifact evidence passed\n' "${flavor}"
  exit 0
fi

command -v go >/dev/null 2>&1 || die "go is required"

cd "${PROJECT_ROOT}"
readonly GO_CACHE_DIR="${GOCACHE:-${TMPDIR:-/tmp}/tunnel-client-runtime-go-cache}"
readonly GO_MOD_CACHE_DIR="${GOMODCACHE:-${TMPDIR:-/tmp}/tunnel-client-runtime-go-mod-cache}"
mkdir -p "${GO_CACHE_DIR}" "${GO_MOD_CACHE_DIR}"
readonly GO_MOD_FLAG="$(runtime_go_mod_flag_for_root "${PROJECT_ROOT}")"
readonly MODULE_PATH="$(env GOWORK=off GOCACHE="${GO_CACHE_DIR}" GOMODCACHE="${GO_MOD_CACHE_DIR}" go list -m -f '{{.Path}}')"
[[ -n "${MODULE_PATH}" ]] || die "could not determine the Go module path"
if [[ -n "${dependency_json_dir}" ]]; then
  mkdir -m 700 "${dependency_json_dir}" ||
    die "could not create dependency JSON directory: ${dependency_json_dir}"
fi

dependency_json_import_paths() {
  runtime_python - "$1" <<'PY'
import json
import pathlib
import sys

payload = pathlib.Path(sys.argv[1]).read_text(encoding="utf-8")
decoder = json.JSONDecoder()
offset = 0
while offset < len(payload):
    while offset < len(payload) and payload[offset].isspace():
        offset += 1
    if offset == len(payload):
        break
    try:
        record, offset = decoder.raw_decode(payload, offset)
    except json.JSONDecodeError:
        raise SystemExit("go list output is not valid JSON")
    if not isinstance(record, dict):
        raise SystemExit("go list output record is not an object")
    import_path = record.get("ImportPath")
    if not isinstance(import_path, str):
        raise SystemExit("go list output record is missing ImportPath")
    print(import_path)
PY
}

# Exclusion helpers set the caller-local reason on a matching path.
common_exclusion_reason() {
  local relative_path="$1"
  reason=""

  case "${relative_path}" in
    pkg/app|pkg/app/*)
      reason="full application wiring"
      return 0
      ;;
    pkg/config|pkg/config/*)
      reason="full configuration"
      return 0
      ;;
    pkg/health|pkg/health/*)
      reason="full health surface"
      return 0
      ;;
    pkg/harpoon|pkg/harpoon/*)
      reason="full Harpoon adapter"
      return 0
      ;;
    pkg/proxyhealth|pkg/proxyhealth/*)
      reason="full proxy health surface"
      return 0
      ;;
    cmd/client|cmd/client/*)
      reason="full command tree"
      return 0
      ;;
  esac

  if [[ "${relative_path}" =~ (^|/)(adminui|plugins|localproxy|docs|examples|e2e|tests|testdata|testsupport)(/|$) ]]; then
    reason="support or development surface"
    return 0
  fi
  if [[ "${relative_path}" =~ (^|/)codex[^/]*(/|$) ]]; then
    reason="Codex surface"
    return 0
  fi

  return 1
}

cloudflared_exclusion_reason() {
  local selected_flavor="$1"
  local relative_path="$2"
  reason=""

  [[ "${relative_path}" =~ ^pkg/cloudflared(/|$) ]] || return 1

  if [[ "${selected_flavor}" == "runtime" ]]; then
    reason="companion package"
    return 0
  fi

  if [[ "${relative_path}" == "pkg/cloudflared/runtime" ||
    "${relative_path}" == pkg/cloudflared/runtime/* ]]; then
    return 1
  fi

  reason="unapproved companion package"
  return 0
}

check_flavor() {
  local selected_flavor="$1"
  local target=""
  case "${selected_flavor}" in
    runtime)
      target="./cmd/client-runtime"
      ;;
    runtime-cloudflared)
      target="./cmd/client-runtime-cloudflared"
      ;;
    *)
      die "unexpected flavor: ${selected_flavor}"
      ;;
  esac

  local platform goos goarch import_path relative_path reason
  local package_list package_output
  local -a format_args=(-f '{{.ImportPath}}')
  package_list="$(mktemp)"
  package_output="${package_list}"
  if [[ -n "${dependency_json_dir}" ]]; then
    format_args=(-json)
    package_output="${dependency_json_dir}/.platform.json"
  fi

  for platform in "${selected_platforms[@]}"; do
    goos="${platform%/*}"
    goarch="${platform#*/}"
    if ! env \
      GOWORK=off \
      GOCACHE="${GO_CACHE_DIR}" \
      GOMODCACHE="${GO_MOD_CACHE_DIR}" \
      GOOS="${goos}" \
      GOARCH="${goarch}" \
      CGO_ENABLED=0 \
      go list -buildvcs=false "${GO_MOD_FLAG}" -deps "${format_args[@]}" "${target}" >"${package_output}"; then
      die "${selected_flavor} dependency listing failed for ${platform}"
    fi
    if [[ -n "${dependency_json_dir}" ]] &&
      ! dependency_json_import_paths "${package_output}" >"${package_list}"; then
      die "${selected_flavor} dependency JSON is invalid for ${platform}"
    fi

    while IFS= read -r import_path; do
      [[ "${import_path}" == "${MODULE_PATH}" || "${import_path}" == "${MODULE_PATH}/"* ]] || continue
      if [[ "${import_path}" == "${MODULE_PATH}" ]]; then
        relative_path="."
      else
        relative_path="${import_path#"${MODULE_PATH}/"}"
      fi

      if common_exclusion_reason "${relative_path}"; then
        die "${selected_flavor} dependency boundary failed for ${platform}: ${relative_path} (${reason})"
      fi
      if cloudflared_exclusion_reason "${selected_flavor}" "${relative_path}"; then
        die "${selected_flavor} dependency boundary failed for ${platform}: ${relative_path} (${reason})"
      fi
    done <"${package_list}"

    if [[ -n "${dependency_json_dir}" ]]; then
      mv "${package_output}" "${dependency_json_dir}/${goos}_${goarch}.json"
    fi
    printf '%s dependency boundary: %s passed\n' "${selected_flavor}" "${platform}"
  done

  rm -f "${package_list}"
}

if [[ "${flavor}" == "all" || "${flavor}" == "runtime" ]]; then
  check_flavor runtime
fi
if [[ "${flavor}" == "all" || "${flavor}" == "runtime-cloudflared" ]]; then
  check_flavor runtime-cloudflared
fi
