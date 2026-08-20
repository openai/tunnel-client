#!/usr/bin/env bash
set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
PROJECT_DIR="$(cd "${SCRIPT_DIR}/.." && pwd)"

usage() {
  cat <<'EOF'
Usage:
  runtime_release_archive_smoke_test.sh --flavor runtime|runtime-cloudflared --binary PATH [--target-os GOOS] [--target-arch GOARCH]

Builds a release-shaped ZIP around the native runtime binary, verifies the
archive/SBOM contract, extracts it, and smoke-tests the packaged binary's
version and help surface. This does not run fake-service behavioral parity.
The runtime-cloudflared fixture uses a harmless local companion and never
contacts Cloudflare.
EOF
}

die() {
  echo "runtime_release_archive_smoke_test.sh: $*" >&2
  exit 1
}

flavor=""
binary_path=""
target_os=""
target_arch=""

while [[ $# -gt 0 ]]; do
  case "$1" in
    --flavor)
      [[ $# -ge 2 ]] || die "--flavor requires a value"
      flavor="$2"
      shift 2
      ;;
    --binary)
      [[ $# -ge 2 ]] || die "--binary requires a value"
      binary_path="$2"
      shift 2
      ;;
    --target-os)
      [[ $# -ge 2 ]] || die "--target-os requires a value"
      target_os="$2"
      shift 2
      ;;
    --target-arch)
      [[ $# -ge 2 ]] || die "--target-arch requires a value"
      target_arch="$2"
      shift 2
      ;;
    -h|--help)
      usage
      exit 0
      ;;
    *)
      die "unknown argument: $1"
      ;;
  esac
done

case "${flavor}" in
  runtime|runtime-cloudflared) ;;
  *) die "--flavor must be runtime or runtime-cloudflared" ;;
esac
[[ -n "${binary_path}" ]] || die "--binary is required"
[[ -f "${binary_path}" ]] || die "binary does not exist: ${binary_path}"

case "$(uname -s)" in
  Linux|Darwin) ;;
  *)
    echo "SKIP: native runtime release archive execution requires a Unix host"
    exit 0
    ;;
esac

command -v go >/dev/null 2>&1 || die "go is required"

[[ -n "${target_os}" ]] || target_os="$(go env GOOS)"
[[ -n "${target_arch}" ]] || target_arch="$(go env GOARCH)"
host_os="$(go env GOHOSTOS)"
host_arch="$(go env GOHOSTARCH)"
if [[ "${target_os}" != "${host_os}" || "${target_arch}" != "${host_arch}" ]]; then
  echo "SKIP: native runtime release archive execution requires a host-target binary"
  exit 0
fi

[[ -x "${binary_path}" ]] || die "binary is not executable: ${binary_path}"
command -v python3 >/dev/null 2>&1 || die "python3 is required"
command -v unzip >/dev/null 2>&1 || die "unzip is required"

expected_name="tunnel-client-runtime"
if [[ "${flavor}" == "runtime-cloudflared" ]]; then
  expected_name="tunnel-client-runtime-cloudflared"
fi

tmp_dir="$(mktemp -d "${TMPDIR:-/tmp}/tunnel-client-runtime-archive.XXXXXX")"
trap 'rm -rf "${tmp_dir}"' EXIT
stage_dir="${tmp_dir}/stage"
dist_dir="${tmp_dir}/dist"
extract_dir="${tmp_dir}/extracted"
mkdir -p "${stage_dir}" "${dist_dir}" "${extract_dir}"

platform="${target_os}-${target_arch}"
release_stem="${expected_name}-v0.0.0-native-${platform}"
license_name="${release_stem}-licenses.txt"
sbom_name="${release_stem}.spdx.json"
archive_name="${release_stem}.zip"

