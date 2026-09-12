#!/usr/bin/env bash
set -euo pipefail

# Build deterministic client and companion payloads for the shared oai_sbom
# rule. Each component/platform can run independently. Every input path comes
# from the declaring action; this script does not scan the checkout.

readonly BASELINE_SEMANTIC_VERSION="0.0.0-baseline"
readonly BASELINE_GIT_SHA="baseline"
readonly FIXED_SOURCE_DATE_EPOCH="0"
readonly CANONICAL_MODULE_PATH="github.com/openai/tunnel-client"
readonly LEGACY_MODULE_PATH="github.com/openai/tunnel-client"
readonly -a PLATFORMS=(
  "linux/amd64"
  "linux/arm64"
  "darwin/amd64"
  "darwin/arm64"
  "windows/amd64"
  "windows/arm64"
)
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
  build_client_sbom_payload.sh \
    [--component client|cloudflared|both] \
    [--platform <goos>/<goarch>] \
    [--cloudflared-manifest <declared-manifest>] \
    --source-gopath <declared-go-path> \
    --go-mod <declared-go-mod> \
    --go-sum <declared-go-sum> \
    --license <declared-license> \
    --notice <declared-notice> \
    --vendor-digest <declared-digest> \
    --vendor-archive <declared-archive> \
    --cloudflared-archive <declared-archive> \
    --expected-cloudflared-version <manifest-version> \
    --expected-cloudflared-module-path <manifest-module-path> \
    --expected-cloudflared-module-version <manifest-module-version> \
    --go <declared-go-tool> \
    --python <declared-python-tool> \
    --source-preparer <declared-helper> \
    --vendor-extractor <declared-helper> \
    --cloudflared-extractor <declared-helper> \
    --output-root <declared-tree-artifact>

Builds both components for all six platforms by default. Client source/vendor
inputs are required for client or both; Cloudflared archive/metadata inputs are
required for cloudflared or both. Cloudflared-only builds require an explicit
manifest; combined builds use the staged client manifest by default.
EOF
}

die() {
  echo "build_client_sbom_payload.sh: $*" >&2
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

component="both"
platform=""
cloudflared_manifest=""
source_gopath=""
go_mod=""
go_sum=""
license_file=""
notice_file=""
vendor_digest=""
vendor_archive=""
cloudflared_archive=""
expected_cloudflared_version=""
expected_cloudflared_module_path=""
expected_cloudflared_module_version=""
go_bin=""
python_bin=""
source_preparer_bin=""
vendor_extractor_bin=""
cloudflared_extractor_bin=""
output_root=""
while [[ $# -gt 0 ]]; do
  case "$1" in
    --component) component="${2:-}"; shift 2 ;;
    --platform) platform="${2:-}"; shift 2 ;;
    --cloudflared-manifest) cloudflared_manifest="${2:-}"; shift 2 ;;
    --source-gopath)
      source_gopath="${2:-}"
      shift 2
      ;;
    --go-mod)
      go_mod="${2:-}"
      shift 2
      ;;
    --go-sum)
      go_sum="${2:-}"
      shift 2
      ;;
    --license)
      license_file="${2:-}"
      shift 2
      ;;
    --notice)
      notice_file="${2:-}"
      shift 2
      ;;
    --vendor-digest)
      vendor_digest="${2:-}"
      shift 2
      ;;
    --vendor-archive)
      vendor_archive="${2:-}"
      shift 2
      ;;
    --cloudflared-archive)
      cloudflared_archive="${2:-}"
      shift 2
      ;;
    --expected-cloudflared-version)
      expected_cloudflared_version="${2:-}"
      shift 2
      ;;
    --expected-cloudflared-module-path)
      expected_cloudflared_module_path="${2:-}"
      shift 2
      ;;
    --expected-cloudflared-module-version)
      expected_cloudflared_module_version="${2:-}"
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
    --source-preparer)
      source_preparer_bin="${2:-}"
      shift 2
      ;;
    --vendor-extractor)
      vendor_extractor_bin="${2:-}"
      shift 2
      ;;
    --cloudflared-extractor)
      cloudflared_extractor_bin="${2:-}"
      shift 2
      ;;
    --output-root)
      output_root="${2:-}"
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

