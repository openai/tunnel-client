#!/usr/bin/env bash
set -euo pipefail

# Build the image companion from the checksum-pinned, vendored source archive.
# This is an action helper: every input path is passed by the declaring Bazel
# rule, and no network or checkout fallback is allowed.

readonly FIXED_SOURCE_DATE_EPOCH="0"
readonly -a AMBIENT_GO_BUILD_ENV_VARS=(
  CGO_ENABLED
  GO111MODULE
  GO386
  GOAMD64
  GOARCH
  GOARM
  GOARM64
  GOCACHEPROG
  GOEXPERIMENT
  GOFIPS140
  GOFLAGS
  GOMIPS
  GOMIPS64
  GOROOT
  GOOS
  GOPPC64
  GORISCV64
  GOWASM
  GO_EXTLINK_ENABLED
)

usage() {
  cat <<'EOF'
Usage:
  build_image_cloudflared.sh \
    --archive <declared-pinned-source-archive> \
    --manifest <declared-cloudflared-manifest> \
    --go <declared-go-tool> \
    --python <declared-python-tool> \
    --extractor <declared-archive-extractor> \
    --goarch amd64|arm64 \
    --output <declared-output>

Builds one Linux cloudflared binary from the pinned source archive's checked-
in vendor tree. The action is offline, fixed-timestamp, and target-arch-only.
EOF
}

die() {
  echo "build_image_cloudflared.sh: $*" >&2
  exit 1
}

