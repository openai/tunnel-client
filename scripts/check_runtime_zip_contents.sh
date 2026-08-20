#!/usr/bin/env bash
set -euo pipefail

usage() {
  cat <<'EOF'
Usage:
  ./scripts/check_runtime_zip_contents.sh \
    --flavor runtime|runtime-cloudflared \
    --archive <path-to-zip>

Checks the release ZIP allowlist for a runtime flavor.
EOF
}

die() {
  echo "check_runtime_zip_contents.sh: $*" >&2
  exit 1
}

flavor=""
archive=""
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
  runtime|runtime-cloudflared) ;;
  *) die "--flavor must be runtime or runtime-cloudflared" ;;
esac
[[ -n "${archive}" ]] || die "--archive is required"
[[ -f "${archive}" ]] || die "archive does not exist: ${archive}"
command -v python3 >/dev/null 2>&1 || die "python3 is required"

python3 - "${flavor}" "${archive}" <<'PY'
import pathlib
import re
import sys
import zipfile

flavor, archive_arg = sys.argv[1:]
archive = pathlib.Path(archive_arg)
binary = "tunnel-client-runtime"
if flavor == "runtime-cloudflared":
    binary = "tunnel-client-runtime-cloudflared"

with zipfile.ZipFile(archive) as zipped:
    names = sorted(info.filename for info in zipped.infolist())

if any(name.startswith("/") or ".." in pathlib.PurePosixPath(name).parts for name in names):
    raise SystemExit("archive contains an unsafe path")
if any(name.endswith("/") for name in names):
    raise SystemExit("archive must not contain directory entries")
if any(pathlib.PurePosixPath(name).name != name for name in names):
    raise SystemExit("archive members must be at the ZIP root")
if len(names) != len(set(names)):
    raise SystemExit("archive contains duplicate members")


def exact_one(candidates: list[str], label: str) -> str:
    if len(candidates) != 1:
        raise SystemExit(f"archive must contain exactly one {label}: {candidates}")
    return candidates[0]


runtime_binary = exact_one(
    [name for name in (binary, binary + ".exe") if name in names],
    "runtime binary",
)
license_report = exact_one(
    [
        name
        for name in names
        if re.fullmatch(rf"{re.escape(binary)}-.+-licenses\.txt", name)
    ],
    "matching platform license report",
)
sbom_sidecar = exact_one(
    [
        name
        for name in names
        if re.fullmatch(rf"{re.escape(binary)}-.+\.spdx\.json", name)
    ],
    "matching SPDX sidecar",
)
license_stem = license_report[: -len("-licenses.txt")]
sbom_stem = sbom_sidecar[: -len(".spdx.json")]
if license_stem != sbom_stem:
    raise SystemExit(
        "archive license report and SPDX sidecar must use the same release stem"
    )
if flavor == "runtime" and sbom_stem.startswith("tunnel-client-runtime-cloudflared-"):
    raise SystemExit("archive sidecars belong to the runtime-cloudflared flavor")

expected = {"LICENSE", "NOTICE", runtime_binary, license_report, sbom_sidecar}
if flavor == "runtime-cloudflared":
    companion = exact_one(
        [name for name in ("cloudflared", "cloudflared.exe") if name in names],
        "cloudflared companion binary",
    )
    expected_companion = "cloudflared.exe" if runtime_binary.endswith(".exe") else "cloudflared"
    if companion != expected_companion:
        raise SystemExit(
            "archive companion extension must match the runtime binary extension"
        )
    expected.update({companion, "cloudflared-manifest.json"})

actual = set(names)
if actual != expected:
    missing = sorted(expected - actual)
    unexpected = sorted(actual - expected)
    raise SystemExit(
        "archive differs from the runtime ZIP allowlist: "
        f"missing={missing}, unexpected={unexpected}"
    )

print(f"{flavor} ZIP contents: passed")
PY
