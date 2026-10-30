#!/usr/bin/env bash
set -euo pipefail

readonly SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
readonly PROJECT_ROOT="$(cd "${SCRIPT_DIR}/.." && pwd)"
readonly MODULE_PATH="github.com/openai/tunnel-client"
readonly NDK_REVISION="30.0.16248370"
readonly ANDROID_MIN_API=28

die() {
  echo "package_android_runtime.sh: $*" >&2
  exit 1
}

usage() {
  cat <<'EOF'
Usage:
  ANDROID_NDK_HOME=/path/to/android-ndk-r30 ./scripts/package_android_runtime.sh \
    --version vX.Y.Z --source-commit <full-sha> \
    --output-dir dist/public --metadata-dir dist/release-metadata

Builds and verifies the experimental Android/arm64 runtime with the release
version and source commit, then produces its ZIP, license report, SPDX 2.3
sidecar, and release payload metadata. The version must match pkg/version/VERSION.
EOF
}

version=""
source_commit=""
output_dir=""
metadata_dir=""
while [[ $# -gt 0 ]]; do
  case "$1" in
    --version|--source-commit|--output-dir|--metadata-dir)
      [[ $# -ge 2 && -n "$2" ]] || die "$1 requires a value"
      case "$1" in
        --version) version="$2" ;;
        --source-commit) source_commit="$2" ;;
        --output-dir) output_dir="$2" ;;
        --metadata-dir) metadata_dir="$2" ;;
      esac
      shift 2
      ;;
    -h|--help) usage; exit 0 ;;
    *) usage >&2; die "unknown argument: $1" ;;
  esac
done
[[ -n "${version}" && -n "${output_dir}" && -n "${metadata_dir}" ]] || die "--version, --output-dir and --metadata-dir are required"
[[ "${source_commit}" =~ ^[0-9a-f]{40}$ ]] || die "--source-commit must be a full lowercase Git SHA"
"${SCRIPT_DIR}/release_tag.sh" parse "${version}" >/dev/null
"${SCRIPT_DIR}/release_tag.sh" check-source-version "${version}"
ndk="${ANDROID_NDK_HOME:-${ANDROID_NDK_ROOT:-}}"
[[ -n "${ndk}" ]] || die "set ANDROID_NDK_HOME to Android NDK r30 (${NDK_REVISION})"
[[ -d "${ndk}" ]] || die "Android NDK directory does not exist"
ndk="$(cd "${ndk}" && pwd)"
for notice in NOTICE NOTICE.toolchain; do
  [[ -f "${ndk}/${notice}" ]] || die "Android NDK ${notice} is missing"
done
for tool in go make python3; do
  command -v "${tool}" >/dev/null 2>&1 || die "${tool} is required"
done

# Pass both values as Make command-line variables so the nested Android build
# retains release stamping rather than replacing the full commit with a short SHA.
make -C "${PROJECT_ROOT}" runtime-android \
  ANDROID_NDK_HOME="${ndk}" ANDROID_API="${ANDROID_MIN_API}" \
  GIT_SHA="${source_commit}" \
  LDFLAGS="-X ${MODULE_PATH}/pkg/version.GitSHA=${source_commit} -X ${MODULE_PATH}/pkg/version.semanticVersion=${version#v}"

case "$(uname -s)" in
  Linux) host_tag=linux-x86_64 ;;
  Darwin) host_tag=darwin-x86_64 ;;
  *) die "Android packaging requires a supported Linux or macOS NDK host" ;;
esac
compiler="${ndk}/toolchains/llvm/prebuilt/${host_tag}/bin/aarch64-linux-android${ANDROID_MIN_API}-clang"
binary="${PROJECT_ROOT}/bin/android_arm64/tunnel-client-runtime"
[[ -f "${binary}" ]] || die "Android build did not produce the runtime binary"
work_dir="$(mktemp -d "${TMPDIR:-/tmp}/tunnel-client-android-package.XXXXXX")"
trap 'rm -rf "${work_dir}"' EXIT
stage_dir="${work_dir}/stage"
mkdir -p "${stage_dir}" "${output_dir}" "${metadata_dir}"
output_dir="$(cd "${output_dir}" && pwd)"
metadata_dir="$(cd "${metadata_dir}" && pwd)"
stem="tunnel-client-runtime-${version}-android-arm64"
license_name="${stem}-licenses.txt"
sbom="${output_dir}/${stem}.spdx.json"
archive="${output_dir}/${stem}.zip"

go version -m "${binary}" > "${work_dir}/build-info.txt"
(
  cd "${PROJECT_ROOT}"
  GOOS=android GOARCH=arm64 CGO_ENABLED=1 CC="${compiler}" \
    go list -deps -json ./cmd/client-runtime > "${work_dir}/packages.json"
)
python3 "${SCRIPT_DIR}/build_android_license_report.py" \
  --binary-build-info "${work_dir}/build-info.txt" \
  --package-graph "${work_dir}/packages.json" \
  --base-report "${PROJECT_ROOT}/compliance/oss-license-report-runtime.txt" \
  --output "${output_dir}/${license_name}"
