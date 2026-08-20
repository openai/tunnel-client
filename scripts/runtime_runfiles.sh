#!/usr/bin/env bash

# Materialize only Bazel-declared tunnel-client runfiles into regular files.
# Go's embed loader rejects symlinked runfiles, and following a source symlink
# back to the checkout would let undeclared inputs hide a missing Bazel label.
runtime_is_bazel_test() {
  [[ -n "${BAZEL_TEST:-}" || -n "${TEST_SRCDIR:-}" || -n "${TEST_WORKSPACE:-}" ]]
}

runtime_require_bazel_test_runfiles() {
  [[ -n "${BAZEL_TEST:-}" ]] || {
    echo "runtime Bazel runfile staging requires BAZEL_TEST" >&2
    return 1
  }
  [[ -n "${TEST_SRCDIR:-}" && -n "${TEST_WORKSPACE:-}" ]] || {
    echo "runtime Bazel runfile staging requires TEST_SRCDIR and TEST_WORKSPACE" >&2
    return 1
  }
  [[ -n "${TEST_TMPDIR:-}" && "${TEST_TMPDIR}" == /* ]] || {
    echo "runtime Bazel runfile staging requires an absolute TEST_TMPDIR" >&2
    return 1
  }
}

runtime_resolve_runfile() {
  local logical_path="$1"
  local manifest="${RUNFILES_MANIFEST_FILE:-${TEST_SRCDIR:-}/MANIFEST}"
  local manifest_logical_path manifest_physical_path

  if [[ "${logical_path}" == /* && -e "${logical_path}" ]]; then
    printf '%s\n' "${logical_path}"
    return 0
  fi
  if [[ -n "${RUNFILES_DIR:-}" && -e "${RUNFILES_DIR}/${logical_path}" ]]; then
    printf '%s\n' "${RUNFILES_DIR}/${logical_path}"
    return 0
  fi
  if [[ -n "${TEST_SRCDIR:-}" && -e "${TEST_SRCDIR}/${logical_path}" ]]; then
    printf '%s\n' "${TEST_SRCDIR}/${logical_path}"
    return 0
  fi
  if [[ -n "${TEST_SRCDIR:-}" && -n "${TEST_WORKSPACE:-}" &&
    -e "${TEST_SRCDIR}/${TEST_WORKSPACE}/${logical_path}" ]]; then
    printf '%s\n' "${TEST_SRCDIR}/${TEST_WORKSPACE}/${logical_path}"
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

runtime_resolve_declared_runfile() {
  local logical_path="$1"

  runtime_require_bazel_test_runfiles || return 1
  [[ -n "${logical_path}" && "${logical_path}" != /* ]] || {
    echo "declared runtime runfile path is missing or absolute: ${logical_path}" >&2
    return 1
  }
  runtime_resolve_runfile "${logical_path}"
}

runtime_copy_declared_file() {
  local logical_path="$1"
  local destination="$2"
  local source

  source="$(runtime_resolve_declared_runfile "${logical_path}")" || return 1
  [[ -f "${source}" ]] || {
    echo "declared runtime file runfile is missing: ${logical_path}" >&2
    return 1
  }
  mkdir -p "$(dirname "${destination}")"
  if [[ -e "${destination}" ]]; then
    chmod u+w "${destination}" || return 1
  fi
  cp -pL -- "${source}" "${destination}" || return 1
  chmod u+w "${destination}" || return 1
}

runtime_declared_python_home() {
  local runfiles_root="${RUNFILES_DIR:-${TEST_SRCDIR:-}}"
  local manifest="${RUNFILES_MANIFEST_FILE:-${TEST_SRCDIR:-}/MANIFEST}"
  local encodings_path logical_path physical_path python_home

  # Remote execution may materialize current_python3 as a regular file instead
  # of preserving its symlink to the hermetic interpreter. Resolve the declared
  # stdlib through runfiles so CPython can import encodings in either layout.
  if [[ -n "${runfiles_root}" ]]; then
    for encodings_path in "${runfiles_root}"/*/lib/python*/encodings/__init__.py; do
      if [[ -f "${encodings_path}" ]]; then
        python_home="$(dirname "$(dirname "$(dirname "$(dirname "${encodings_path}")")")")"
        printf '%s\n' "${python_home}"
        return 0
      fi
    done
  fi
  if [[ -f "${manifest}" ]]; then
    while IFS=' ' read -r logical_path physical_path; do
      case "${logical_path}" in
        */lib/python*/encodings/__init__.py)
          if [[ -f "${physical_path}" ]]; then
            python_home="$(dirname "$(dirname "$(dirname "$(dirname "${physical_path}")")")")"
            printf '%s\n' "${python_home}"
            return 0
          fi
          ;;
      esac
    done <"${manifest}"
  fi
  echo "declared runtime Python stdlib is missing from runfiles" >&2
  return 1
}

runtime_prepare_offline_go_environment() {
  runtime_require_bazel_test_runfiles || return 1

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
  export GOENV=off
  export GOTELEMETRY=off
  export GOFLAGS=
  mkdir -p "${HOME}" "${TMPDIR}" "${GOCACHE}" "${GOMODCACHE}" "${GOPATH}"
}

runtime_select_declared_python() {
  local python_runfile="${TUNNEL_CLIENT_PYTHON_RUNFILE:-}"
  local python_home

  RUNTIME_PYTHON_BIN="$(runtime_resolve_declared_runfile "${python_runfile}")" || {
    echo "declared runtime Python runfile is missing: ${python_runfile}" >&2
    return 1
  }
  [[ -x "${RUNTIME_PYTHON_BIN}" ]] || {
    echo "declared runtime Python is not executable: ${RUNTIME_PYTHON_BIN}" >&2
    return 1
  }
  python_home="$(runtime_declared_python_home)" || return 1
  export TUNNEL_CLIENT_RUNTIME_PYTHON_HOME="${python_home}"
  export TUNNEL_CLIENT_RUNTIME_PYTHON="${RUNTIME_PYTHON_BIN}"
}

runtime_python() {
  if [[ -n "${TUNNEL_CLIENT_RUNTIME_PYTHON:-}" ]]; then
    if [[ -n "${TUNNEL_CLIENT_RUNTIME_PYTHON_HOME:-}" ]]; then
      PYTHONHOME="${TUNNEL_CLIENT_RUNTIME_PYTHON_HOME}" "${TUNNEL_CLIENT_RUNTIME_PYTHON}" "$@"
    else
      "${TUNNEL_CLIENT_RUNTIME_PYTHON}" "$@"
    fi
    return
  fi
  python3 "$@"
}

runtime_stage_declared_vendor() {
  local source_root="$1"
  local vendor_archive_runfile="${TUNNEL_CLIENT_VENDOR_ARCHIVE_RUNFILE:-}"
  local vendor_digest_runfile="${TUNNEL_CLIENT_VENDOR_DIGEST_RUNFILE:-}"
  local vendor_extractor_runfile="${TUNNEL_CLIENT_VENDOR_EXTRACTOR_RUNFILE:-}"

  runtime_require_bazel_test_runfiles || return 1
  case "${source_root}" in
    "${TEST_TMPDIR}"/*) ;;
    *)
      echo "runtime vendor staging must stay below TEST_TMPDIR: ${source_root}" >&2
      return 1
      ;;
  esac
  [[ ! -e "${source_root}/vendor" ]] || {
    echo "runtime vendor staging destination already exists: ${source_root}/vendor" >&2
    return 1
  }

  if [[ -z "${RUNTIME_PYTHON_BIN:-}" ]]; then
    runtime_select_declared_python || return 1
  fi
  RUNTIME_VENDOR_ARCHIVE="$(runtime_resolve_declared_runfile "${vendor_archive_runfile}")" || {
    echo "declared runtime vendor archive runfile is missing: ${vendor_archive_runfile}" >&2
    return 1
  }
  RUNTIME_VENDOR_EXTRACTOR="$(runtime_resolve_declared_runfile "${vendor_extractor_runfile}")" || {
    echo "declared runtime vendor extractor runfile is missing: ${vendor_extractor_runfile}" >&2
    return 1
  }
  [[ -f "${RUNTIME_VENDOR_ARCHIVE}" ]] || {
    echo "declared runtime vendor archive is missing: ${RUNTIME_VENDOR_ARCHIVE}" >&2
    return 1
  }
  [[ -f "${RUNTIME_VENDOR_EXTRACTOR}" ]] || {
    echo "declared runtime vendor extractor is missing: ${RUNTIME_VENDOR_EXTRACTOR}" >&2
    return 1
  }

  runtime_copy_declared_file "${vendor_digest_runfile}" "${source_root}/compliance/sbom-vendor-tree.sha256" || return 1
  runtime_python "${RUNTIME_VENDOR_EXTRACTOR}" --source-root "${source_root}" --vendor-archive "${RUNTIME_VENDOR_ARCHIVE}"
}

runtime_stage_vendor_into_root() {
  local source_root="$1"

  if ! runtime_is_bazel_test; then
    return 0
  fi
  runtime_stage_declared_vendor "${source_root}"
}

runtime_go_mod_flag_for_root() {
  local root="$1"

  if [[ -f "${root}/vendor/modules.txt" ]]; then
    printf '%s\n' "-mod=vendor"
    return 0
  fi
  printf '%s\n' "-mod=readonly"
}

# Bazel sh_tests do not inherit rules_go's toolchain PATH. When the BUILD
# target supplies an rlocation, prefer that declared SDK and preserve the
# caller's ordinary PATH/GOROOT behavior everywhere else.
use_tunnel_client_bazel_go_sdk() {
  local go_runfile="${TUNNEL_CLIENT_BAZEL_GO_RUNFILE:-}"
  local go_binary sdk_root

  if [[ -z "${go_runfile}" ]]; then
    if runtime_is_bazel_test; then
      echo "declared Bazel Go SDK runfile is missing" >&2
      return 1
    fi
    return 0
  fi
  if runtime_is_bazel_test; then
    go_binary="$(runtime_resolve_declared_runfile "${go_runfile}")" || {
      echo "declared Bazel Go SDK runfile is missing: ${go_runfile}" >&2
      return 1
    }
  else
    go_binary="$(runtime_resolve_runfile "${go_runfile}")" || {
      echo "declared Bazel Go SDK runfile is missing: ${go_runfile}" >&2
      return 1
    }
  fi
  [[ -n "${go_binary}" ]] || {
    echo "declared Bazel Go SDK runfile is missing: ${go_runfile}" >&2
    return 1
  }
  [[ -x "${go_binary}" ]] || {
    echo "declared Bazel Go SDK binary is not executable: ${go_binary}" >&2
    return 1
  }

  sdk_root="$(cd "$(dirname "${go_binary}")/.." && pwd)"
  [[ -d "${sdk_root}/src" ]] || {
    echo "declared Bazel Go SDK is incomplete: ${sdk_root}" >&2
    return 1
  }
  export GOROOT="${sdk_root}"
  export PATH="$(dirname "${go_binary}"):${PATH:-/usr/bin:/bin}"
}

materialize_tunnel_client_runfiles() {
  local fallback_script_dir="$1"
  RUNTIME_RUNFILES_TEMP_ROOT=""
  RUNTIME_SCRIPT_DIR="${fallback_script_dir}"
  RUNTIME_PROJECT_ROOT="$(cd "${fallback_script_dir}/.." && pwd)"
  RUNTIME_PYTHON_BIN=""
  RUNTIME_VENDOR_ARCHIVE=""
  RUNTIME_VENDOR_EXTRACTOR=""

  if ! runtime_is_bazel_test; then
    return 0
  fi
  runtime_require_bazel_test_runfiles || return 1
  runtime_prepare_offline_go_environment || return 1

  local logical_prefix="${TEST_WORKSPACE}/api/tunnel-client/"
  local runfiles_root="${TEST_SRCDIR}/${TEST_WORKSPACE}/api/tunnel-client"
  local manifest="${RUNFILES_MANIFEST_FILE:-${TEST_SRCDIR}/MANIFEST}"
  local staged_root
  staged_root="$(mktemp -d "${TEST_TMPDIR}/tunnel-client-runtime-runfiles.XXXXXX")"

  local logical_path physical_path relative_path destination_path source_path
  if [[ -f "${manifest}" ]]; then
    while IFS=' ' read -r logical_path physical_path; do
      case "${logical_path}" in
        "${logical_prefix}"*)
          relative_path="${logical_path#"${logical_prefix}"}"
          [[ -n "${relative_path}" && -n "${physical_path}" ]] || continue
          destination_path="${staged_root}/${relative_path}"
          mkdir -p "$(dirname "${destination_path}")"
          cp -pL -- "${physical_path}" "${destination_path}"
          ;;
      esac
    done <"${manifest}"
  elif [[ -d "${runfiles_root}" ]]; then
    while IFS= read -r -d '' source_path; do
      relative_path="${source_path#"${runfiles_root}/"}"
      destination_path="${staged_root}/${relative_path}"
      mkdir -p "$(dirname "${destination_path}")"
      cp -pL -- "${source_path}" "${destination_path}"
    done < <(find "${runfiles_root}" \( -type f -o -type l \) -print0)
  else
    rm -rf "${staged_root}"
    echo "runtime runfiles root is missing: ${runfiles_root}" >&2
    return 1
  fi

  if [[ ! -f "${staged_root}/go.mod" || ! -x "${staged_root}/scripts/check_runtime_boundary.sh" ]]; then
    rm -rf "${staged_root}"
    echo "runtime runfiles are missing declared project inputs" >&2
    return 1
  fi
  runtime_select_declared_python || {
    rm -rf "${staged_root}"
    return 1
  }
  runtime_stage_declared_vendor "${staged_root}" || {
    rm -rf "${staged_root}"
    return 1
  }

  RUNTIME_RUNFILES_TEMP_ROOT="${staged_root}"
  RUNTIME_SCRIPT_DIR="${staged_root}/scripts"
  RUNTIME_PROJECT_ROOT="${staged_root}"
}

runtime_runfiles_cleanup() {
  if [[ -n "${RUNTIME_RUNFILES_TEMP_ROOT:-}" ]]; then
    rm -rf "${RUNTIME_RUNFILES_TEMP_ROOT}"
  fi
}