absolute_path() {
  case "$1" in
    /*) printf '%s\n' "$1" ;;
    *) printf '%s/%s\n' "${PWD}" "$1" ;;
  esac
}

clear_ambient_go_build_environment() {
  unset "${AMBIENT_GO_BUILD_ENV_VARS[@]}"
}

archive=""
manifest=""
go_bin=""
python_bin=""
extractor=""
goarch=""
output=""
while [[ $# -gt 0 ]]; do
  case "$1" in
    --archive)
      archive="${2:-}"
      shift 2
      ;;
    --manifest)
      manifest="${2:-}"
      shift 2
      ;;
    --go)
      go_bin="${2:-}"
      shift 2
      ;;
    --python)
      python_bin="${2:-}"
      shift 2
      ;;
    --extractor)
      extractor="${2:-}"
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

for required_arg in archive manifest go_bin python_bin extractor output; do
  [[ -n "${!required_arg}" ]] || die "--${required_arg//_/-} is required"
  printf -v "${required_arg}" '%s' "$(absolute_path "${!required_arg}")"
done
case "${goarch}" in
  amd64|arm64) ;;
  *) die "--goarch must be amd64 or arm64" ;;
esac

[[ -f "${archive}" ]] || die "declared source archive is missing: ${archive}"
[[ -f "${manifest}" ]] || die "declared manifest is missing: ${manifest}"
[[ -f "${extractor}" ]] || die "declared extractor is missing: ${extractor}"
for executable in "${go_bin}" "${python_bin}"; do
  [[ -x "${executable}" ]] || die "declared tool is not executable: ${executable}"
done

output_dir="$(dirname "${output}")"
mkdir -p "${output_dir}"
output="$(cd "${output_dir}" && pwd)/$(basename "${output}")"
work_root="$(mktemp -d "${output_dir}/tunnel-client-image-cloudflared.XXXXXX")"
trap 'rm -rf "${work_root}"' EXIT

# The existing archive extractor deliberately accepts only Bazel-owned
# scratch roots. Give this action the same private boundary as its tests.
export BAZEL_TEST=1
export TEST_TMPDIR="${work_root}"
source_root="$(
  "${python_bin}" "${extractor}" \
    --archive "${archive}" \
    --output-root "${work_root}/cloudflared-source"
)" || die "could not extract the declared cloudflared source archive"
[[ "${source_root}" == /* && -d "${source_root}" ]] ||
  die "extractor did not return an absolute source directory"

if ! IFS=$'\t' read -r version release_commit module_path build_time < <(
  "${python_bin}" - "${manifest}" "${goarch}" <<'PY'
import json
import pathlib
import sys

manifest_path = pathlib.Path(sys.argv[1])
goarch = sys.argv[2]
try:
    manifest = json.loads(manifest_path.read_text(encoding="utf-8"))
except (OSError, json.JSONDecodeError) as error:
    raise SystemExit(f"manifest is not valid JSON: {error}")

required = ("version", "release_commit", "module_path", "build_time")
missing = [field for field in required if not manifest.get(field)]
if missing:
    raise SystemExit("manifest is missing required fields: " + ", ".join(missing))
if f"linux/{goarch}" not in manifest.get("platforms", []):
    raise SystemExit(f"manifest does not support linux/{goarch}")

print("\t".join(str(manifest[field]) for field in required))
PY
); then
  die "could not read pinned cloudflared metadata"
fi
[[ "$(basename "${source_root}")" == "cloudflared-${release_commit}" ]] ||
  die "source archive commit does not match the declared manifest"
for required_file in go.mod vendor/modules.txt cmd/cloudflared/main.go; do
  [[ -f "${source_root}/${required_file}" ]] ||
    die "pinned cloudflared source is missing ${required_file}"
done

readonly go_cache_dir="${work_root}/go-cache"
readonly go_mod_cache_dir="${work_root}/go-mod-cache"
readonly go_path_dir="${work_root}/go-path"
readonly home_dir="${work_root}/home"
readonly runtime_tmp_dir="${work_root}/tmp"
mkdir -p \
  "${go_cache_dir}" \
  "${go_mod_cache_dir}" \
  "${go_path_dir}" \
  "${home_dir}" \
  "${runtime_tmp_dir}"

clear_ambient_go_build_environment
export LC_ALL=C
export TZ=UTC
export SOURCE_DATE_EPOCH="${FIXED_SOURCE_DATE_EPOCH}"
export HOME="${home_dir}"
export TMPDIR="${runtime_tmp_dir}"
export GOTMPDIR="${runtime_tmp_dir}"
export GOCACHE="${go_cache_dir}"
export GOMODCACHE="${go_mod_cache_dir}"
export GOPATH="${go_path_dir}"
export GOWORK=off
export GOPROXY=off
export GOSUMDB=off
export GOTOOLCHAIN=local
export GOENV=off
export GOTELEMETRY=off
export GOFLAGS=

selected_goroot="$(
  unset GOROOT
  "${go_bin}" env GOROOT
)" || die "could not derive GOROOT from --go"
[[ "${selected_goroot}" == /* && -d "${selected_goroot}/src" ]] ||
  die "--go reported an invalid GOROOT: ${selected_goroot}"
export GOROOT="${selected_goroot}"

actual_module_path="$(
  cd "${source_root}"
  "${go_bin}" list -mod=vendor -m -f '{{.Path}}'
)" || die "could not inspect the vendored cloudflared module"
[[ "${actual_module_path}" == "${module_path}" ]] ||
  die "pinned source module path mismatch: got ${actual_module_path}, want ${module_path}"

if ! (
  cd "${source_root}"
  env \
    GOWORK=off \
    GOPROXY=off \
    GOSUMDB=off \
    GOTOOLCHAIN=local \
    GOCACHE="${go_cache_dir}" \
    GOMODCACHE="${go_mod_cache_dir}" \
    GOOS=linux \
    GOARCH="${goarch}" \
    CGO_ENABLED=0 \
    "${go_bin}" build \
      -mod=vendor \
      -trimpath \
      -buildvcs=false \
      -ldflags "-X main.Version=${version} -X main.BuildTime=${build_time}" \
      -o "${output}" \
      ./cmd/cloudflared
); then
  die "offline cloudflared build failed for linux/${goarch}"
fi

chmod 0755 "${output}"
printf 'built pinned cloudflared %s for linux/%s to %s\n' "${version}" "${goarch}" "${output}"
