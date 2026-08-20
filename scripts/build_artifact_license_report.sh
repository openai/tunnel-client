#!/usr/bin/env bash
set -euo pipefail

readonly SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
readonly PROJECT_ROOT="$(cd "${SCRIPT_DIR}/.." && pwd)"
readonly COMPLIANCE_DIR="${PROJECT_ROOT}/compliance"

usage() {
  cat <<'EOF'
Usage:
  ./scripts/build_artifact_license_report.sh \
    --flavor client|runtime|runtime-cloudflared \
    --goos <goos> \
    --goarch <goarch> \
    [--python <absolute-path>] \
    [--base-report <path>] \
    [--companion-report <path>] \
    --output <path>

Builds the deterministic, artifact-scoped license sidecar used in a release
payload. The normal runtime has one Go-entrypoint report. The full client and
runtime-cloudflared payloads also ship the pinned cloudflared companion, so
their sidecar is the union of the corresponding Go-entrypoint report and the
separately generated pinned-companion report, filtered to the artifact's exact
GOOS/GOARCH tuple.
EOF
}

die() {
  echo "build_artifact_license_report.sh: $*" >&2
  exit 1
}

flavor=""
goos=""
goarch=""
output=""
python_bin=""
base_report=""
companion_report=""
while [[ $# -gt 0 ]]; do
  case "$1" in
    --flavor)
      flavor="${2:-}"
      shift 2
      ;;
    --goos)
      goos="${2:-}"
      shift 2
      ;;
    --goarch)
      goarch="${2:-}"
      shift 2
      ;;
    --output)
      output="${2:-}"
      shift 2
      ;;
    --python)
      python_bin="${2:-}"
      shift 2
      ;;
    --base-report)
      base_report="${2:-}"
      shift 2
      ;;
    --companion-report)
      companion_report="${2:-}"
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
[[ -n "${goos}" ]] || die "--goos is required"
[[ -n "${goarch}" ]] || die "--goarch is required"
[[ "${goos}" =~ ^[a-z0-9_]+$ ]] || die "--goos must be a lowercase Go OS value"
[[ "${goarch}" =~ ^[a-z0-9_]+$ ]] || die "--goarch must be a lowercase Go architecture value"
[[ -n "${output}" ]] || die "--output is required"
if [[ -z "${python_bin}" ]]; then
  python_bin="$(command -v python3)" || die "python3 is required"
fi
[[ "${python_bin}" == /* && -x "${python_bin}" ]] ||
  die "--python must be an absolute executable"
platform="${goos}/${goarch}"

if [[ -z "${base_report}" ]]; then
  base_report="${COMPLIANCE_DIR}/oss-license-report-${flavor}.txt"
fi
[[ -f "${base_report}" ]] || die "missing flavor report: ${base_report}"
reports=("${base_report}")
if [[ -z "${companion_report}" &&
  ( "${flavor}" == "client" || "${flavor}" == "runtime-cloudflared" ) ]]; then
  companion_report="${COMPLIANCE_DIR}/oss-license-report-cloudflared-binary.txt"
fi
if [[ -n "${companion_report}" ]]; then
  [[ -f "${companion_report}" ]] || die "missing companion report: ${companion_report}"
  reports+=("${companion_report}")
fi

output_parent="$(dirname "${output}")"
mkdir -p "${output_parent}"
output_parent="$(cd "${output_parent}" && pwd)"
output="${output_parent}/$(basename "${output}")"
tmp_output="$(mktemp "${output_parent}/.$(basename "${output}").tmp.XXXXXX")"
trap 'rm -f "${tmp_output}"' EXIT

"${python_bin}" - "${flavor}" "${platform}" "${tmp_output}" "${reports[@]}" <<'PY'
import collections
import pathlib
import sys

flavor = sys.argv[1]
platform = sys.argv[2]
output_path = pathlib.Path(sys.argv[3])
report_paths = [pathlib.Path(path) for path in sys.argv[4:]]
header = "DEPENDENCY|TYPE|VERSION|LICENSES|LICENSE_FILE|PLATFORMS"
rows: dict[tuple[str, str, str, str, str], set[str]] = collections.defaultdict(set)

for report_path in report_paths:
    in_table = False
    for line in report_path.read_text(encoding="utf-8").splitlines():
        if line == header:
            in_table = True
            continue
        if not in_table or not line:
            continue
        fields = line.split("|")
        if len(fields) != 6:
            raise SystemExit(f"malformed license row in {report_path}: {line!r}")
        dependency, dependency_type, version, licenses, license_file, platforms = fields
        row_platforms = {candidate for candidate in platforms.split(",") if candidate}
        if platform not in row_platforms:
            continue
        rows[(dependency, dependency_type, version, licenses, license_file)].add(
            platform
        )

if not rows:
    raise SystemExit(f"artifact license report has no dependency rows for {platform}")

licenses = sorted(
    {
        license_name.strip()
        for (_, _, _, license_names, _), _platforms in rows.items()
        for license_name in license_names.split(",")
        if license_name.strip()
    }
)

with output_path.open("w", encoding="utf-8", newline="\n") as output:
    output.write("License report for tunnel-client release artifact flavor: " + flavor + "\n")
    output.write("Platforms: " + platform + "\n")
    output.write("Tests included: false\n")
    output.write(
        "License summary (all licenses currently in use; additions require legal approval and #release-approval sign-off):\n"
    )
    for license_name in licenses:
        output.write("  " + license_name + "\n")
    output.write("\n")
    output.write(header + "\n")
    for key in sorted(rows):
        platforms = ",".join(sorted(rows[key]))
        output.write("|".join((*key, platforms)) + "\n")
PY

mv "${tmp_output}" "${output}"
trap - EXIT
printf 'wrote artifact license report: %s\n' "${output}"
