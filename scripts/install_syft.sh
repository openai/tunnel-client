#!/usr/bin/env bash
set -euo pipefail

readonly SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
readonly PROJECT_ROOT="$(cd "${SCRIPT_DIR}/.." && pwd)"
readonly SYFT_VERSION="1.46.0"
readonly INSTALL_DIR="${PROJECT_ROOT}/.cache/tools"
readonly DEFAULT_SYFT_PATH="${INSTALL_DIR}/syft"

usage() {
  cat <<'EOF'
Usage:
  ./scripts/install_syft.sh

Installs the pinned Syft binary at .cache/tools/syft and prints its absolute
path. Set SYFT=/absolute/path/to/syft to use an existing pinned binary.
EOF
}

die() {
  echo "install_syft.sh: $*" >&2
  exit 1
}

if [[ $# -gt 0 ]]; then
  case "$1" in
    -h|--help)
      usage
      exit 0
      ;;
    *)
      usage >&2
      die "unknown argument: $1"
      ;;
  esac
fi

sha256_file() {
  local file="$1"
  if command -v sha256sum >/dev/null 2>&1; then
    sha256sum "${file}" | awk '{print $1}'
    return 0
  fi
  if command -v shasum >/dev/null 2>&1; then
    shasum -a 256 "${file}" | awk '{print $1}'
    return 0
  fi
  die "sha256sum or shasum is required"
}

validate_syft() {
  local candidate="$1"
  local version_output
  [[ "${candidate}" == /* ]] || return 1
  [[ -x "${candidate}" ]] || return 1
  version_output="$("${candidate}" version 2>&1)" || return 1
  printf '%s\n' "${version_output}" |
    grep -Eq '^[[:space:]]*Version:[[:space:]]+v?1\.46\.0([[:space:]]|$)'
}

if [[ -n "${SYFT:-}" ]]; then
  [[ "${SYFT}" == /* ]] || die "SYFT must be an absolute path"
  validate_syft "${SYFT}" ||
    die "SYFT must point to an executable Syft ${SYFT_VERSION} binary"
  printf '%s\n' "${SYFT}"
  exit 0
fi

if validate_syft "${DEFAULT_SYFT_PATH}"; then
  printf '%s\n' "${DEFAULT_SYFT_PATH}"
  exit 0
fi

command -v curl >/dev/null 2>&1 || die "curl is required"
command -v tar >/dev/null 2>&1 || die "tar is required"

host_os=""
case "$(uname -s)" in
  Linux) host_os="linux" ;;
  Darwin) host_os="darwin" ;;
  *) die "only Linux and Darwin are supported" ;;
esac

host_arch=""
case "$(uname -m)" in
  x86_64|amd64) host_arch="amd64" ;;
  arm64|aarch64) host_arch="arm64" ;;
  *) die "unsupported architecture: $(uname -m)" ;;
esac

expected_sha=""
case "${host_os}/${host_arch}" in
  darwin/amd64) expected_sha="5c983db13533de02e5331aae88091116f25365840741f86234084a30166672a7" ;;
  darwin/arm64) expected_sha="cd4e2c40e075684a5746d8959f76b6572bb2d2dda8cf6877dbfff1cc0baeea01" ;;
  linux/amd64) expected_sha="d654f678b709eb53c393d38519d5ed7d2e57205529404018614cfefa0fb2b5ca" ;;
  linux/arm64) expected_sha="9fafef4db4f032ce81008d3a1529985d41ceb6ccdf2b388c9ce2f1ed7d32082e" ;;
  *) die "unsupported platform: ${host_os}/${host_arch}" ;;
esac

asset="syft_${SYFT_VERSION}_${host_os}_${host_arch}.tar.gz"
url="https://github.com/anchore/syft/releases/download/v${SYFT_VERSION}/${asset}"
tmp_dir="$(mktemp -d "${TMPDIR:-/tmp}/tunnel-client-syft.XXXXXX")"
trap 'rm -rf "${tmp_dir}"' EXIT
archive="${tmp_dir}/${asset}"

echo "install_syft.sh: downloading Syft ${SYFT_VERSION} for ${host_os}/${host_arch}" >&2
curl --fail --silent --show-error --location --retry 3 \
  --proto '=https' --tlsv1.2 \
  --output "${archive}" \
  "${url}"

actual_sha="$(sha256_file "${archive}")"
[[ "${actual_sha}" == "${expected_sha}" ]] ||
  die "checksum mismatch for ${asset}: got ${actual_sha}, want ${expected_sha}"

tar -xzf "${archive}" -C "${tmp_dir}" syft ||
  die "archive does not contain the expected syft binary"
[[ -f "${tmp_dir}/syft" ]] || die "archive extraction did not produce syft"

mkdir -p "${INSTALL_DIR}"
install -m 0755 "${tmp_dir}/syft" "${DEFAULT_SYFT_PATH}"
validate_syft "${DEFAULT_SYFT_PATH}" ||
  die "installed binary did not report Syft ${SYFT_VERSION}"

printf '%s\n' "${DEFAULT_SYFT_PATH}"
