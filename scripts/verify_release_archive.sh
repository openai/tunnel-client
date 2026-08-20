#!/usr/bin/env bash
set -euo pipefail

usage() {
  cat <<'EOF'
Usage:
  ./scripts/verify_release_archive.sh \
    --flavor client|runtime|runtime-cloudflared \
    --archive <release-zip> \
    --sbom <matching-spdx-json> \
    --checksums <SHA256SUMS.txt>

Verifies a downloaded release ZIP and matching SPDX sidecar against
SHA256SUMS.txt, then proves that the SBOM SHA256 inventory matches the ZIP
payload.
EOF
}

die() {
  echo "verify_release_archive.sh: $*" >&2
  exit 1
}

flavor=""
archive=""
sbom=""
checksums=""
while [[ $# -gt 0 ]]; do
  case "$1" in
    --flavor)
      flavor="${2:-}"
      shift 2
      ;;
    --archive)
      archive="${2:-}"
      shift 2
      ;;
    --sbom)
      sbom="${2:-}"
      shift 2
      ;;
    --checksums)
      checksums="${2:-}"
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
[[ -f "${archive}" ]] || die "archive does not exist: ${archive}"
[[ -f "${sbom}" ]] || die "SBOM does not exist: ${sbom}"
[[ -f "${checksums}" ]] || die "checksums file does not exist: ${checksums}"
command -v python3 >/dev/null 2>&1 || die "python3 is required"

resolved_script_path="$(python3 - "${BASH_SOURCE[0]}" <<'PY'
import os
import sys

print(os.path.realpath(sys.argv[1]))
PY
)"
script_dir="$(cd "$(dirname "${resolved_script_path}")" && pwd)"
if [[ -x "${script_dir}/scripts/verify_release_sbom.sh" ]]; then
  script_dir="${script_dir}/scripts"
fi
payload_dir="$(mktemp -d "${TMPDIR:-/tmp}/tunnel-client-release-sbom.XXXXXX")"
trap 'rm -rf "${payload_dir}"' EXIT

python3 - "${archive}" "${sbom}" "${checksums}" "${payload_dir}" <<'PY'
import hashlib
import pathlib
import re
import stat
import sys
import zipfile

archive_arg, sbom_arg, checksums_arg, payload_arg = sys.argv[1:]
archive_path = pathlib.Path(archive_arg)
sbom_path = pathlib.Path(sbom_arg)
checksums_path = pathlib.Path(checksums_arg)
payload_dir = pathlib.Path(payload_arg)
CHUNK_BYTES = 1024 * 1024
MAX_ARCHIVE_BYTES = 1024 * 1024 * 1024
MAX_MEMBER_BYTES = 512 * 1024 * 1024
MAX_TOTAL_UNCOMPRESSED_BYTES = 1024 * 1024 * 1024
MAX_SBOM_BYTES = 64 * 1024 * 1024
MAX_CHECKSUMS_BYTES = 1024 * 1024


def fail(message: str) -> None:
    raise SystemExit(message)


def digest(path: pathlib.Path) -> str:
    value = hashlib.sha256()
    with path.open("rb") as source:
        for chunk in iter(lambda: source.read(CHUNK_BYTES), b""):
            value.update(chunk)
    return value.hexdigest()


def checksum_for(name: str) -> str:
    matches = []
    for raw_line in checksums_path.read_text(encoding="utf-8").splitlines():
        match = re.fullmatch(r"([0-9a-f]{64}) [ *](.+)", raw_line)
        if match and match.group(2) == name:
            matches.append(match.group(1))
    if len(matches) != 1:
        fail(f"checksums file must contain exactly one SHA256 entry for {name}")
    return matches[0]


def basename(path: pathlib.Path, label: str) -> str:
    name = path.name
    if pathlib.PurePosixPath(name).name != name:
        fail(f"{label} filename must be a basename")
    return name


def copy_member(zipped: zipfile.ZipFile, info: zipfile.ZipInfo, destination: pathlib.Path) -> None:
    written = 0
    with zipped.open(info, "r") as source, destination.open("xb") as output:
        while True:
            chunk = source.read(CHUNK_BYTES)
            if not chunk:
                break
            written += len(chunk)
            if written > info.file_size or written > MAX_MEMBER_BYTES:
                fail(f"archive member exceeds declared size: {info.filename}")
            output.write(chunk)
    if written != info.file_size:
        fail(f"archive member size mismatch: {info.filename}")


