#!/usr/bin/env bash
set -euo pipefail

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
compliance_dir="${TEST_TMPDIR}/compliance"
mkdir -p "${compliance_dir}"

sbom_copy_declared_file "${TUNNEL_CLIENT_SBOM_MANIFEST_RUNFILE:-}" "${compliance_dir}/sbom-baseline-manifest.json"
sbom_copy_declared_file "${TUNNEL_CLIENT_CLIENT_SBOM_RUNFILE:-}" "${compliance_dir}/tunnel-client.spdx.json"
sbom_copy_declared_file "${TUNNEL_CLIENT_RUNTIME_SBOM_RUNFILE:-}" "${compliance_dir}/tunnel-client-runtime.spdx.json"
sbom_copy_declared_file "${TUNNEL_CLIENT_RUNTIME_CLOUDFLARED_SBOM_RUNFILE:-}" "${compliance_dir}/tunnel-client-runtime-cloudflared.spdx.json"

verifier="$(sbom_resolve_runfile "${TUNNEL_CLIENT_SBOM_VERIFIER_RUNFILE:-}")"
"${verifier}" --compliance-dir "${compliance_dir}"
