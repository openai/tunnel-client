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
  ./scripts/check_runtime_boundary.sh [--flavor runtime|runtime-cloudflared|all] [--binary <path>]

Checks the first-party Go dependency closure for every release platform.
The normal runtime must stay free of support, development, and companion
packages. The companion runtime may add only its runtime-specific package.
When --binary is supplied, the same public-safe gate validates the already
built artifact's first-party markers instead of rebuilding its closure.
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

if [[ -n "${binary}" ]]; then
  [[ "${flavor}" != "all" ]] || die "--binary requires one explicit flavor"
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

relative_import_path() {
  local import_path="$1"
  if [[ "${import_path}" == "${MODULE_PATH}" ]]; then
    printf '.\n'
    return 0
  fi
  if [[ "${import_path}" == "${MODULE_PATH}/"* ]]; then
    printf '%s\n' "${import_path#"${MODULE_PATH}/"}"
    return 0
  fi
  return 1
}

common_exclusion_reason() {
  local relative_path="$1"

  case "${relative_path}" in
    pkg/app|pkg/app/*)
      printf 'full application wiring'
      return 0
      ;;
    pkg/config|pkg/config/*)
      printf 'full configuration'
      return 0
      ;;
    pkg/health|pkg/health/*)
      printf 'full health surface'
      return 0
      ;;
    pkg/harpoon|pkg/harpoon/*)
      printf 'full Harpoon adapter'
      return 0
      ;;
    pkg/proxyhealth|pkg/proxyhealth/*)
      printf 'full proxy health surface'
      return 0
      ;;
    cmd/client|cmd/client/*)
      printf 'full command tree'
      return 0
      ;;
  esac

  if [[ "${relative_path}" =~ (^|/)(adminui|plugins|localproxy|docs|examples|e2e|tests|testdata|testsupport)(/|$) ]]; then
    printf 'support or development surface'
    return 0
  fi
  if [[ "${relative_path}" =~ (^|/)codex[^/]*(/|$) ]]; then
    printf 'Codex surface'
    return 0
  fi

  return 1
}

cloudflared_exclusion_reason() {
  local selected_flavor="$1"
  local relative_path="$2"

  [[ "${relative_path}" =~ ^pkg/cloudflared(/|$) ]] || return 1

  if [[ "${selected_flavor}" == "runtime" ]]; then
    printf 'companion package'
    return 0
  fi

  if [[ "${relative_path}" == "pkg/cloudflared/runtime" ||
    "${relative_path}" == pkg/cloudflared/runtime/* ]]; then
    return 1
  fi

  printf 'unapproved companion package'
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
  local package_list
  package_list="$(mktemp)"

  for platform in "${PLATFORMS[@]}"; do
    goos="${platform%/*}"
    goarch="${platform#*/}"
    if ! env \
      GOWORK=off \
      GOCACHE="${GO_CACHE_DIR}" \
      GOMODCACHE="${GO_MOD_CACHE_DIR}" \
      GOOS="${goos}" \
      GOARCH="${goarch}" \
      CGO_ENABLED=0 \
      go list -buildvcs=false "${GO_MOD_FLAG}" -deps -f '{{.ImportPath}}' "${target}" >"${package_list}"; then
      die "${selected_flavor} dependency listing failed for ${platform}"
    fi

    while IFS= read -r import_path; do
      relative_path="$(relative_import_path "${import_path}")" || continue

      if reason="$(common_exclusion_reason "${relative_path}")"; then
        die "${selected_flavor} dependency boundary failed for ${platform}: ${relative_path} (${reason})"
      fi
      if reason="$(cloudflared_exclusion_reason "${selected_flavor}" "${relative_path}")"; then
        die "${selected_flavor} dependency boundary failed for ${platform}: ${relative_path} (${reason})"
      fi
    done <"${package_list}"

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
