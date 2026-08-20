#!/usr/bin/env bash
set -euo pipefail

flavor="${1:-}"
case "${flavor}" in
  runtime|runtime-cloudflared) ;;
  *)
    echo "usage: runtime_zip_contents_test.sh runtime|runtime-cloudflared" >&2
    exit 1
    ;;
esac

resolved_script_path="$(python3 - "${BASH_SOURCE[0]}" <<'PY'
import os
import sys

print(os.path.realpath(sys.argv[1]))
PY
)"
script_dir="$(cd "$(dirname "${resolved_script_path}")" && pwd)"
if [[ -x "${script_dir}/scripts/check_runtime_zip_contents.sh" ]]; then
  script_dir="${script_dir}/scripts"
fi

tmp_dir="$(mktemp -d "${TMPDIR:-/tmp}/tunnel-client-runtime-zip-test.XXXXXX")"
trap 'rm -rf "${tmp_dir}"' EXIT
archive="${tmp_dir}/artifact.zip"

python3 - "${flavor}" "${archive}" <<'PY'
import pathlib
import sys
import zipfile

flavor, archive_arg = sys.argv[1:]
binary = "tunnel-client-runtime"
if flavor == "runtime-cloudflared":
    binary = "tunnel-client-runtime-cloudflared"

members = {
    "LICENSE": "license\n",
    "NOTICE": "notice\n",
    binary: "binary\n",
    f"{binary}-v0.0.0-linux-amd64-licenses.txt": "licenses\n",
    f"{binary}-v0.0.0-linux-amd64.spdx.json": "{}\n",
}
if flavor == "runtime-cloudflared":
    members["cloudflared"] = "companion\n"
    members["cloudflared-manifest.json"] = "{}\n"

with zipfile.ZipFile(pathlib.Path(archive_arg), "w") as archive:
    for name, contents in sorted(members.items()):
        archive.writestr(name, contents)
PY

"${script_dir}/check_runtime_zip_contents.sh" \
  --flavor "${flavor}" \
  --archive "${archive}"

assert_rejected() {
  local fixture="$1"
  local expected_message="$2"
  if "${script_dir}/check_runtime_zip_contents.sh" \
    --flavor "${flavor}" \
    --archive "${fixture}" >"${tmp_dir}/rejected.out" 2>&1; then
    echo "expected invalid ZIP fixture to fail: ${fixture}" >&2
    exit 1
  fi
  grep -F "${expected_message}" "${tmp_dir}/rejected.out" >/dev/null || {
    echo "invalid ZIP fixture failed for the wrong reason: ${fixture}" >&2
    cat "${tmp_dir}/rejected.out" >&2
    exit 1
  }
}

python3 - "${flavor}" "${tmp_dir}" <<'PY'
import pathlib
import sys
import warnings
import zipfile

flavor, tmp_arg = sys.argv[1:]
tmp_dir = pathlib.Path(tmp_arg)
binary = "tunnel-client-runtime"
if flavor == "runtime-cloudflared":
    binary = "tunnel-client-runtime-cloudflared"

base = {
    "LICENSE": "license\n",
    "NOTICE": "notice\n",
    binary: "binary\n",
    f"{binary}-v0.0.0-linux-amd64-licenses.txt": "licenses\n",
    f"{binary}-v0.0.0-linux-amd64.spdx.json": "{}\n",
}
if flavor == "runtime-cloudflared":
    base["cloudflared"] = "companion\n"
    base["cloudflared-manifest.json"] = "{}\n"


def write(name: str, members: dict[str, str], duplicates: list[tuple[str, str]] = []):
    with zipfile.ZipFile(tmp_dir / name, "w") as archive:
        for member, contents in sorted(members.items()):
            archive.writestr(member, contents)
        with warnings.catch_warnings():
            warnings.simplefilter("ignore", UserWarning)
            for member, contents in duplicates:
                archive.writestr(member, contents)


both_binaries = dict(base)
both_binaries[binary + ".exe"] = "binary\n"
write("both-binaries.zip", both_binaries)

two_licenses = dict(base)
two_licenses[f"{binary}-v0.0.0-linux-arm64-licenses.txt"] = "licenses\n"
write("two-licenses.zip", two_licenses)

mismatched_sbom = dict(base)
del mismatched_sbom[f"{binary}-v0.0.0-linux-amd64.spdx.json"]
mismatched_sbom[f"{binary}-v0.0.0-linux-arm64.spdx.json"] = "{}\n"
write("mismatched-sidecars.zip", mismatched_sbom)

write("duplicate-license.zip", base, [(f"{binary}-v0.0.0-linux-amd64-licenses.txt", "again\n")])

if flavor == "runtime-cloudflared":
    missing_manifest = dict(base)
    del missing_manifest["cloudflared-manifest.json"]
    write("missing-manifest.zip", missing_manifest)

    both_companions = dict(base)
    both_companions["cloudflared.exe"] = "companion\n"
    write("both-companions.zip", both_companions)
PY

assert_rejected "${tmp_dir}/both-binaries.zip" "exactly one runtime binary"
assert_rejected "${tmp_dir}/two-licenses.zip" "exactly one matching platform license report"
assert_rejected "${tmp_dir}/mismatched-sidecars.zip" "same release stem"
assert_rejected "${tmp_dir}/duplicate-license.zip" "duplicate members"
if [[ "${flavor}" == "runtime-cloudflared" ]]; then
  assert_rejected "${tmp_dir}/missing-manifest.zip" "runtime ZIP allowlist"
  assert_rejected "${tmp_dir}/both-companions.zip" "exactly one cloudflared companion binary"
fi
