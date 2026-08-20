#!/usr/bin/env bash
set -euo pipefail

readonly SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
readonly PROJECT_ROOT="$(cd "${SCRIPT_DIR}/.." && pwd)"
readonly SYFT_VERSION="1.46.0"

usage() {
  cat <<'EOF'
Usage:
  ./scripts/generate_sbom.sh \
    --flavor client|runtime|runtime-cloudflared \
    --staged-dir <directory> \
    --output <path-to-spdx-json>

Scans a final pre-SBOM staged payload and writes an SPDX 2.3 JSON document.
The output path must be outside the staged payload so the document never
describes itself. Set SBOM_SOURCE_VERSION to bind the document to a release
version; local generation defaults to "local".
EOF
}

die() {
  echo "generate_sbom.sh: $*" >&2
  exit 1
}

flavor=""
staged_dir=""
output=""
while [[ $# -gt 0 ]]; do
  case "$1" in
    --flavor)
      flavor="${2:-}"
      shift 2
      ;;
    --staged-dir)
      staged_dir="${2:-}"
      shift 2
      ;;
    --output)
      output="${2:-}"
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
  client|runtime|runtime-cloudflared) ;;
  *) die "--flavor must be client, runtime, or runtime-cloudflared" ;;
esac
[[ -n "${staged_dir}" ]] || die "--staged-dir is required"
[[ -d "${staged_dir}" ]] || die "staged directory does not exist: ${staged_dir}"
[[ -n "${output}" ]] || die "--output is required"

command -v jq >/dev/null 2>&1 || die "jq is required"

staged_dir="$(cd "${staged_dir}" && pwd)"
output_parent="$(dirname "${output}")"
mkdir -p "${output_parent}"
output_parent="$(cd "${output_parent}" && pwd)"
output="${output_parent}/$(basename "${output}")"
case "${output}" in
  "${staged_dir}"|"${staged_dir}"/*)
    die "--output must be outside --staged-dir"
    ;;
esac

syft_bin="$("${SCRIPT_DIR}/install_syft.sh")"
[[ "${syft_bin}" == /* && -x "${syft_bin}" ]] ||
  die "installer did not return an executable absolute path"
version_output="$("${syft_bin}" version 2>&1)" ||
  die "could not run Syft version check"
printf '%s\n' "${version_output}" |
  grep -Eq '^[[:space:]]*Version:[[:space:]]+v?1\.46\.0([[:space:]]|$)' ||
  die "Syft must report version ${SYFT_VERSION}"

tmp_output="$(mktemp "${output_parent}/.$(basename "${output}").tmp.XXXXXX")"
augmented_output="$(mktemp "${output_parent}/.$(basename "${output}").augmented.XXXXXX")"
trap 'rm -f "${tmp_output}" "${augmented_output}"' EXIT
syft_cache_dir="${SYFT_CACHE_DIR:-${PROJECT_ROOT}/.cache/syft}"
mkdir -p "${syft_cache_dir}"
source_name=""
case "${flavor}" in
  client) source_name="tunnel-client" ;;
  runtime) source_name="tunnel-client-runtime" ;;
  runtime-cloudflared) source_name="tunnel-client-runtime-cloudflared" ;;
esac
source_version="${SBOM_SOURCE_VERSION:-local}"

if ! env \
  SYFT_CHECK_FOR_APP_UPDATE=false \
  SYFT_CACHE_DIR="${syft_cache_dir}" \
  "${syft_bin}" \
  "dir:${staged_dir}" \
  --parallelism 1 \
  --base-path "${staged_dir}" \
  --source-name "${source_name}" \
  --source-version "${source_version}" \
  -o "spdx-json=${tmp_output}"; then
  die "Syft failed for ${flavor} staged payload"
fi

jq -e '.spdxVersion == "SPDX-2.3"' "${tmp_output}" >/dev/null ||
  die "generated document is not SPDX 2.3 JSON"

"${SCRIPT_DIR}/augment_sbom_files.sh" \
  --input "${tmp_output}" \
  --staged-dir "${staged_dir}" \
  --output "${augmented_output}" >/dev/null
jq -e '.spdxVersion == "SPDX-2.3"' "${augmented_output}" >/dev/null ||
  die "augmented document is not SPDX 2.3 JSON"

mv "${augmented_output}" "${output}"
trap - EXIT
printf 'wrote %s\n' "${output}"
