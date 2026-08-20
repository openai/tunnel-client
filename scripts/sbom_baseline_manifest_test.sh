#!/usr/bin/env bash
set -euo pipefail

resolved_script_path="$(python3 - "${BASH_SOURCE[0]}" <<'PY'
import os
import sys

print(os.path.realpath(sys.argv[1]))
PY
)"
script_dir="$(cd "$(dirname "${resolved_script_path}")" && pwd)"
if [[ -x "${script_dir}/scripts/verify_sbom_baselines.sh" ]]; then
  script_dir="${script_dir}/scripts"
fi

tmp_dir="$(mktemp -d "${TMPDIR:-/tmp}/tunnel-client-sbom-baseline-test.XXXXXX")"
trap 'rm -rf "${tmp_dir}"' EXIT

python3 - "${tmp_dir}" <<'PY'
import hashlib
import json
import pathlib
import shutil
import sys

root = pathlib.Path(sys.argv[1])
names = (
    "tunnel-client.spdx.json",
    "tunnel-client-runtime.spdx.json",
    "tunnel-client-runtime-cloudflared.spdx.json",
)


def digest(data):
    return hashlib.sha256(data).hexdigest()


def write_fixture(directory_name, mutation=None):
    directory = root / directory_name
    directory.mkdir()
    documents = {}
    for name in names:
        version = "SPDX-2.2" if mutation == "invalid-spdx" and name == names[0] else "SPDX-2.3"
        contents = (
            json.dumps({"spdxVersion": version, "name": name}, sort_keys=True) + "\n"
        ).encode()
        (directory / name).write_bytes(contents)
        documents[name] = {"sha256": digest(contents)}
    if mutation == "missing-entry":
        documents.pop(names[-1])
    manifest = {
        "schemaVersion": 1,
        "buildMetadata": {
            "semanticVersion": "0.0.0-baseline",
            "gitSHA": "baseline",
        },
        "documents": documents,
    }
    (directory / "sbom-baseline-manifest.json").write_text(
        json.dumps(manifest, indent=2, sort_keys=True) + "\n"
    )
    if mutation == "tampered-document":
        (directory / names[0]).write_text('{"spdxVersion":"SPDX-2.3","name":"tampered"}\n')
    if mutation == "untracked-document":
        shutil.copyfile(directory / names[0], directory / "extra.spdx.json")


write_fixture("valid")
write_fixture("tampered", "tampered-document")
write_fixture("invalid-spdx", "invalid-spdx")
write_fixture("missing-entry", "missing-entry")
write_fixture("untracked-document", "untracked-document")
PY

run_fixture() {
  "${script_dir}/verify_sbom_baselines.sh" \
    --compliance-dir "${tmp_dir}/$1"
}

assert_rejected() {
  local directory="$1"
  local expected_message="$2"
  if run_fixture "${directory}" >"${tmp_dir}/${directory}.out" 2>&1; then
    echo "expected invalid baseline fixture to fail: ${directory}" >&2
    exit 1
  fi
  grep -F "${expected_message}" "${tmp_dir}/${directory}.out" >/dev/null || {
    echo "invalid baseline fixture failed for the wrong reason: ${directory}" >&2
    cat "${tmp_dir}/${directory}.out" >&2
    exit 1
  }
}

run_fixture valid
assert_rejected tampered "checksum mismatch"
assert_rejected invalid-spdx "is not SPDX 2.3"
assert_rejected missing-entry "missing required documents"
assert_rejected untracked-document "document coverage mismatch"