case "${component}" in
  client|cloudflared|both) ;;
  *) die "--component must be client, cloudflared, or both" ;;
esac
selected_platforms=("${PLATFORMS[@]}")
if [[ -n "${platform}" ]]; then
  case "${platform}" in
    linux/amd64|linux/arm64|darwin/amd64|darwin/arm64|windows/amd64|windows/arm64) ;;
    *) die "--platform must be one supported release platform" ;;
  esac
  selected_platforms=("${platform}")
fi

required_paths=(go_bin python_bin source_preparer_bin output_root)
input_files=(source_preparer_bin)
if [[ "${component}" != "cloudflared" ]]; then
  required_paths+=(source_gopath go_mod go_sum license_file notice_file vendor_digest vendor_archive vendor_extractor_bin)
  input_files+=(go_mod go_sum license_file notice_file vendor_digest vendor_archive vendor_extractor_bin)
fi
if [[ "${component}" != "client" ]]; then
  required_paths+=(cloudflared_archive cloudflared_extractor_bin)
  input_files+=(cloudflared_archive cloudflared_extractor_bin)
  if [[ "${component}" == "cloudflared" || -n "${cloudflared_manifest}" ]]; then
    required_paths+=(cloudflared_manifest)
    input_files+=(cloudflared_manifest)
  fi
  for metadata_arg in expected_cloudflared_version expected_cloudflared_module_path expected_cloudflared_module_version; do
    [[ -n "${!metadata_arg}" ]] || die "--${metadata_arg//_/-} is required"
  done
fi
for required_arg in "${required_paths[@]}"; do
  [[ -n "${!required_arg}" ]] || die "--${required_arg//_/-} is required"
  printf -v "${required_arg}" '%s' "$(absolute_path "${!required_arg}")"
done
for input_file in "${input_files[@]}"; do
  [[ -f "${!input_file}" ]] || die "declared input file is missing: ${!input_file}"
done
for executable in "${go_bin}" "${python_bin}"; do
  [[ -x "${executable}" ]] || die "declared tool is not executable: ${executable}"
done
[[ ! -L "${output_root}" ]] || die "--output-root must not be a symlink"

readonly source_gopath_root="${source_gopath}/src/github.com/openai/tunnel-client"
if [[ "${component}" != "cloudflared" ]]; then
  [[ -d "${source_gopath_root}" ]] ||
    die "declared Go source tree is missing: ${source_gopath_root}"
fi

readonly work_root="${output_root}/.work"
[[ ! -e "${work_root}" && ! -L "${work_root}" ]] ||
  die "action work root already exists"
mkdir -p "$(dirname "${output_root}")" "${output_root}" "${work_root}"
trap 'rm -rf "${work_root}"' EXIT

# The extraction/preparation helpers enforce that mutable paths stay below
# TEST_TMPDIR. For this Bazel action, its private work root is that boundary;
# output_root remains the one declared TreeArtifact.
export BAZEL_TEST=1
export TEST_TMPDIR="${work_root}"

readonly source_root="${work_root}/client-source"
readonly cloudflared_extract_root="${work_root}/cloudflared-source"
readonly canonical_source_root="${work_root}/canonical-source"
readonly payload_root="${output_root}/payloads"
readonly go_cache_dir="${work_root}/go-cache"
readonly go_mod_cache_dir="${work_root}/go-mod-cache"
readonly go_path_dir="${work_root}/go-path"
readonly home_dir="${work_root}/home"
readonly runtime_tmp_dir="${work_root}/tmp"
mkdir -p \
  "${source_root}" \
  "${canonical_source_root}" \
  "${payload_root}" \
  "${go_cache_dir}" \
  "${go_mod_cache_dir}" \
  "${go_path_dir}" \
  "${home_dir}" \
  "${runtime_tmp_dir}"