def member_matches_file(
    zipped: zipfile.ZipFile, info: zipfile.ZipInfo, path: pathlib.Path
) -> bool:
    if info.file_size != path.stat().st_size:
        return False
    with zipped.open(info, "r") as embedded, path.open("rb") as external:
        while True:
            embedded_chunk = embedded.read(CHUNK_BYTES)
            external_chunk = external.read(CHUNK_BYTES)
            if embedded_chunk != external_chunk:
                return False
            if not embedded_chunk:
                return True


archive_name = basename(archive_path, "archive")
sbom_name = basename(sbom_path, "SBOM")
if not archive_name.endswith(".zip"):
    fail("archive filename must end with .zip")
if not sbom_name.endswith(".spdx.json"):
    fail("SBOM filename must end with .spdx.json")
archive_stem = archive_name[: -len(".zip")]
sbom_stem = sbom_name[: -len(".spdx.json")]
if archive_stem != sbom_stem:
    fail("archive and SBOM sidecar must use the same release stem")
if archive_path.stat().st_size > MAX_ARCHIVE_BYTES:
    fail("archive exceeds the 1 GiB verification limit")
if sbom_path.stat().st_size > MAX_SBOM_BYTES:
    fail("SBOM exceeds the 64 MiB verification limit")
if checksums_path.stat().st_size > MAX_CHECKSUMS_BYTES:
    fail("checksums file exceeds the 1 MiB verification limit")

for path, name in ((archive_path, archive_name), (sbom_path, sbom_name)):
    expected = checksum_for(name)
    actual = digest(path)
    if actual != expected:
        fail(f"SHA256 mismatch for {name}")

try:
    with zipfile.ZipFile(archive_path) as zipped:
        infos = zipped.infolist()
        names = [info.filename for info in infos]
        if len(names) != len(set(names)):
            fail("archive contains duplicate members")
        total_uncompressed_bytes = 0
        for info in infos:
            name = info.filename
            path = pathlib.PurePosixPath(name)
            windows_path = pathlib.PureWindowsPath(name)
            if (
                not name
                or name.startswith("/")
                or path.is_absolute()
                or ".." in path.parts
                or "\\" in name
                or windows_path.is_absolute()
                or ".." in windows_path.parts
            ):
                fail("archive contains an unsafe path")
            if name.endswith("/"):
                fail("archive must not contain directory entries")
            if path.name != name or windows_path.name != name:
                fail("archive members must be at the ZIP root")
            unix_mode = (info.external_attr >> 16) & 0o177777
            file_type = stat.S_IFMT(unix_mode)
            if file_type == stat.S_IFLNK:
                fail("archive must not contain symlinks")
            if file_type not in (0, stat.S_IFREG):
                fail("archive must contain only regular files")
            if info.file_size < 0 or info.file_size > MAX_MEMBER_BYTES:
                fail(f"archive member exceeds the 512 MiB verification limit: {name}")
            total_uncompressed_bytes += info.file_size
            if total_uncompressed_bytes > MAX_TOTAL_UNCOMPRESSED_BYTES:
                fail("archive exceeds the 1 GiB uncompressed verification limit")

        embedded = [info for info in infos if info.filename == sbom_name]
        if len(embedded) != 1:
            fail("archive must contain exactly one embedded SBOM sidecar")
        if not member_matches_file(zipped, embedded[0], sbom_path):
            fail("embedded SBOM sidecar differs from downloaded SBOM")

        for info in infos:
            if info.filename == sbom_name:
                continue
            copy_member(zipped, info, payload_dir / info.filename)
except (OSError, RuntimeError, zipfile.BadZipFile) as exc:
    fail(f"archive is not a readable ZIP: {exc}")

print("release archive checksums and embedded SBOM: passed")
PY

"${script_dir}/verify_release_sbom.sh" \
  --flavor "${flavor}" \
  --staged-dir "${payload_dir}" \
  --sbom "${sbom}"

printf '%s downloadable release SBOM: passed\n' "${flavor}"
