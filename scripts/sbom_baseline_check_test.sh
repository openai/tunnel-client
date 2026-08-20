#!/usr/bin/env bash
set -euo pipefail

flavor="${1:-client}"
case "${flavor}" in
  client|runtime|runtime-cloudflared) ;;
  *)
    echo "usage: sbom_baseline_check_test.sh [client|runtime|runtime-cloudflared]" >&2
    exit 1
    ;;
esac

bootstrap_resolve_runfile() {
  local logical_path="$1"
  local manifest="${RUNFILES_MANIFEST_FILE:-${TEST_SRCDIR:-}/MANIFEST}"
  local manifest_logical_path manifest_physical_path

  [[ -n "${logical_path}" && "${logical_path}" != /* ]] || return 1
  if [[ -n "${RUNFILES_DIR:-}" && -e "${RUNFILES_DIR}/${logical_path}" ]]; then
    printf '%s\n' "${RUNFILES_DIR}/${logical_path}"
    return 0
  fi
  if [[ -n "${TEST_SRCDIR:-}" && -e "${TEST_SRCDIR}/${logical_path}" ]]; then
    printf '%s\n' "${TEST_SRCDIR}/${logical_path}"
    return 0
  fi
  if [[ -f "${manifest}" ]]; then
    while IFS=' ' read -r manifest_logical_path manifest_physical_path; do
      if [[ "${manifest_logical_path}" == "${logical_path}" &&
        -n "${manifest_physical_path}" ]]; then
        printf '%s\n' "${manifest_physical_path}"
        return 0
      fi
    done <"${manifest}"
  fi
  return 1
}

runfiles_helper="$(bootstrap_resolve_runfile "${TUNNEL_CLIENT_SBOM_RUNFILES_RUNFILE:-}")" || {
  echo "declared SBOM runfiles helper is missing" >&2
  exit 1
}
# shellcheck source=sbom_runfiles.sh
source "${runfiles_helper}"

sbom_require_bazel_test_runfiles
source_root="${TEST_TMPDIR}/${flavor}-source"
cloudflared_extract_root="${TEST_TMPDIR}/cloudflared-source"
generated_root="${TEST_TMPDIR}/generated"
output_name=""
case "${flavor}" in
  client) output_name="tunnel-client.spdx.json" ;;
  runtime) output_name="tunnel-client-runtime.spdx.json" ;;
  runtime-cloudflared) output_name="tunnel-client-runtime-cloudflared.spdx.json" ;;
esac
export HOME="${TEST_TMPDIR}/home"
export TMPDIR="${TEST_TMPDIR}/tmp"
export GOTMPDIR="${TEST_TMPDIR}/tmp"
export GOCACHE="${TEST_TMPDIR}/go-cache"
export GOMODCACHE="${TEST_TMPDIR}/go-mod-cache"
export GOPATH="${TEST_TMPDIR}/go-path"
export GOWORK=off
export GOPROXY=off
export GOSUMDB=off
export GOTOOLCHAIN=local
export PYTHONDONTWRITEBYTECODE=1
export PYTHONNOUSERSITE=1
unset PYTHONHOME PYTHONPATH PYTHONUSERBASE VIRTUAL_ENV
mkdir -p \
  "${generated_root}" \
  "${HOME}" \
  "${TMPDIR}" \
  "${GOCACHE}" \
  "${GOMODCACHE}" \
  "${GOPATH}"

sbom_materialize_client_source "${source_root}"
vendor_archive="$(sbom_resolve_runfile "${TUNNEL_CLIENT_VENDOR_ARCHIVE_RUNFILE:-}")"
vendor_extractor="$(sbom_resolve_runfile "${TUNNEL_CLIENT_VENDOR_EXTRACTOR_RUNFILE:-}")"
python_bin="$(sbom_resolve_runfile "${TUNNEL_CLIENT_PYTHON_RUNFILE:-}")"
"${python_bin}" "${vendor_extractor}" \
  --source-root "${source_root}" \
  --vendor-archive "${vendor_archive}"
cloudflared_archive="$(sbom_resolve_runfile "${TUNNEL_CLIENT_CLOUDFLARED_ARCHIVE_RUNFILE:-}")"
cloudflared_extractor="$(sbom_resolve_runfile "${TUNNEL_CLIENT_CLOUDFLARED_EXTRACTOR_RUNFILE:-}")"
cloudflared_source="$(
  "${python_bin}" "${cloudflared_extractor}" \
    --archive "${cloudflared_archive}" \
    --output-root "${cloudflared_extract_root}"
)"
sbom_select_go_sdk

generator="$(sbom_resolve_runfile "${TUNNEL_CLIENT_SBOM_GENERATOR_RUNFILE:-}")"
source_preparer="$(sbom_resolve_runfile "${TUNNEL_CLIENT_SOURCE_PREPARER_RUNFILE:-}")"
oai_sbom="$(sbom_resolve_runfile "${TUNNEL_CLIENT_OAI_SBOM_RUNFILE:-}")"
syft="$(sbom_resolve_runfile "${TUNNEL_CLIENT_SYFT_RUNFILE:-}")"
syft_lock="$(sbom_resolve_runfile "${TUNNEL_CLIENT_SYFT_LOCK_RUNFILE:-}")"
expected="$(sbom_resolve_runfile "${TUNNEL_CLIENT_SBOM_BASELINE_RUNFILE:-}")"

generator_args=(
  --flavor "${flavor}"
  --source-root "${source_root}"
  --cloudflared-source "${cloudflared_source}"
  --go "${SBOM_GO_BIN}"
  --python "${python_bin}"
  --source-preparer "${source_preparer}"
  --oai-sbom "${oai_sbom}"
  --syft "${syft}"
  --syft-lock "${syft_lock}"
  --output "${generated_root}/${output_name}"
)
if [[ "${flavor}" != "client" ]]; then
  license_report_builder="$(sbom_resolve_runfile "${TUNNEL_CLIENT_LICENSE_REPORT_BUILDER_RUNFILE:-}")"
  base_license_report="$(sbom_resolve_runfile "${TUNNEL_CLIENT_BASE_LICENSE_REPORT_RUNFILE:-}")"
  generator_args+=(
    --license-report-builder "${license_report_builder}"
    --base-license-report "${base_license_report}"
  )
  if [[ "${flavor}" == "runtime-cloudflared" ]]; then
    companion_license_report="$(sbom_resolve_runfile "${TUNNEL_CLIENT_COMPANION_LICENSE_REPORT_RUNFILE:-}")"
    generator_args+=(--companion-license-report "${companion_license_report}")
  fi
fi

"${generator}" "${generator_args[@]}"

if ! diff -u "${expected}" "${generated_root}/${output_name}"; then
  echo "${flavor} SBOM baseline is stale" >&2
  exit 1
fi
