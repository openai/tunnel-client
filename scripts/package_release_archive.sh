#!/usr/bin/env bash
set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"

usage() {
  cat <<'EOF'
Usage:
  package_release_archive.sh \
    --flavor client|runtime|runtime-cloudflared \
    --staged-dir PATH \
    --sbom PATH \
    --archive PATH

Packages a verified release payload without mutating the pre-SBOM staging
directory. The resulting ZIP contains only root-level payload files plus the
provided SPDX sidecar.
EOF
}

die() {
  echo "error: $*" >&2
  exit 1
}

flavor=""
staged_dir=""
sbom_path=""
archive_path=""

while [[ $# -gt 0 ]]; do
  case "$1" in
    --flavor)
      [[ $# -ge 2 ]] || die "--flavor requires a value"
      flavor="$2"
      shift 2
      ;;
    --staged-dir)
      [[ $# -ge 2 ]] || die "--staged-dir requires a value"
      staged_dir="$2"
      shift 2
      ;;
    --sbom)
      [[ $# -ge 2 ]] || die "--sbom requires a value"
      sbom_path="$2"
      shift 2
      ;;
    --archive)
      [[ $# -ge 2 ]] || die "--archive requires a value"
      archive_path="$2"
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
  client|runtime|runtime-cloudflared) ;;
  *) die "--flavor must be client, runtime, or runtime-cloudflared" ;;
esac

[[ -n "${staged_dir}" ]] || die "--staged-dir is required"
[[ -n "${sbom_path}" ]] || die "--sbom is required"
[[ -n "${archive_path}" ]] || die "--archive is required"
[[ -d "${staged_dir}" ]] || die "staged directory not found: ${staged_dir}"
[[ -f "${sbom_path}" ]] || die "SBOM not found: ${sbom_path}"
command -v python3 >/dev/null 2>&1 || die "python3 is required"

staged_dir="$(cd "${staged_dir}" && pwd)"
sbom_path="$(cd "$(dirname "${sbom_path}")" && pwd)/$(basename "${sbom_path}")"
mkdir -p "$(dirname "${archive_path}")"
archive_path="$(cd "$(dirname "${archive_path}")" && pwd)/$(basename "${archive_path}")"

case "${archive_path}" in
  "${staged_dir}"/*) die "archive path must not be inside the staged directory" ;;
esac

tmp_dir="$(mktemp -d)"
candidate_dir="$(mktemp -d "$(dirname "${archive_path}")/.tunnel-client-release-archive.XXXXXX")"
trap 'rm -rf "${tmp_dir}" "${candidate_dir}"' EXIT
payload_dir="${tmp_dir}/payload"
candidate_archive="${candidate_dir}/$(basename "${archive_path}")"
mkdir -p "${payload_dir}"

python3 - "${staged_dir}" "${payload_dir}" "${sbom_path}" <<'PY'
import pathlib
import shutil
import stat
import sys

source = pathlib.Path(sys.argv[1])
destination = pathlib.Path(sys.argv[2])
sbom = pathlib.Path(sys.argv[3])
if sbom.is_symlink() or not sbom.is_file():
    raise SystemExit(f"error: SBOM must be a regular file: {sbom}")

for item in sorted(source.iterdir(), key=lambda path: path.name):
    if item.is_symlink() or not item.is_file():
        raise SystemExit(f"error: staged payload must contain only regular root files: {item}")
    target = destination / item.name
    shutil.copyfile(item, target)
    target.chmod(stat.S_IMODE(item.stat().st_mode))

sbom_target = destination / sbom.name
if sbom_target.exists():
    raise SystemExit(f"error: staged payload already contains SBOM name: {sbom.name}")
shutil.copyfile(sbom, sbom_target)
sbom_target.chmod(stat.S_IMODE(sbom.stat().st_mode))
PY

python3 - "${payload_dir}" "${candidate_archive}" <<'PY'
import os
import pathlib
import stat
import sys
import zipfile

payload = pathlib.Path(sys.argv[1])
archive = pathlib.Path(sys.argv[2])
temporary_archive = archive.with_name(f".{archive.name}.tmp.{os.getpid()}")

try:
    with zipfile.ZipFile(
        temporary_archive,
        "w",
        compression=zipfile.ZIP_DEFLATED,
        compresslevel=9,
    ) as output:
        for item in sorted(payload.iterdir(), key=lambda path: path.name):
            if item.is_symlink() or not item.is_file():
                raise SystemExit(f"error: package payload must contain only regular root files: {item}")
            info = zipfile.ZipInfo(item.name, date_time=(1980, 1, 1, 0, 0, 0))
            info.create_system = 3
            info.compress_type = zipfile.ZIP_DEFLATED
            info.external_attr = (stat.S_IFREG | stat.S_IMODE(item.stat().st_mode)) << 16
            with item.open("rb") as source, output.open(info, "w") as target:
                while True:
                    chunk = source.read(1024 * 1024)
                    if not chunk:
                        break
                    target.write(chunk)
    os.replace(temporary_archive, archive)
finally:
    temporary_archive.unlink(missing_ok=True)
PY

checksums_path="${tmp_dir}/SHA256SUMS.txt"
python3 - "${candidate_archive}" "${sbom_path}" "${checksums_path}" <<'PY'
import hashlib
import pathlib
import sys

archive = pathlib.Path(sys.argv[1])
sbom = pathlib.Path(sys.argv[2])
checksums = pathlib.Path(sys.argv[3])


def digest(path: pathlib.Path) -> str:
    value = hashlib.sha256()
    with path.open("rb") as source:
        for chunk in iter(lambda: source.read(1024 * 1024), b""):
            value.update(chunk)
    return value.hexdigest()


with checksums.open("w", encoding="utf-8") as output:
    for path in (archive, sbom):
        output.write(f"{digest(path)}  {path.name}\n")
PY

"${SCRIPT_DIR}/verify_release_archive.sh" --flavor "${flavor}" --archive "${candidate_archive}" --sbom "${sbom_path}" --checksums "${checksums_path}"

case "${flavor}" in
  runtime|runtime-cloudflared)
    "${SCRIPT_DIR}/check_runtime_zip_contents.sh" --flavor "${flavor}" --archive "${candidate_archive}"
    ;;
esac

python3 - "${candidate_archive}" "${archive_path}" <<'PY'
import os
import sys

os.replace(sys.argv[1], sys.argv[2])
PY

echo "release archive verified: ${archive_path}"