cp "${binary_path}" "${stage_dir}/${expected_name}"
chmod 0755 "${stage_dir}/${expected_name}"
cp "${PROJECT_DIR}/LICENSE" "${stage_dir}/LICENSE"
cp "${PROJECT_DIR}/NOTICE" "${stage_dir}/NOTICE"
printf '%s\n' "test-only native release archive license report" >"${stage_dir}/${license_name}"

if [[ "${flavor}" == "runtime-cloudflared" ]]; then
  printf '%s\n' '#!/usr/bin/env sh' 'exit 0' >"${stage_dir}/cloudflared"
  chmod 0755 "${stage_dir}/cloudflared"
  printf '%s\n' '{"version":"test-only","source":"local-fixture"}' >"${stage_dir}/cloudflared-manifest.json"
fi

python3 - "${expected_name}" "${stage_dir}" "${dist_dir}/${sbom_name}" <<'PY'
import hashlib
import json
import pathlib
import sys

name, stage_arg, output_arg = sys.argv[1:]
stage = pathlib.Path(stage_arg)
output = pathlib.Path(output_arg)
files = []
described = []
for index, path in enumerate(sorted(stage.iterdir(), key=lambda item: item.name), start=1):
    if not path.is_file() or path.is_symlink():
        raise SystemExit(f"unexpected staged entry: {path}")
    spdx_id = f"SPDXRef-File-{index}"
    described.append(spdx_id)
    files.append(
        {
            "fileName": path.name,
            "SPDXID": spdx_id,
            "checksums": [
                {
                    "algorithm": "SHA256",
                    "checksumValue": hashlib.sha256(path.read_bytes()).hexdigest(),
                }
            ],
        }
    )

document = {
    "spdxVersion": "SPDX-2.3",
    "dataLicense": "CC0-1.0",
    "SPDXID": "SPDXRef-DOCUMENT",
    "name": name,
    "documentNamespace": "https://example.invalid/tunnel-client/native-archive-smoke",
    "creationInfo": {
        "created": "1980-01-01T00:00:00Z",
        "creators": ["Tool: runtime_release_archive_smoke_test.sh"],
    },
    "files": files,
    "documentDescribes": described,
}
output.write_text(json.dumps(document, sort_keys=True, indent=2) + "\n", encoding="utf-8")
PY

"${SCRIPT_DIR}/package_release_archive.sh" --flavor "${flavor}" --staged-dir "${stage_dir}" --sbom "${dist_dir}/${sbom_name}" --archive "${dist_dir}/${archive_name}"

python3 - "${dist_dir}" "${archive_name}" "${sbom_name}" <<'PY'
import hashlib
import pathlib
import sys

directory = pathlib.Path(sys.argv[1])
names = sys.argv[2:]
with (directory / "SHA256SUMS.txt").open("w", encoding="utf-8") as output:
    for name in names:
        digest = hashlib.sha256((directory / name).read_bytes()).hexdigest()
        output.write(f"{digest}  {name}\n")
PY

"${SCRIPT_DIR}/verify_release_archive.sh" --flavor "${flavor}" --archive "${dist_dir}/${archive_name}" --sbom "${dist_dir}/${sbom_name}" --checksums "${dist_dir}/SHA256SUMS.txt"

unzip -q "${dist_dir}/${archive_name}" -d "${extract_dir}"
[[ -x "${extract_dir}/${expected_name}" ]] ||
  die "standard unzip did not preserve packaged binary execute permissions"
if [[ "${flavor}" == "runtime-cloudflared" ]]; then
  [[ -x "${extract_dir}/cloudflared" ]] ||
    die "standard unzip did not preserve packaged companion execute permissions"
fi

version_output="$("${extract_dir}/${expected_name}" --version)"
grep -Eq "(^|[[:space:]])flavor=${flavor}([[:space:]]|$)" <<<"${version_output}" >/dev/null || {
  echo "${version_output}" >&2
  die "packaged binary version output did not identify flavor=${flavor}"
}
"${extract_dir}/${expected_name}" run --help >/dev/null

echo "${flavor} native release archive smoke: passed"
