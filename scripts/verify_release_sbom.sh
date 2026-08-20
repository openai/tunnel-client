#!/usr/bin/env bash
set -euo pipefail

usage() {
  cat <<'EOF'
Usage:
  ./scripts/verify_release_sbom.sh \
    --flavor client|runtime|runtime-cloudflared \
    --staged-dir <pre-sbom-payload-dir> \
    --sbom <path-to-spdx-json>

Validates that a release-sidecar SBOM is SPDX 2.3, names the expected flavor,
and describes a pre-SBOM payload that contains the required release files.
For a downloaded release ZIP, use ./scripts/verify_release_archive.sh.
EOF
}

die() {
  echo "verify_release_sbom.sh: $*" >&2
  exit 1
}

flavor=""
staged_dir=""
sbom=""
while [[ $# -gt 0 ]]; do
  case "$1" in
    --flavor)
      flavor="${2:-}"
      shift 2
      ;;
    --staged-dir)
      staged_dir="${2:-}"
      shift 2
      ;;
    --sbom)
      sbom="${2:-}"
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
[[ -d "${staged_dir}" ]] || die "staged directory does not exist: ${staged_dir}"
[[ -f "${sbom}" ]] || die "SBOM does not exist: ${sbom}"
command -v python3 >/dev/null 2>&1 || die "python3 is required"

expected_name="tunnel-client"
[[ "${flavor}" == "runtime" ]] && expected_name="tunnel-client-runtime"
[[ "${flavor}" == "runtime-cloudflared" ]] && expected_name="tunnel-client-runtime-cloudflared"

python3 - "${flavor}" "${expected_name}" "${staged_dir}" "${sbom}" <<'PY'
import hashlib
import json
import pathlib
import sys

flavor, expected_name, staged_arg, sbom_arg = sys.argv[1:]
staged_dir = pathlib.Path(staged_arg)
sbom_path = pathlib.Path(sbom_arg)
with sbom_path.open(encoding="utf-8") as source:
    document = json.load(source)

if document.get("spdxVersion") != "SPDX-2.3":
    raise SystemExit("SBOM is not SPDX 2.3")
if document.get("name") != expected_name:
    raise SystemExit(
        f"SBOM name is {document.get('name')!r}, want {expected_name!r}"
    )


def normalize_name(value: str) -> str:
    value = value.replace("\\", "/")
    while value.startswith("./"):
        value = value[2:]
    return value


def fail(message: str) -> None:
    raise SystemExit(message)


def exact_one(candidates: list[str], label: str) -> str:
    if len(candidates) != 1:
        fail(f"staged payload must contain exactly one {label}: {candidates}")
    return candidates[0]


# This verifier runs before the SBOM sidecar is copied into the ZIP. Bind the
# external sidecar name to the one platform license report in the staged
# payload so a valid SPDX document cannot be paired with an unrelated release
# filename.
sbom_name = sbom_path.name
sbom_suffix = ".spdx.json"
if not sbom_name.endswith(sbom_suffix):
    fail("SBOM filename must end with .spdx.json")
sidecar_stem = sbom_name[: -len(sbom_suffix)]
if not sidecar_stem.startswith(expected_name + "-") or sidecar_stem == expected_name + "-":
    fail(f"SBOM filename does not match {expected_name} release sidecar: {sbom_name}")
longer_flavor_prefixes = {
    "client": ("tunnel-client-runtime-",),
    "runtime": ("tunnel-client-runtime-cloudflared-",),
    "runtime-cloudflared": (),
}
if sidecar_stem.startswith(longer_flavor_prefixes[flavor]):
    fail(f"SBOM filename belongs to a different release flavor: {sbom_name}")
license_name = f"{sidecar_stem}-licenses.txt"

staged_paths = sorted(staged_dir.rglob("*"))
for path in staged_paths:
    relative = path.relative_to(staged_dir).as_posix()
    if path.is_symlink():
        fail(f"staged payload must not contain symlinks: {relative}")
    if path.is_dir():
        fail(f"staged payload must not contain directories: {relative}")
    if not path.is_file():
        fail(f"staged payload contains a non-file entry: {relative}")

actual_names = [path.relative_to(staged_dir).as_posix() for path in staged_paths]
binary = exact_one(
    [name for name in (expected_name, expected_name + ".exe") if name in actual_names],
    f"{expected_name} binary",
)
license_reports = [
    name
    for name in actual_names
    if name.startswith(expected_name + "-") and name.endswith("-licenses.txt")
]
if license_reports != [license_name]:
    fail(
        "staged payload must contain exactly the license report matching the "
        f"SBOM sidecar: got {license_reports}, want {[license_name]}"
    )

expected_files = {"LICENSE", "NOTICE", binary, license_name}
includes_companion = flavor in {"client", "runtime-cloudflared"}
if includes_companion:
    companion = "cloudflared.exe" if binary.endswith(".exe") else "cloudflared"
    exact_one(
        [name for name in ("cloudflared", "cloudflared.exe") if name in actual_names],
        "cloudflared companion binary",
    )
    expected_files.update({companion, "cloudflared-manifest.json"})

actual_set = set(actual_names)
if actual_set != expected_files:
    missing = sorted(expected_files - actual_set)
    unexpected = sorted(actual_set - expected_files)
    fail(
        "staged payload differs from the release allowlist: "
        f"missing={missing}, unexpected={unexpected}"
    )

files = document.get("files")
if not isinstance(files, list) or not files:
    raise SystemExit("SBOM has no file inventory")
by_name = {}
for entry in files:
    if not isinstance(entry, dict) or not isinstance(entry.get("fileName"), str):
        raise SystemExit("SBOM contains a malformed file entry")
    name = normalize_name(entry["fileName"])
    if name in by_name:
        raise SystemExit(f"SBOM contains duplicate file entry: {name}")
    by_name[name] = entry

sbom_names = sorted(by_name)
if actual_names != sbom_names:
    missing = sorted(set(actual_names) - set(sbom_names))
    unexpected = sorted(set(sbom_names) - set(actual_names))
    raise SystemExit(
        "SBOM file inventory differs from staged payload: "
        f"missing={missing}, unexpected={unexpected}"
    )

described = document.get("documentDescribes")
if not isinstance(described, list):
    raise SystemExit("SBOM documentDescribes must list staged file SPDX IDs")
described_ids = set(described)
for name in actual_names:
    entry = by_name[name]
    spdx_id = entry.get("SPDXID")
    if not isinstance(spdx_id, str) or spdx_id not in described_ids:
        raise SystemExit(f"SBOM does not describe staged file: {name}")
    checksums = entry.get("checksums")
    if not isinstance(checksums, list):
        raise SystemExit(f"SBOM file has no checksums: {name}")
    expected_sha = next(
        (
            item.get("checksumValue")
            for item in checksums
            if isinstance(item, dict) and item.get("algorithm") == "SHA256"
        ),
        None,
    )
    if not isinstance(expected_sha, str):
        raise SystemExit(f"SBOM file has no SHA256 checksum: {name}")
    digest = hashlib.sha256((staged_dir / name).read_bytes()).hexdigest()
    if digest != expected_sha:
        raise SystemExit(f"SBOM SHA256 mismatch for staged file: {name}")
PY

printf '%s release SBOM contract: passed\n' "${flavor}"
