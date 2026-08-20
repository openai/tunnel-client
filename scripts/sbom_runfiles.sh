#!/usr/bin/env bash
set -euo pipefail

# Bazel-only runfile staging for the tunnel-client SBOM test. There is
# deliberately no checkout fallback: every source, tool, and archive must be a
# declared runfile.

sbom_runfiles_die() {
  echo "sbom_runfiles.sh: $*" >&2
  return 1
}

sbom_require_bazel_test_runfiles() {
  [[ -n "${BAZEL_TEST:-}" ]] ||
    sbom_runfiles_die "SBOM runfile staging is available only under Bazel tests"
  [[ -n "${TEST_SRCDIR:-}" && -n "${TEST_WORKSPACE:-}" ]] ||
    sbom_runfiles_die "TEST_SRCDIR and TEST_WORKSPACE are required under Bazel"
  [[ -n "${TEST_TMPDIR:-}" && "${TEST_TMPDIR}" == /* ]] ||
    sbom_runfiles_die "TEST_TMPDIR is required under Bazel"
}

sbom_assert_tmpdir_child() {
  local path="$1"
  sbom_require_bazel_test_runfiles || return 1
  case "${path}" in
    "${TEST_TMPDIR}"/*) ;;
    *) sbom_runfiles_die "staging path must stay below TEST_TMPDIR: ${path}" ;;
  esac
}

sbom_resolve_runfile() {
  local logical_path="$1"
  local manifest="${RUNFILES_MANIFEST_FILE:-${TEST_SRCDIR:-}/MANIFEST}"
  local manifest_logical_path manifest_physical_path

  sbom_require_bazel_test_runfiles || return 1
  [[ -n "${logical_path}" ]] ||
    sbom_runfiles_die "declared runfile path is missing"
  [[ "${logical_path}" != /* ]] ||
    sbom_runfiles_die "absolute paths are not runfile logical paths: ${logical_path}"
  if [[ -n "${RUNFILES_DIR:-}" && -e "${RUNFILES_DIR}/${logical_path}" ]]; then
    printf '%s\n' "${RUNFILES_DIR}/${logical_path}"
    return 0
  fi
  if [[ -e "${TEST_SRCDIR}/${logical_path}" ]]; then
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
  sbom_runfiles_die "declared runfile is missing: ${logical_path}"
}

sbom_copy_declared_file() {
  local logical_path="$1"
  local destination="$2"
  local source

  sbom_assert_tmpdir_child "${destination}" || return 1
  source="$(sbom_resolve_runfile "${logical_path}")" || return 1
  [[ -f "${source}" ]] ||
    sbom_runfiles_die "declared file runfile is missing: ${logical_path}"
  mkdir -p "$(dirname "${destination}")"
  cp -pL -- "${source}" "${destination}"
  chmod u+w "${destination}"
}

sbom_materialize_client_source() {
  local destination="$1"
  local gopath source_root

  sbom_assert_tmpdir_child "${destination}" || return 1
  gopath="$(sbom_resolve_runfile "${TUNNEL_CLIENT_SOURCE_GOPATH_RUNFILE:-}")" ||
    return 1
  source_root="${gopath}/src/github.com/openai/tunnel-client"
  [[ -d "${source_root}" ]] ||
    sbom_runfiles_die "declared Go source tree is missing: ${source_root}"

  mkdir -p "${destination}"
  cp -pRL -- "${source_root}/." "${destination}/"
  chmod -R u+rwX "${destination}"
  sbom_copy_declared_file "${TUNNEL_CLIENT_GO_MOD_RUNFILE:-}" "${destination}/go.mod"
  sbom_copy_declared_file "${TUNNEL_CLIENT_GO_SUM_RUNFILE:-}" "${destination}/go.sum"
  sbom_copy_declared_file "${TUNNEL_CLIENT_LICENSE_RUNFILE:-}" "${destination}/LICENSE"
  sbom_copy_declared_file "${TUNNEL_CLIENT_NOTICE_RUNFILE:-}" "${destination}/NOTICE"
  sbom_copy_declared_file \
    "${TUNNEL_CLIENT_VENDOR_DIGEST_RUNFILE:-}" \
    "${destination}/compliance/sbom-vendor-tree.sha256"
}

sbom_select_go_sdk() {
  SBOM_GO_BIN="$(sbom_resolve_runfile "${TUNNEL_CLIENT_GO_RUNFILE:-}")" ||
    return 1
  [[ -x "${SBOM_GO_BIN}" ]] ||
    sbom_runfiles_die "declared Go tool is not executable: ${SBOM_GO_BIN}"
}