cp "${binary}" "${PROJECT_ROOT}/LICENSE" "${stage_dir}/"
cp "${output_dir}/${license_name}" "${stage_dir}/${license_name}"
cp "${PROJECT_ROOT}/NOTICE" "${stage_dir}/NOTICE"
go_root="$(go env GOROOT)"
[[ -f "${go_root}/LICENSE" ]] || die "Go toolchain LICENSE is missing"
{
  printf '\nGo toolchain license\n\n'
  cat "${go_root}/LICENSE"
  printf '\nAndroid NDK %s distribution notices\n' "${NDK_REVISION}"
  printf 'These notices cover the NDK distribution; not every component is linked into this runtime.\n\n'
  cat "${ndk}/NOTICE" "${ndk}/NOTICE.toolchain"
} >> "${stage_dir}/NOTICE"
# Keep scanner scratch files inside our temporary directory; only the final
# sidecar belongs in the directory uploaded by the release workflow.
SBOM_SOURCE_VERSION="${version}" "${SCRIPT_DIR}/generate_sbom.sh" \
  --flavor runtime --staged-dir "${stage_dir}" --output "${work_dir}/${stem}.spdx.json"
cp "${work_dir}/${stem}.spdx.json" "${sbom}"
"${SCRIPT_DIR}/verify_release_sbom.sh" --flavor runtime --staged-dir "${stage_dir}" --sbom "${sbom}"

python3 - "${stage_dir}" "${sbom}" "${metadata_dir}/runtime-android-arm64.json" \
  "${version}" "${source_commit}" "$(go env GOVERSION)" "${NDK_REVISION}" "${ANDROID_MIN_API}" "${license_name}" <<'PY'
import hashlib
import json
import pathlib
import sys

stage_arg, sbom_arg, output_arg, version, commit, go_version, ndk, api, license_name = sys.argv[1:]
stage, sbom = pathlib.Path(stage_arg), pathlib.Path(sbom_arg)

def digest(path):
    return hashlib.sha256(path.read_bytes()).hexdigest()

files = [{"path": path.name, "sha256": digest(path)} for path in sorted(stage.iterdir())]
canonical = json.dumps(files, separators=(",", ":"), sort_keys=True).encode()
metadata = {
    "schemaVersion": 1,
    "flavor": "runtime",
    "semanticVersion": version,
    "commitSHA": commit,
    "goVersion": go_version,
    "goos": "android",
    "goarch": "arm64",
    "cgoEnabled": "1",
    "buildFlags": ["-trimpath", "-buildvcs=false", "-buildmode=pie"],
    "supportStatus": "experimental",
    "androidToolchain": {"ndkRevision": ndk, "minimumAPI": int(api)},
    "preSbomPayload": {
        "algorithm": "sha256-canonical-file-manifest-v1",
        "sha256": hashlib.sha256(canonical).hexdigest(),
        "files": files,
    },
    "licenseReport": {"name": license_name, "sha256": digest(stage / license_name)},
    "releaseSbom": {"name": sbom.name, "sha256": digest(sbom)},
}
pathlib.Path(output_arg).write_text(json.dumps(metadata, indent=2, sort_keys=True) + "\n", encoding="utf-8")
PY

cp "${sbom}" "${stage_dir}/$(basename "${sbom}")"
python3 - "${stage_dir}" "${archive}" "${sbom}" "${work_dir}/SHA256SUMS.txt" <<'PY'
import hashlib
import pathlib
import sys
import zipfile

stage_arg, archive_arg, sbom_arg, checksums_arg = sys.argv[1:]
stage, archive, sbom = map(pathlib.Path, (stage_arg, archive_arg, sbom_arg))
with zipfile.ZipFile(archive, "w", compression=zipfile.ZIP_DEFLATED, compresslevel=9) as zipped:
    for path in sorted(stage.iterdir()):
        zipped.write(path, path.name)
pathlib.Path(checksums_arg).write_text(
    "".join(f"{hashlib.sha256(path.read_bytes()).hexdigest()}  {path.name}\n" for path in (archive, sbom)),
    encoding="utf-8",
)
PY
"${SCRIPT_DIR}/check_runtime_zip_contents.sh" --flavor runtime --archive "${archive}"
"${SCRIPT_DIR}/verify_release_archive.sh" --flavor runtime --archive "${archive}" \
  --sbom "${sbom}" --checksums "${work_dir}/SHA256SUMS.txt"
printf 'Packaged experimental Android runtime: %s\n' "${archive}"