if [[ "${component}" != "cloudflared" ]]; then
  cp -pRL -- "${source_gopath_root}/." "${source_root}/"
  chmod -R u+rwX "${source_root}"
  cp -pL -- "${go_mod}" "${source_root}/go.mod"
  cp -pL -- "${go_sum}" "${source_root}/go.sum"
  cp -pL -- "${license_file}" "${source_root}/LICENSE"
  cp -pL -- "${notice_file}" "${source_root}/NOTICE"
  mkdir -p "${source_root}/compliance"
  cp -pL -- "${vendor_digest}" "${source_root}/compliance/sbom-vendor-tree.sha256"

  "${python_bin}" "${vendor_extractor_bin}" \
    --source-root "${source_root}" \
    --vendor-archive "${vendor_archive}"
fi
if [[ "${component}" != "client" ]]; then
  cloudflared_source="$(
    "${python_bin}" "${cloudflared_extractor_bin}" \
      --archive "${cloudflared_archive}" \
      --output-root "${cloudflared_extract_root}"
  )"
fi

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

actual_go_version="$("${go_bin}" version | awk '{print $3}')"
[[ "${actual_go_version}" =~ ^go[0-9]+\.[0-9]+(\.[0-9]+)?$ ]] ||
  die "--go reported an invalid version: ${actual_go_version}"

if [[ "${component}" != "cloudflared" ]]; then
  for required_file in \
    go.mod \
    go.sum \
    LICENSE \
    NOTICE \
    vendor/modules.txt \
    cmd/client/main.go \
    pkg/cloudflared/manifest.json; do
    [[ -f "${source_root}/${required_file}" ]] ||
      die "required source input is missing: ${required_file}"
  done
  required_go_version="$(
    awk '
      $1 == "toolchain" { print $2; found = 1; exit }
      $1 == "go" { fallback = "go" $2 }
      END { if (!found && fallback != "") print fallback }
    ' "${source_root}/go.mod"
  )"
  [[ "${required_go_version}" =~ ^(go[0-9]+\.[0-9]+)(\.[0-9]+)?$ ]] ||
    die "go.mod does not declare an exact Go version"
  required_go_series="${BASH_REMATCH[1]}"
  case "${actual_go_version}" in
    "${required_go_series}"|"${required_go_series}".*) ;;
    *) die "go version mismatch: got ${actual_go_version}, want ${required_go_series}.x" ;;
  esac

  for relative_path in go.mod go.sum vendor LICENSE NOTICE cmd pkg docs plugins; do
    [[ -e "${source_root}/${relative_path}" ]] ||
      die "canonical source input is missing: ${relative_path}"
    cp -R -- "${source_root}/${relative_path}" "${canonical_source_root}/${relative_path}"
  done
  chmod -R u+rwX "${canonical_source_root}"

  "${python_bin}" "${source_preparer_bin}" canonicalize \
    --source-root "${canonical_source_root}" \
    --legacy-module-path "${LEGACY_MODULE_PATH}" \
    --canonical-module-path "${CANONICAL_MODULE_PATH}"

  module_path="$(
    cd "${canonical_source_root}"
    "${go_bin}" list -mod=vendor -m -f '{{.Path}}'
  )"
  [[ "${module_path}" == "${CANONICAL_MODULE_PATH}" ]] ||
    die "canonical source module path mismatch: got ${module_path}"

  client_ldflags="-X ${module_path}/pkg/version.semanticVersion=${BASELINE_SEMANTIC_VERSION} -X ${module_path}/pkg/version.GitSHA=${BASELINE_GIT_SHA} -X ${module_path}/pkg/version.GoVersion=${actual_go_version} -X ${module_path}/pkg/version.BuildFlags=-trimpath,-buildvcs=false -X ${module_path}/pkg/version.Flavor=full"
