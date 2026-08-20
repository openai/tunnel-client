#!/usr/bin/env bash
set -euo pipefail

usage() {
  cat <<'EOF'
Usage:
  ./scripts/verify_sbom_baselines.sh [--compliance-dir <path>]

Verifies that every mirrored SPDX baseline in compliance/ is covered by
sbom-baseline-manifest.json, has the recorded SHA256 digest, and parses as
SPDX 2.3. This checks the mirrored source baselines; it does not validate
release archive bytes.
EOF
}

die() {
  echo "verify_sbom_baselines.sh: $*" >&2
  exit 1
}

compliance_dir=""
while [[ $# -gt 0 ]]; do
  case "$1" in
    --compliance-dir)
      compliance_dir="${2:-}"
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

command -v python3 >/dev/null 2>&1 || die "python3 is required"
if [[ -z "${compliance_dir}" ]]; then
  resolved_script_path="$(python3 - "${BASH_SOURCE[0]}" <<'PY'
import os
import sys

print(os.path.realpath(sys.argv[1]))
PY
)"
  script_dir="$(cd "$(dirname "${resolved_script_path}")" && pwd)"
  compliance_dir="${script_dir}/../compliance"
fi
[[ -d "${compliance_dir}" ]] || die "compliance directory does not exist: ${compliance_dir}"

python3 - "${compliance_dir}" <<'PY'
import hashlib
import json
import pathlib
import re
import sys

compliance_dir = pathlib.Path(sys.argv[1])
manifest_path = compliance_dir / "sbom-baseline-manifest.json"
required_documents = {
    "tunnel-client.spdx.json",
    "tunnel-client-runtime.spdx.json",
    "tunnel-client-runtime-cloudflared.spdx.json",
}


def fail(message):
    raise SystemExit(message)


def sha256(path):
    value = hashlib.sha256()
    with path.open("rb") as source:
        for chunk in iter(lambda: source.read(1024 * 1024), b""):
            value.update(chunk)
    return value.hexdigest()


if not manifest_path.is_file() or manifest_path.is_symlink():
    fail("SBOM baseline manifest is missing or is not a regular file")
try:
    with manifest_path.open(encoding="utf-8") as source:
        manifest = json.load(source)
except (OSError, UnicodeDecodeError, json.JSONDecodeError) as exc:
    fail(f"SBOM baseline manifest is not valid JSON: {exc}")

if not isinstance(manifest, dict) or manifest.get("schemaVersion") != 1:
    fail("SBOM baseline manifest schema version mismatch")
documents = manifest.get("documents")
if not isinstance(documents, dict):
    fail("SBOM baseline manifest has no documents")
if not all(isinstance(name, str) for name in documents):
    fail("SBOM baseline manifest has a non-string document name")

document_names = set(documents)
if not required_documents <= document_names:
    missing = ", ".join(sorted(required_documents - document_names))
    fail(f"SBOM baseline manifest is missing required documents: {missing}")

checked_in_names = {path.name for path in compliance_dir.glob("*.spdx.json")}
if document_names != checked_in_names:
    missing = sorted(checked_in_names - document_names)
    unexpected = sorted(document_names - checked_in_names)
    fail(
        "SBOM baseline manifest document coverage mismatch: "
        f"missing={missing}, unexpected={unexpected}"
    )

for name in sorted(document_names):
    if pathlib.PurePosixPath(name).name != name or pathlib.PureWindowsPath(name).name != name:
        fail(f"SBOM baseline manifest has an unsafe document name: {name}")
    entry = documents[name]
    expected_sha = entry.get("sha256") if isinstance(entry, dict) else None
    if not isinstance(expected_sha, str) or not re.fullmatch(r"[0-9a-f]{64}", expected_sha):
        fail(f"SBOM baseline manifest has no valid SHA256 for {name}")
    document_path = compliance_dir / name
    if not document_path.is_file() or document_path.is_symlink():
        fail(f"SBOM baseline document is missing or is not a regular file: {name}")
    if sha256(document_path) != expected_sha:
        fail(f"SBOM baseline manifest checksum mismatch for {name}")
    try:
        with document_path.open(encoding="utf-8") as source:
            document = json.load(source)
    except (OSError, UnicodeDecodeError, json.JSONDecodeError) as exc:
        fail(f"SBOM baseline document is not valid JSON: {name}: {exc}")
    if not isinstance(document, dict) or document.get("spdxVersion") != "SPDX-2.3":
        fail(f"SBOM baseline document is not SPDX 2.3: {name}")

print(f"SBOM baseline manifest and {len(document_names)} SPDX documents: passed")
PY
