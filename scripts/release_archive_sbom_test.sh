#!/usr/bin/env bash
set -euo pipefail

resolved_script_path="$(python3 - "${BASH_SOURCE[0]}" <<'PY'
import os
import sys

print(os.path.realpath(sys.argv[1]))
PY
)"
script_dir="$(cd "$(dirname "${resolved_script_path}")" && pwd)"
if [[ -x "${script_dir}/scripts/verify_release_archive.sh" ]]; then
  script_dir="${script_dir}/scripts"
fi

tmp_dir="$(mktemp -d "${TMPDIR:-/tmp}/tunnel-client-release-sbom-test.XXXXXX")"
trap 'rm -rf "${tmp_dir}"' EXIT

python3 - "${tmp_dir}" <<'PY'
import hashlib
import json
import pathlib
import stat
import sys
import warnings
import zipfile

root = pathlib.Path(sys.argv[1])


def digest(data: bytes) -> str:
    return hashlib.sha256(data).hexdigest()


def fixture(flavor: str, directory_name: str, mutation=None) -> None:
    expected = {
        "client": "tunnel-client",
        "runtime": "tunnel-client-runtime",
        "runtime-cloudflared": "tunnel-client-runtime-cloudflared",
    }[flavor]
    directory = root / directory_name
    directory.mkdir()
    stem = f"{expected}-v0.0.0-linux-amd64"
    archive_name = f"{stem}.zip"
    sbom_name = f"{stem}.spdx.json"
    license_name = f"{stem}-licenses.txt"
    payload = {
        "LICENSE": b"license\n",
        "NOTICE": b"notice\n",
        expected: b"binary\n",
        license_name: b"licenses\n",
    }
    if flavor in {"client", "runtime-cloudflared"}:
        payload["cloudflared"] = b"cloudflared\n"
        payload["cloudflared-manifest.json"] = b'{"version":"test"}\n'

    files = []
    described = []
    for index, (name, contents) in enumerate(sorted(payload.items())):
        spdx_id = f"SPDXRef-File-{index}"
        described.append(spdx_id)
        files.append(
            {
                "SPDXID": spdx_id,
                "fileName": name,
                "checksums": [
                    {"algorithm": "SHA256", "checksumValue": digest(contents)}
                ],
            }
        )
    sbom = {
        "spdxVersion": "SPDX-2.3",
        "name": expected,
        "files": files,
        "documentDescribes": described,
    }
    sbom_bytes = (json.dumps(sbom, indent=2, sort_keys=True) + "\n").encode()
    (directory / sbom_name).write_bytes(sbom_bytes)

    zip_payload = dict(payload)
    if mutation == "tampered-binary":
        zip_payload[expected] = b"tampered\n"
    with zipfile.ZipFile(directory / archive_name, "w") as archive:
        for name, contents in sorted(zip_payload.items()):
            archive.writestr(name, contents)
        if mutation != "missing-sidecar":
            embedded_sbom = sbom_bytes
            if mutation == "embedded-mismatch":
                embedded_sbom += b" "
            archive.writestr(sbom_name, embedded_sbom)
        if mutation == "unsafe-path":
            archive.writestr("../escape", b"escape\n")
        if mutation == "unsafe-windows-path":
            archive.writestr(r"C:\escape", b"escape\n")
        if mutation == "duplicate-member":
            with warnings.catch_warnings():
                warnings.simplefilter("ignore", UserWarning)
                archive.writestr("LICENSE", b"duplicate\n")
        if mutation == "symlink":
            link = zipfile.ZipInfo("link")
            link.create_system = 3
            link.external_attr = (stat.S_IFLNK | 0o777) << 16
            archive.writestr(link, b"LICENSE")

    checksums = []
    for name in (archive_name, sbom_name):
        checksums.append(f"{digest((directory / name).read_bytes())}  {name}")
    if mutation == "bad-checksum":
        checksums[0] = "0" * 64 + checksums[0][64:]
    if mutation == "duplicate-checksum":
        checksums.append(checksums[0])
    (directory / "SHA256SUMS.txt").write_text("\n".join(checksums) + "\n")


for flavor in ("client", "runtime", "runtime-cloudflared"):
    fixture(flavor, flavor)
fixture("runtime", "tampered", "tampered-binary")
fixture("runtime", "missing-sidecar", "missing-sidecar")
fixture("runtime", "unsafe-path", "unsafe-path")
fixture("runtime", "unsafe-windows-path", "unsafe-windows-path")
fixture("runtime", "duplicate-member", "duplicate-member")
fixture("runtime", "embedded-mismatch", "embedded-mismatch")
fixture("runtime", "bad-checksum", "bad-checksum")
fixture("runtime", "duplicate-checksum", "duplicate-checksum")
fixture("runtime", "symlink", "symlink")
PY

run_fixture() {
  local flavor="$1"
  local directory="$2"
  local stem="tunnel-client"
  [[ "${flavor}" == "runtime" ]] && stem="tunnel-client-runtime"
  [[ "${flavor}" == "runtime-cloudflared" ]] && stem="tunnel-client-runtime-cloudflared"
  stem="${stem}-v0.0.0-linux-amd64"
  "${script_dir}/verify_release_archive.sh" \
    --flavor "${flavor}" \
    --archive "${tmp_dir}/${directory}/${stem}.zip" \
    --sbom "${tmp_dir}/${directory}/${stem}.spdx.json" \
    --checksums "${tmp_dir}/${directory}/SHA256SUMS.txt"
}

assert_rejected() {
  local directory="$1"
  local expected_message="$2"
  if run_fixture runtime "${directory}" >"${tmp_dir}/${directory}.out" 2>&1; then
    echo "expected invalid release fixture to fail: ${directory}" >&2
    exit 1
  fi
  grep -F "${expected_message}" "${tmp_dir}/${directory}.out" >/dev/null || {
    echo "invalid release fixture failed for the wrong reason: ${directory}" >&2
    cat "${tmp_dir}/${directory}.out" >&2
    exit 1
  }
}

run_fixture client client
run_fixture runtime runtime
run_fixture runtime-cloudflared runtime-cloudflared
assert_rejected tampered "SBOM SHA256 mismatch for staged file"
assert_rejected missing-sidecar "exactly one embedded SBOM sidecar"
assert_rejected unsafe-path "archive contains an unsafe path"
assert_rejected unsafe-windows-path "archive contains an unsafe path"
assert_rejected duplicate-member "archive contains duplicate members"
assert_rejected embedded-mismatch "embedded SBOM sidecar differs"
assert_rejected bad-checksum "SHA256 mismatch"
assert_rejected duplicate-checksum "exactly one SHA256 entry"
assert_rejected symlink "archive must not contain symlinks"
