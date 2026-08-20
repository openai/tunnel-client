#!/usr/bin/env bash
set -euo pipefail

readonly SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
readonly PROJECT_ROOT="$(cd "${SCRIPT_DIR}/.." && pwd)"
readonly CALLER_DIR="$(pwd)"

usage() {
  cat <<'EOF'
Usage:
  ./scripts/check_runtime_binary_markers.sh \
    --flavor runtime|runtime-cloudflared \
    [--binary <path>]

Builds the host runtime binary when --binary is omitted, then checks its
flavor identity and rejects markers for excluded first-party surfaces.
EOF
}

die() {
  echo "check_runtime_binary_markers.sh: $*" >&2
  exit 1
}

flavor=""
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
  runtime|runtime-cloudflared) ;;
  *) die "--flavor must be runtime or runtime-cloudflared" ;;
esac

command -v strings >/dev/null 2>&1 || die "strings is required"
if [[ -n "${binary}" && "${binary}" != /* ]]; then
  binary="${CALLER_DIR}/${binary}"
fi
cd "${PROJECT_ROOT}"

if [[ -z "${binary}" && -n "${TEST_SRCDIR:-}" && -n "${TEST_WORKSPACE:-}" ]]; then
  candidate="${TEST_SRCDIR}/${TEST_WORKSPACE}/api/tunnel-client/cmd/client-runtime/client_runtime"
  if [[ "${flavor}" == "runtime-cloudflared" ]]; then
    candidate="${TEST_SRCDIR}/${TEST_WORKSPACE}/api/tunnel-client/cmd/client-runtime-cloudflared/client_runtime_cloudflared"
  fi
  if [[ -x "${candidate}" ]]; then
    binary="${candidate}"
  fi
fi

tmp_dir=""
if [[ -z "${binary}" ]]; then
  command -v go >/dev/null 2>&1 || die "go is required when --binary is omitted"
  tmp_dir="$(mktemp -d "${TMPDIR:-/tmp}/tunnel-client-runtime-markers.XXXXXX")"
  trap 'rm -rf "${tmp_dir}"' EXIT
  binary="${tmp_dir}/tunnel-client-${flavor}"
  target="./cmd/client-runtime"
  [[ "${flavor}" == "runtime-cloudflared" ]] && target="./cmd/client-runtime-cloudflared"
  go_cache_dir="${GOCACHE:-${TMPDIR:-/tmp}/tunnel-client-runtime-go-cache}"
  go_mod_cache_dir="${GOMODCACHE:-${TMPDIR:-/tmp}/tunnel-client-runtime-go-mod-cache}"
  mkdir -p "${go_cache_dir}" "${go_mod_cache_dir}"
  env \
    GOWORK=off \
    GOCACHE="${go_cache_dir}" \
    GOMODCACHE="${go_mod_cache_dir}" \
    CGO_ENABLED=0 \
    go build \
      -mod=readonly \
      -trimpath \
      -buildvcs=false \
      -ldflags "-X github.com/openai/tunnel-client/pkg/version.Flavor=${flavor}" \
      -o "${binary}" \
      "${target}"
fi

[[ -f "${binary}" ]] || die "binary does not exist: ${binary}"

marker_file="$(mktemp "${TMPDIR:-/tmp}/tunnel-client-runtime-markers.XXXXXX")"
trap 'rm -f "${marker_file}"; [[ -z "${tmp_dir}" ]] || rm -rf "${tmp_dir}"' EXIT
strings "${binary}" >"${marker_file}"

required_marker="flavor=${flavor}"

readonly -a forbidden_markers=(
  "github.com/openai/tunnel-client/pkg/adminui"
  "github.com/openai/tunnel-client/pkg/codex"
  "github.com/openai/tunnel-client/pkg/plugins"
  "github.com/openai/tunnel-client/pkg/localproxy"
  "github.com/openai/tunnel-client/pkg/proxyhealth"
  "github.com/openai/tunnel-client/pkg/harpoon"
  "CapturePayloads"
  "CallBuffer"
)

for marker in "${forbidden_markers[@]}"; do
  if grep -Fq "${marker}" "${marker_file}"; then
    die "${flavor} binary contains excluded marker: ${marker}"
  fi
done

if grep -Eq 'go\.openai\.org/api/tunnel-client/cmd/client([./]|$)' "${marker_file}"; then
  die "${flavor} binary contains excluded full command marker"
fi

cloudflared_marker="github.com/openai/tunnel-client/pkg/cloudflared/runtime"
if [[ "${flavor}" == "runtime" ]]; then
  if grep -Fq "${cloudflared_marker}" "${marker_file}"; then
    die "runtime binary contains companion runtime marker"
  fi
else
  grep -Fq "${cloudflared_marker}" "${marker_file}" ||
    die "runtime-cloudflared binary is missing companion runtime marker"
fi

if [[ -x "${binary}" && "${binary}" != *.exe ]]; then
  version_output="$("${binary}" --version 2>&1)" ||
    die "binary --version failed"
  [[ "${version_output}" == *"${required_marker}"* ]] ||
    die "binary --version did not report ${required_marker}"
else
  grep -Fq "${flavor}" "${marker_file}" ||
    die "binary does not contain expected flavor identity: ${flavor}"
fi

printf '%s binary markers: passed\n' "${flavor}"
