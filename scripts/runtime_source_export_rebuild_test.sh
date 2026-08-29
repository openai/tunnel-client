#!/usr/bin/env bash
set -euo pipefail

flavor=""
platform=""
archive=""
compare_public_default=false
if [[ $# -gt 0 && "$1" != --* ]]; then
  flavor="$1"
  shift
fi
while [[ $# -gt 0 ]]; do
  case "$1" in
    --platform)
      platform="${2:-}"
      shift 2
      ;;
    --platform=*)
      platform="${1#*=}"
      shift
      ;;
    --archive)
      archive="${2:-}"
      shift 2
      ;;
    --archive=*)
      archive="${1#*=}"
      shift
      ;;
    --compare-public-default)
      compare_public_default=true
      shift
      ;;
    -h|--help)
      echo "usage: runtime_source_export_rebuild_test.sh runtime|runtime-cloudflared [--platform <goos>/<goarch>] [--archive <path>] [--compare-public-default]" >&2
      exit 0
      ;;
    *)
      echo "usage: runtime_source_export_rebuild_test.sh runtime|runtime-cloudflared [--platform <goos>/<goarch>] [--archive <path>] [--compare-public-default]" >&2
      exit 1
      ;;
  esac
done

case "${flavor}" in
  runtime|runtime-cloudflared) ;;
  *)
    echo "usage: runtime_source_export_rebuild_test.sh runtime|runtime-cloudflared [--platform <goos>/<goarch>] [--archive <path>] [--compare-public-default]" >&2
    exit 1
    ;;
esac
case "${platform}" in
  ""|linux/amd64|linux/arm64|darwin/amd64|darwin/arm64|windows/amd64|windows/arm64) ;;
  *)
    echo "--platform must be one of linux/amd64, linux/arm64, darwin/amd64, darwin/arm64, windows/amd64, or windows/arm64" >&2
    exit 1
    ;;
esac
platform_args=()
if [[ -n "${platform}" ]]; then
  platform_args=(--platform "${platform}")
fi
if [[ "${compare_public_default}" == true ]]; then
  [[ -n "${archive}" ]] || {
    echo "--compare-public-default requires --archive" >&2
    exit 1
  }
  [[ -z "${platform}" ]] || {
    echo "--compare-public-default cannot be combined with --platform" >&2
    exit 1
  }
fi

script_dir="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
if [[ -n "${TEST_SRCDIR:-}" && -n "${TEST_WORKSPACE:-}" ]]; then
  script_dir="${TEST_SRCDIR}/${TEST_WORKSPACE}/api/tunnel-client/scripts"
elif [[ -x "${script_dir}/scripts/build_runtime_source_export.sh" ]]; then
  script_dir="${script_dir}/scripts"
fi
if [[ -n "${archive}" && "${archive}" != /* ]]; then
  if [[ -f "${archive}" ]]; then
    archive="$(cd "$(dirname "${archive}")" && pwd)/$(basename "${archive}")"
  else
    # $(location ...) is workspace-relative in sh_test args. Resolve that
    # declared archive through runfiles before handing it to tar/verification.
    # shellcheck source=runtime_runfiles.sh
    source "${script_dir}/runtime_runfiles.sh"
    archive="$(runtime_resolve_runfile "${archive}")" || {
      echo "declared runtime source archive is missing from runfiles" >&2
      exit 1
    }
  fi
fi
if [[ -z "${archive}" || "${compare_public_default}" == true ]]; then
  [[ -x "${script_dir}/build_runtime_source_export.sh" ]] || {
    echo "runtime source-export build script is missing from runfiles" >&2
    exit 1
  }
  if [[ -n "${BAZEL_TEST:-}" && -n "${TEST_TMPDIR:-}" ]]; then
    output_dir="$(mktemp -d "${TEST_TMPDIR}/tunnel-client-runtime-source-test.XXXXXX")"
  else
    output_dir="$(mktemp -d "${TMPDIR:-/tmp}/tunnel-client-runtime-source-test.XXXXXX")"
  fi
  trap 'rm -rf "${output_dir}"' EXIT

  "${script_dir}/build_runtime_source_export.sh" \
    --flavor "${flavor}" \
    --version v0.0.0-test \
    --output-dir "${output_dir}" \
    "${platform_args[@]}"

  built_archive="${output_dir}/tunnel-client-runtime-source-v0.0.0-test.tar.gz"
  if [[ "${flavor}" == "runtime-cloudflared" ]]; then
    built_archive="${output_dir}/tunnel-client-runtime-cloudflared-source-v0.0.0-test.tar.gz"
  fi
  if [[ -z "${archive}" ]]; then
    archive="${built_archive}"
  elif ! cmp -s "${archive}" "${built_archive}"; then
    echo "Bazel runtime source archive differs from the public default builder" >&2
    exit 1
  fi
fi

if [[ "${compare_public_default}" == true ]]; then
  printf '%s source export: public default archive bytes passed\n' "${flavor}"
  exit 0
fi
"${script_dir}/verify_runtime_source_export.sh" \
  --flavor "${flavor}" \
  --archive "${archive}" \
  "${platform_args[@]}"
