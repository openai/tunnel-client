#!/usr/bin/env bash
set -euo pipefail

readonly SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
readonly PROJECT_ROOT="$(cd "${SCRIPT_DIR}/.." && pwd)"
readonly GRYPE_VERSION="0.117.0"
readonly INSTALL_DIR="${PROJECT_ROOT}/.cache/tools"
readonly DEFAULT_GRYPE_PATH="${INSTALL_DIR}/grype"

usage() {
  cat <<'EOF'
Usage:
  ./scripts/install_grype.sh

Installs the pinned Grype binary at .cache/tools/grype and prints its absolute
path. Set GRYPE=/absolute/path/to/grype to use an existing pinned binary.
EOF
}

die() {
  echo "install_grype.sh: $*" >&2
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

validate_grype() {
  local candidate="$1"
  local version_output
  [[ "${candidate}" == /* ]] || return 1
  [[ -x "${candidate}" ]] || return 1
  version_output="$("${candidate}" version 2>&1)" || return 1
  printf '%s\n' "${version_output}" |
    grep -Eq '^[[:space:]]*Version:[[:space:]]+v?0\.117\.0([[:space:]]|$)'
}

if [[ -n "${GRYPE:-}" ]]; then
  [[ "${GRYPE}" == /* ]] || die "GRYPE must be an absolute path"
  validate_grype "${GRYPE}" ||
    die "GRYPE must point to an executable Grype ${GRYPE_VERSION} binary"
  printf '%s\n' "${GRYPE}"
  exit 0
fi

if validate_grype "${DEFAULT_GRYPE_PATH}"; then
  printf '%s\n' "${DEFAULT_GRYPE_PATH}"
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
  darwin/amd64) expected_sha="312ed375dcda6d8893b4ca5d517371fef1a50062ebb0b0dbe6bdb4ac2fb57c57" ;;
  darwin/arm64) expected_sha="bfcefa3f3b1690d9c77d847841b32ebd6106ab0e0e32f810924707e704d53584" ;;
  linux/amd64) expected_sha="38525dab1e06f162ebaa02f94d82d1f807076b011a44180cf2777edf1a7b9c26" ;;
  linux/arm64) expected_sha="935f628bdf9331ffdd946931ea5fdb50045d3970ba52670cbeb44a88f127291b" ;;
  *) die "unsupported platform: ${host_os}/${host_arch}" ;;
esac

asset="grype_${GRYPE_VERSION}_${host_os}_${host_arch}.tar.gz"
url="https://github.com/anchore/grype/releases/download/v${GRYPE_VERSION}/${asset}"
tmp_dir="$(mktemp -d "${TMPDIR:-/tmp}/tunnel-client-grype.XXXXXX")"
trap 'rm -rf "${tmp_dir}"' EXIT
archive="${tmp_dir}/${asset}"

echo "install_grype.sh: downloading Grype ${GRYPE_VERSION} for ${host_os}/${host_arch}" >&2
curl --fail --silent --show-error --location --retry 3 \
  --proto '=https' --tlsv1.2 \
  --output "${archive}" \
  "${url}"

actual_sha="$(sha256_file "${archive}")"
[[ "${actual_sha}" == "${expected_sha}" ]] ||
  die "checksum mismatch for ${asset}: got ${actual_sha}, want ${expected_sha}"

tar -xzf "${archive}" -C "${tmp_dir}" grype ||
  die "archive does not contain the expected grype binary"
[[ -f "${tmp_dir}/grype" ]] || die "archive extraction did not produce grype"

mkdir -p "${INSTALL_DIR}"
install -m 0755 "${tmp_dir}/grype" "${DEFAULT_GRYPE_PATH}"
validate_grype "${DEFAULT_GRYPE_PATH}" ||
  die "installed binary did not report Grype ${GRYPE_VERSION}"

printf '%s\n' "${DEFAULT_GRYPE_PATH}"