fi
if [[ "${component}" != "client" ]]; then
  for required_file in go.mod vendor/modules.txt cmd/cloudflared/main.go; do
    [[ -f "${cloudflared_source}/${required_file}" ]] ||
      die "required cloudflared source input is missing: ${required_file}"
  done

  if [[ -n "${cloudflared_manifest}" ]]; then
    cp -pL -- "${cloudflared_manifest}" "${work_root}/cloudflared-manifest.json"
    cloudflared_manifest="${work_root}/cloudflared-manifest.json"
  else
    cloudflared_manifest="${source_root}/pkg/cloudflared/manifest.json"
  fi
  cloudflared_metadata="$(
    "${python_bin}" "${source_preparer_bin}" cloudflared-metadata \
      --manifest "${cloudflared_manifest}" \
      --source-root "${cloudflared_source}"
  )" || die "could not read cloudflared manifest"
  IFS=$'\t' read -r \
    cloudflared_version \
    cloudflared_build_time \
    cloudflared_module_path \
    cloudflared_module_version <<<"${cloudflared_metadata}"
  [[ -n "${cloudflared_version}" &&
    -n "${cloudflared_build_time}" &&
    -n "${cloudflared_module_path}" &&
    -n "${cloudflared_module_version}" ]] ||
    die "cloudflared manifest is missing required metadata"
  [[ "${cloudflared_version}" == "${expected_cloudflared_version}" ]] ||
    die "cloudflared manifest version does not match declared SBOM metadata"
  [[ "${cloudflared_module_path}" == "${expected_cloudflared_module_path}" ]] ||
    die "cloudflared manifest module path does not match declared SBOM metadata"
  [[ "${cloudflared_module_version}" == "${expected_cloudflared_module_version}" ]] ||
    die "cloudflared manifest module version does not match declared SBOM metadata"

  cloudflared_ldflags="-X main.Version=${cloudflared_version} -X main.BuildTime=${cloudflared_build_time}"
fi

for platform in "${selected_platforms[@]}"; do
  goos="${platform%/*}"
  goarch="${platform#*/}"
  extension=""
  [[ "${goos}" == "windows" ]] && extension=".exe"
  payload_dir="${payload_root}/${goos}_${goarch}"
  mkdir -p "${payload_dir}"

  if [[ "${component}" != "cloudflared" ]]; then
    (
      cd "${canonical_source_root}"
      env \
        GOWORK=off \
        GOPROXY=off \
        GOSUMDB=off \
        GOTOOLCHAIN=local \
        GOCACHE="${go_cache_dir}" \
        GOMODCACHE="${go_mod_cache_dir}" \
        GOOS="${goos}" \
        GOARCH="${goarch}" \
        CGO_ENABLED=0 \
        "${go_bin}" build \
        -mod=vendor \
        -trimpath \
        -buildvcs=false \
        -ldflags "${client_ldflags}" \
        -o "${payload_dir}/tunnel-client${extension}" \
        ./cmd/client
    )

    cp "${source_root}/LICENSE" "${payload_dir}/LICENSE"
  fi
  if [[ "${component}" != "client" ]]; then
    (
      cd "${cloudflared_source}"
      env \
        GOWORK=off \
        GOPROXY=off \
        GOSUMDB=off \
        GOTOOLCHAIN=local \
        GOCACHE="${go_cache_dir}" \
        GOMODCACHE="${go_mod_cache_dir}" \
        GOOS="${goos}" \
        GOARCH="${goarch}" \
        CGO_ENABLED=0 \
        "${go_bin}" build \
        -mod=vendor \
        -trimpath \
        -buildvcs=false \
        -ldflags "${cloudflared_ldflags}" \
        -o "${payload_dir}/cloudflared${extension}" \
        ./cmd/cloudflared
    )
    cp "${cloudflared_manifest}" "${payload_dir}/cloudflared-manifest.json"
  fi
done

printf 'built %s SBOM payload for %s platform(s)\n' "${component}" "${#selected_platforms[@]}"
