#!/usr/bin/env bash
set -euo pipefail

usage() {
  cat <<'EOF'
Usage:
  ./scripts/augment_sbom_files.sh \
    --input <spdx-json> \
    --staged-dir <payload-directory> \
    --output <spdx-json>

Adds deterministic SPDX file entries for every staged payload file that Syft
did not catalog itself. The output describes exactly the pre-SBOM payload and
never includes the output document.
EOF
}

die() {
  echo "augment_sbom_files.sh: $*" >&2
  exit 1
}

input=""
staged_dir=""
output=""
while [[ $# -gt 0 ]]; do
  case "$1" in
    --input)
      input="${2:-}"
      shift 2
      ;;
    --staged-dir)
      staged_dir="${2:-}"
      shift 2
      ;;
    --output)
      output="${2:-}"
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

[[ -f "${input}" ]] || die "--input must name an existing SPDX JSON file"
[[ -d "${staged_dir}" ]] || die "--staged-dir must name an existing directory"
[[ -n "${output}" ]] || die "--output is required"
command -v python3 >/dev/null 2>&1 || die "python3 is required"

staged_dir="$(cd "${staged_dir}" && pwd)"
output_parent="$(dirname "${output}")"
mkdir -p "${output_parent}"
output_parent="$(cd "${output_parent}" && pwd)"
output="${output_parent}/$(basename "${output}")"
tmp_output="$(mktemp "${output_parent}/.$(basename "${output}").tmp.XXXXXX")"
trap 'rm -f "${tmp_output}"' EXIT

python3 - "${input}" "${staged_dir}" "${tmp_output}" <<'PY'
import hashlib
import json
import pathlib
import sys

input_path = pathlib.Path(sys.argv[1])
staged_root = pathlib.Path(sys.argv[2])
output_path = pathlib.Path(sys.argv[3])

with input_path.open(encoding="utf-8") as source:
    document = json.load(source)
if document.get("spdxVersion") != "SPDX-2.3":
    raise SystemExit("input document is not SPDX 2.3 JSON")


def normalize_name(value: str) -> str:
    value = value.replace("\\", "/")
    while value.startswith("./"):
        value = value[2:]
    return value


def checksums(path: pathlib.Path) -> list[dict[str, str]]:
    sha1 = hashlib.sha1()
    sha256 = hashlib.sha256()
    with path.open("rb") as source:
        for chunk in iter(lambda: source.read(1024 * 1024), b""):
            sha1.update(chunk)
            sha256.update(chunk)
    return [
        {"algorithm": "SHA1", "checksumValue": sha1.hexdigest()},
        {"algorithm": "SHA256", "checksumValue": sha256.hexdigest()},
    ]


files = document.get("files")
if files is None:
    files = []
if not isinstance(files, list):
    raise SystemExit("SPDX files field must be an array")

by_name: dict[str, dict] = {}
for entry in files:
    if not isinstance(entry, dict) or not isinstance(entry.get("fileName"), str):
        raise SystemExit("SPDX file entry is malformed")
    name = normalize_name(entry["fileName"])
    entry["fileName"] = name
    by_name[name] = entry

for path in sorted(
    (candidate for candidate in staged_root.rglob("*") if candidate.is_file()),
    key=lambda candidate: candidate.relative_to(staged_root).as_posix(),
):
    name = path.relative_to(staged_root).as_posix()
    if name in by_name:
        continue
    suffix = hashlib.sha256(name.encode("utf-8")).hexdigest()[:16]
    by_name[name] = {
        "fileName": name,
        "SPDXID": "SPDXRef-File-staged-" + suffix,
        "fileTypes": ["OTHER"],
        "checksums": checksums(path),
        "licenseConcluded": "NOASSERTION",
        "licenseInfoInFiles": ["NOASSERTION"],
        "copyrightText": "NOASSERTION",
    }

ordered_files = [by_name[name] for name in sorted(by_name)]
document["files"] = ordered_files
document["documentDescribes"] = sorted(
    entry["SPDXID"]
    for entry in ordered_files
    if isinstance(entry.get("SPDXID"), str) and entry["SPDXID"]
)

with output_path.open("w", encoding="utf-8", newline="\n") as destination:
    json.dump(document, destination, ensure_ascii=False, indent=2, sort_keys=True)
    destination.write("\n")
PY

mv "${tmp_output}" "${output}"
trap - EXIT
printf 'wrote %s\n' "${output}"
