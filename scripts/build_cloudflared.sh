#!/usr/bin/env bash
set -euo pipefail

readonly SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
readonly DEFAULT_MANIFEST="${SCRIPT_DIR}/../pkg/cloudflared/manifest.json"

usage() {
  cat <<'EOF'
Usage:
  GOPROXY=<proxy-only-module-proxy> ./scripts/build_cloudflared.sh \
    --goos <linux|darwin|windows> \
    --goarch <amd64|arm64> \
    --output <path> \
    [--manifest <path>]

Use --describe instead of --output to print the pinned version, Go module,
module checksum, package, and acquisition kind without fetching or building.

The build path uses Go module tooling only. GOPROXY must be explicit and must
not be direct, off, or contain a direct fallback.
EOF
}

die() {
  echo "build_cloudflared.sh: $*" >&2
  exit 1
}

require_proxy_only_goproxy() {
  local proxy="${GOPROXY:-}"
  [[ -n "${proxy}" ]] || die "GOPROXY is required and must name a module proxy without a direct fallback"

  local normalized="${proxy//|/,}"
  local entry
  local entries=()
  IFS=',' read -r -a entries <<< "${normalized}"
  for entry in "${entries[@]}"; do
    [[ -n "${entry}" ]] || die "GOPROXY contains an empty proxy entry"
    case "${entry}" in
      direct|off)
        die "GOPROXY must not use ${entry}; direct VCS acquisition is disabled"
        ;;
    esac
  done
}

goos=""
goarch=""
output=""
manifest="${DEFAULT_MANIFEST}"
describe=false

while [[ $# -gt 0 ]]; do
  case "$1" in
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
    --manifest)
      manifest="${2:-}"
      shift 2
      ;;
    --describe)
      describe=true
      shift
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

[[ -n "${goos}" ]] || die "--goos is required"
[[ -n "${goarch}" ]] || die "--goarch is required"
[[ -f "${manifest}" ]] || die "manifest does not exist: ${manifest}"
if [[ "${describe}" != "true" ]]; then
  [[ -n "${output}" ]] || die "--output is required"
fi

platform="${goos}/${goarch}"
if ! IFS=$'\t' read -r version module_path package_path module_version module_sum go_mod_sum build_time < <(
  python3 -c '
import json
import sys

manifest_path, platform = sys.argv[1:3]
with open(manifest_path, encoding="utf-8") as f:
    manifest = json.load(f)

required = (
    "version",
    "module_path",
    "package_path",
    "module_version",
    "module_sum",
    "go_mod_sum",
    "build_time",
)
missing = [field for field in required if not manifest.get(field)]
if missing:
    print("manifest is missing required fields: " + ", ".join(missing), file=sys.stderr)
    sys.exit(2)
if platform not in manifest.get("platforms", []):
    sys.exit(3)

print("\t".join(str(manifest[field]) for field in required))
' "${manifest}" "${platform}"
); then
  die "invalid manifest or unsupported cloudflared platform: ${platform}"
fi

if [[ "${describe}" == "true" ]]; then
  printf '%s\t%s@%s\t%s\t%s\t%s\n' \
    "${version}" \
    "${module_path}" \
    "${module_version}" \
    "${module_sum}" \
    "${package_path}" \
    "go-module-proxy"
  exit 0
fi

package_relative_path="${package_path#"${module_path}/"}"
[[ -n "${package_relative_path}" && "${package_relative_path}" != "${package_path}" ]] ||
  die "package_path must be inside module_path"
package_relative_path="./${package_relative_path}"

command -v go >/dev/null 2>&1 || die "go is required"
require_proxy_only_goproxy

tmp_dir="$(mktemp -d)"
trap 'rm -rf "${tmp_dir}"' EXIT
module_json="${tmp_dir}/module.json"

if ! env \
  GOPROXY="${GOPROXY}" \
  GONOPROXY=none \
  go mod download -json "${module_path}@${module_version}" > "${module_json}"; then
  die "failed to acquire ${module_path}@${module_version} through GOPROXY"
fi

if ! IFS=$'\t' read -r actual_module_path actual_module_version module_dir actual_module_sum actual_go_mod_sum < <(
  python3 -c '
import json
import sys

with open(sys.argv[1], encoding="utf-8") as f:
    module = json.load(f)
if module.get("Error"):
    print(module["Error"], file=sys.stderr)
    sys.exit(2)
required = ("Path", "Version", "Dir", "Sum", "GoModSum")
missing = [field for field in required if not module.get(field)]
if missing:
    print("go mod download did not return: " + ", ".join(missing), file=sys.stderr)
    sys.exit(3)
print("\t".join(module[field] for field in required))
' "${module_json}"
); then
  die "could not inspect downloaded Go module metadata"
fi

[[ "${actual_module_sum}" == "${module_sum}" ]] ||
  die "Go module checksum mismatch: got ${actual_module_sum}, want ${module_sum}"
[[ "${actual_go_mod_sum}" == "${go_mod_sum}" ]] ||
  die "Go module go.mod checksum mismatch: got ${actual_go_mod_sum}, want ${go_mod_sum}"
[[ "${actual_module_path}" == "${module_path}" ]] ||
  die "Go module path mismatch: got ${actual_module_path}, want ${module_path}"
[[ "${actual_module_version}" == "${module_version}" ]] ||
  die "Go module version mismatch: got ${actual_module_version}, want ${module_version}"
[[ -d "${module_dir}" ]] || die "downloaded Go module directory does not exist: ${module_dir}"

output_dir="$(dirname "${output}")"
mkdir -p "${output_dir}"
output="$(cd "${output_dir}" && pwd)/$(basename "${output}")"
built_path="${tmp_dir}/cloudflared"

(
  cd "${module_dir}"
  env \
    GOPROXY="${GOPROXY}" \
    GONOPROXY=none \
    GOOS="${goos}" \
    GOARCH="${goarch}" \
    CGO_ENABLED=0 \
    go build \
      -mod=readonly \
      -trimpath \
      -buildvcs=false \
      -ldflags "-X main.Version=${version} -X main.BuildTime=${build_time}" \
      -o "${built_path}" \
      "${package_relative_path}"
)

cp "${built_path}" "${output}"
if [[ "${goos}" != "windows" ]]; then
  chmod 0755 "${output}"
fi

printf 'built cloudflared %s for %s from %s@%s through GOPROXY to %s\n' \
  "${version}" \
  "${platform}" \
  "${module_path}" \
  "${module_version}" \
  "${output}"
