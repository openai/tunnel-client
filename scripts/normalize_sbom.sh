#!/usr/bin/env bash
set -euo pipefail

usage() {
  cat <<'EOF'
Usage:
  ./scripts/normalize_sbom.sh --input <spdx-json> --output <normalized-json>

Normalizes stable baseline documents while preserving dependency identities,
licenses, checksums, relationships, filenames, and tool identity. The
first-party module is canonicalized to its public repository identity so
baseline bytes stay stable when the repository path changes. Set
SBOM_NORMALIZE_ROOT to the temporary staging root whose prefix should be
removed from recorded paths.
EOF
}

die() {
  echo "normalize_sbom.sh: $*" >&2
  exit 1
}

input=""
output=""
while [[ $# -gt 0 ]]; do
  case "$1" in
    --input)
      input="${2:-}"
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

[[ -n "${input}" ]] || die "--input is required"
[[ -f "${input}" ]] || die "input does not exist: ${input}"
[[ -n "${output}" ]] || die "--output is required"
command -v python3 >/dev/null 2>&1 || die "python3 is required"

output_parent="$(dirname "${output}")"
mkdir -p "${output_parent}"
output_parent="$(cd "${output_parent}" && pwd)"
output="${output_parent}/$(basename "${output}")"
tmp_output="$(mktemp "${output_parent}/.$(basename "${output}").tmp.XXXXXX")"
trap 'rm -f "${tmp_output}"' EXIT

python3 - "${input}" "${tmp_output}" "${SBOM_NORMALIZE_ROOT:-}" <<'PY'
import hashlib
import json
import pathlib
import sys

input_path = pathlib.Path(sys.argv[1])
output_path = pathlib.Path(sys.argv[2])
staging_root = sys.argv[3].rstrip("/\\")
fixed_created = "1970-01-01T00:00:00Z"
namespace_prefix = "https://github.com/openai/tunnel-client/sbom/baseline/"
canonical_module_path = "github.com/openai/tunnel-client"
# Keep the legacy alias split so text-level source rewrites preserve it.
legacy_module_path = "go.openai.org/api/" + "tunnel-client"
module_path_aliases = (
    legacy_module_path,
    canonical_module_path,
)


def normalize_string(value: str) -> str:
    for module_path in module_path_aliases:
        value = value.replace(module_path, canonical_module_path)
    if not staging_root:
        return value
    normalized_root = staging_root.replace("\\", "/")
    variants = []
    for root in (staging_root, normalized_root):
        if root and root not in variants:
            variants.append(root)
    for root in variants:
        value = value.replace("file://" + root + "/", "file://")
        value = value.replace(root + "/", "")
        value = value.replace(root, ".")
    return value


def canonical(value: object) -> str:
    return json.dumps(
        value,
        ensure_ascii=False,
        separators=(",", ":"),
        sort_keys=True,
    )


def normalize(value: object) -> object:
    if isinstance(value, dict):
        normalized = {key: normalize(item) for key, item in value.items()}
        creation_info = normalized.get("creationInfo")
        if isinstance(creation_info, dict) and "created" in creation_info:
            creation_info["created"] = fixed_created
        return {key: normalized[key] for key in sorted(normalized)}
    if isinstance(value, list):
        normalized = [normalize(item) for item in value]
        return sorted(normalized, key=canonical)
    if isinstance(value, str):
        return normalize_string(value)
    return value


with input_path.open(encoding="utf-8") as source:
    document = json.load(source)

if document.get("spdxVersion") != "SPDX-2.3":
    raise SystemExit("input document is not SPDX 2.3 JSON")

document = normalize(document)
document["documentNamespace"] = namespace_prefix + "pending"
document = normalize(document)
digest = hashlib.sha256(canonical(document).encode("utf-8")).hexdigest()
document["documentNamespace"] = namespace_prefix + digest
document = normalize(document)

with output_path.open("w", encoding="utf-8", newline="\n") as destination:
    json.dump(document, destination, ensure_ascii=False, indent=2, sort_keys=True)
    destination.write("\n")
PY

mv "${tmp_output}" "${output}"
trap - EXIT
printf 'wrote %s\n' "${output}"
