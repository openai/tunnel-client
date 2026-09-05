#!/usr/bin/env bash
set -euo pipefail

# Build deterministic release-platform client and narrow-runtime payloads,
# then delegate SPDX generation and normalization to the shared oai_sbom tool.
# Public callers get all six release platforms unless they request one leaf.

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
  ./scripts/generate_client_sbom.sh \
    --flavor client|runtime|runtime-cloudflared \
    --source-root <staged-source> \
    --cloudflared-source <staged-source> \
    --go <absolute-path> \
    --python <absolute-path> \
    --source-preparer <absolute-path> \
    [--license-report-builder <absolute-path>] \
    [--base-license-report <absolute-path>] \
    [--companion-license-report <absolute-path>] \
    --oai-sbom <absolute-path> \
    --syft <absolute-path> \
    --syft-lock <absolute-path> \
    [--output <spdx-path>] \
    [--platform <goos>/<goarch>] \
    [--payload-only-output <absolute-directory>] \
    [--prebuilt-payload <goos/goarch>=<absolute-directory>]

Builds a deterministic baseline from declared offline inputs and generates one
SPDX 2.3 report through oai_sbom. All six release platforms are included by
default; --platform narrows the payload to one release-platform leaf. Runtime
flavors also stage their checked-in artifact-scoped license sidecars.

--payload-only-output emits the staged payload without invoking Syft and
requires one explicit --platform. --prebuilt-payload may be repeated to supply
the exact platform payloads that the final six-platform scan should assemble.
EOF
}

die() {
  echo "generate_client_sbom.sh: $*" >&2
  exit 1
}

clear_ambient_go_build_environment() {
  unset "${AMBIENT_GO_BUILD_ENV_VARS[@]}"
}

flavor="client"
requested_platform=""
source_root=""
cloudflared_source=""
go_bin=""
python_bin=""
source_preparer_bin=""
license_report_builder=""
base_license_report=""
companion_license_report=""
oai_sbom_bin=""
syft_bin=""
syft_lock=""
output_path=""
payload_only_output=""
prebuilt_payload_specs=()
while [[ $# -gt 0 ]]; do
  case "$1" in
    --flavor)
      flavor="${2:-}"
      shift 2
      ;;
    --platform)
      requested_platform="${2:-}"
      shift 2
      ;;
    --platform=*)
      requested_platform="${1#*=}"
      shift
      ;;
    --source-root)
      source_root="${2:-}"
      shift 2
      ;;
    --cloudflared-source)
      cloudflared_source="${2:-}"
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
    --license-report-builder)
      license_report_builder="${2:-}"
      shift 2
      ;;
    --base-license-report)
      base_license_report="${2:-}"
      shift 2
      ;;
    --companion-license-report)
      companion_license_report="${2:-}"
      shift 2
      ;;
    --oai-sbom)
      oai_sbom_bin="${2:-}"
      shift 2
      ;;
    --syft)
      syft_bin="${2:-}"
      shift 2
      ;;
    --syft-lock)
      syft_lock="${2:-}"
      shift 2
      ;;
    --output)
      output_path="${2:-}"
      shift 2
      ;;
    --payload-only-output)
      payload_only_output="${2:-}"
      shift 2
      ;;
    --prebuilt-payload)
      prebuilt_payload_specs+=("${2:-}")
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
case "${requested_platform}" in
  ""|linux/amd64|linux/arm64|darwin/amd64|darwin/arm64|windows/amd64|windows/arm64) ;;
  *) die "--platform must be one supported goos/goarch release platform" ;;
esac

selected_platforms=("${PLATFORMS[@]}")
if [[ -n "${requested_platform}" ]]; then
  selected_platforms=("${requested_platform}")
fi

[[ -z "${payload_only_output}" || ${#prebuilt_payload_specs[@]} -eq 0 ]] ||
  die "--payload-only-output cannot be combined with --prebuilt-payload"
[[ -z "${payload_only_output}" || -n "${requested_platform}" ]] ||
  die "--payload-only-output requires one explicit --platform"
[[ -z "${payload_only_output}" || -z "${output_path}" ]] ||
  die "--payload-only-output cannot be combined with --output"

prebuilt_payload_platforms=()
prebuilt_payload_roots=()
# Bash 3.2 treats an empty array expansion as unbound under set -u.
for spec in "${prebuilt_payload_specs[@]+"${prebuilt_payload_specs[@]}"}"; do
  [[ "${spec}" == *=* ]] ||
    die "--prebuilt-payload must be formatted as goos/goarch=absolute-directory"
  mapped_platform="${spec%%=*}"
  mapped_root="${spec#*=}"
  case "${mapped_platform}" in
    linux/amd64|linux/arm64|darwin/amd64|darwin/arm64|windows/amd64|windows/arm64) ;;
    *) die "--prebuilt-payload uses an unsupported platform: ${mapped_platform}" ;;
  esac
  [[ "${mapped_root}" == /* && -d "${mapped_root}" ]] ||
    die "--prebuilt-payload root must be an absolute directory: ${mapped_root}"
  [[ ! -L "${mapped_root}" ]] ||
    die "--prebuilt-payload root must not be a symlink: ${mapped_root}"
  for existing_platform in "${prebuilt_payload_platforms[@]+"${prebuilt_payload_platforms[@]}"}"; do
    [[ "${existing_platform}" != "${mapped_platform}" ]] ||
      die "duplicate --prebuilt-payload platform: ${mapped_platform}"
  done
  prebuilt_payload_platforms+=("${mapped_platform}")
  prebuilt_payload_roots+=("${mapped_root}")
done
if [[ ${#prebuilt_payload_platforms[@]} -gt 0 ]]; then
  [[ ${#prebuilt_payload_platforms[@]} -eq ${#selected_platforms[@]} ]] ||
    die "--prebuilt-payload must provide every selected platform exactly once"
  for platform in "${selected_platforms[@]}"; do
    found="false"
    for existing_platform in "${prebuilt_payload_platforms[@]}"; do
      [[ "${existing_platform}" != "${platform}" ]] || found="true"
    done
    [[ "${found}" == "true" ]] ||
      die "--prebuilt-payload is missing selected platform: ${platform}"
  done
fi

for required_arg in \
  source_root \
  go_bin \
  python_bin \
  source_preparer_bin; do
  [[ -n "${!required_arg}" ]] || die "--${required_arg//_/-} is required"
done
if [[ "${flavor}" != "runtime" ]]; then
  [[ -n "${cloudflared_source}" ]] || die "--cloudflared-source is required"
fi
if [[ -z "${payload_only_output}" ]]; then
  for required_arg in oai_sbom_bin syft_bin syft_lock output_path; do
    [[ -n "${!required_arg}" ]] || die "--${required_arg//_/-} is required"
  done
fi
[[ "${source_root}" == /* && -d "${source_root}" ]] ||
  die "--source-root must be an absolute directory"
if [[ -n "${cloudflared_source}" ]]; then
  [[ "${cloudflared_source}" == /* && -d "${cloudflared_source}" ]] ||
    die "--cloudflared-source must be an absolute directory"
fi
for executable in \
  "${go_bin}" \
  "${python_bin}"; do
  [[ "${executable}" == /* && -x "${executable}" ]] ||
    die "tool must be an absolute executable: ${executable}"
done
if [[ -z "${payload_only_output}" ]]; then
  for executable in "${oai_sbom_bin}" "${syft_bin}"; do
    [[ "${executable}" == /* && -x "${executable}" ]] ||
      die "tool must be an absolute executable: ${executable}"
  done
fi
[[ "${source_preparer_bin}" == /* && -f "${source_preparer_bin}" ]] ||
  die "--source-preparer must be an absolute file"
if [[ -z "${payload_only_output}" ]]; then
  [[ "${syft_lock}" == /* && -f "${syft_lock}" ]] ||
    die "--syft-lock must be an absolute file"
  [[ "${output_path}" == /* ]] || die "--output must be an absolute path"
else
  [[ "${payload_only_output}" == /* ]] ||
    die "--payload-only-output must be an absolute path"
fi
if [[ "${flavor}" != "client" ]]; then
  [[ "${license_report_builder}" == /* && -x "${license_report_builder}" ]] ||
    die "--license-report-builder must be an absolute executable for runtime flavors"
  [[ "${base_license_report}" == /* && -f "${base_license_report}" ]] ||
    die "--base-license-report must be an absolute file for runtime flavors"
  if [[ "${flavor}" == "runtime-cloudflared" ]]; then
    [[ "${companion_license_report}" == /* && -f "${companion_license_report}" ]] ||
      die "--companion-license-report must be an absolute file for runtime-cloudflared"
  fi
fi
[[ -n "${BAZEL_TEST:-}" ]] || die "BAZEL_TEST is required"
[[ -n "${TEST_TMPDIR:-}" && "${TEST_TMPDIR}" == /* ]] ||
  die "TEST_TMPDIR is required under Bazel"
staged_paths=("${source_root}")
if [[ -n "${cloudflared_source}" ]]; then
  staged_paths+=("${cloudflared_source}")
fi
if [[ ${#prebuilt_payload_roots[@]} -gt 0 ]]; then
  staged_paths+=("${prebuilt_payload_roots[@]}")
fi
if [[ -n "${payload_only_output}" ]]; then
  staged_paths+=("${payload_only_output}")
else
  staged_paths+=("${output_path}")
fi
for staged_path in "${staged_paths[@]}"; do
  case "${staged_path}" in
    "${TEST_TMPDIR}"/*) ;;
    *) die "Bazel test staging must stay below TEST_TMPDIR: ${staged_path}" ;;
  esac
done

tmp_parent="${TEST_TMPDIR}"
mkdir -p "${tmp_parent}"
tmp_dir="$(mktemp -d "${tmp_parent%/}/tunnel-client-sbom.XXXXXX")"
trap 'rm -rf "${tmp_dir}"' EXIT

target=""
binary_name=""
linked_flavor=""
stable_root_name=""
source_name=""
include_cloudflared="false"
include_runtime_sidecars="false"
cloudflared_manifest_path=""
case "${flavor}" in
  client)
    target="./cmd/client"
    binary_name="tunnel-client"
    linked_flavor="full"
    stable_root_name="client"
    source_name="tunnel-client-baseline"
    include_cloudflared="true"
    cloudflared_manifest_path="pkg/cloudflared/manifest.json"
    ;;
  runtime)
    target="./cmd/client-runtime"
    binary_name="tunnel-client-runtime"
    linked_flavor="runtime"
    stable_root_name="runtime"
    source_name="tunnel-client-runtime-baseline"
    include_runtime_sidecars="true"
    ;;
  runtime-cloudflared)
    target="./cmd/client-runtime-cloudflared"
    binary_name="tunnel-client-runtime-cloudflared"
    linked_flavor="runtime-cloudflared"
    stable_root_name="runtime-cloudflared"
    source_name="tunnel-client-runtime-cloudflared-baseline"
    include_cloudflared="true"
    include_runtime_sidecars="true"
    cloudflared_manifest_path="pkg/cloudflared/runtime/manifest.json"
    ;;
esac

readonly canonical_source_root="${tmp_dir}/canonical-source"
readonly stable_root="${tmp_dir}/${stable_root_name}"
readonly payload_root="${stable_root}/payloads"
readonly go_cache_dir="${tmp_dir}/go-cache"
readonly go_mod_cache_dir="${tmp_dir}/go-mod-cache"
readonly go_path_dir="${tmp_dir}/go-path"
readonly home_dir="${tmp_dir}/home"
readonly runtime_tmp_dir="${tmp_dir}/tmp"
readonly oai_sbom_cache_dir="${tmp_dir}/oai-sbom-cache"
output_parent=""
if [[ -n "${output_path}" ]]; then
  output_parent="$(dirname "${output_path}")"
else
  output_parent="$(dirname "${payload_only_output}")"
fi
mkdir -p \
  "${canonical_source_root}" \
  "${payload_root}" \
  "${go_cache_dir}" \
  "${go_mod_cache_dir}" \
  "${go_path_dir}" \
  "${home_dir}" \
  "${runtime_tmp_dir}" \
  "${oai_sbom_cache_dir}" \
  "${output_parent}"

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

if [[ ${#prebuilt_payload_platforms[@]} -gt 0 ]]; then
  # Version and module metadata need only the declared Go executable. Keep
  # GOROOT beside that input without resolving runfile symlinks to a full SDK.
  selected_goroot="$(dirname "${go_bin}")"
  # The mode is file-backed; prevent telemetry from outliving private staging.
  export XDG_CONFIG_HOME="${home_dir}/.config" APPDATA="${home_dir}/.config"
  GOROOT="${selected_goroot}" "${go_bin}" telemetry off
else
  selected_goroot="$(
    unset GOROOT
    "${go_bin}" env GOROOT
  )" || die "could not derive GOROOT from --go"
  [[ "${selected_goroot}" == /* && -d "${selected_goroot}/src" ]] ||
    die "--go reported an invalid GOROOT: ${selected_goroot}"
fi
export GOROOT="${selected_goroot}"

required_source_files=(
  go.mod
  go.sum
  LICENSE
  NOTICE
  vendor/modules.txt
  "${target#./}/main.go"
)
if [[ "${include_cloudflared}" == "true" ]]; then
  required_source_files+=("${cloudflared_manifest_path}")
fi
for required_file in "${required_source_files[@]}"; do
  [[ -f "${source_root}/${required_file}" ]] ||
    die "required source input is missing: ${required_file}"
done
if [[ "${include_cloudflared}" == "true" ]]; then
  for required_file in go.mod vendor/modules.txt cmd/cloudflared/main.go; do
    [[ -f "${cloudflared_source}/${required_file}" ]] ||
      die "required cloudflared source input is missing: ${required_file}"
  done
fi

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
actual_go_version="$("${go_bin}" version | awk '{print $3}')"
[[ "${actual_go_version}" =~ ^go[0-9]+\.[0-9]+(\.[0-9]+)?$ ]] ||
  die "--go reported an invalid version: ${actual_go_version}"
case "${actual_go_version}" in
  "${required_go_series}"|"${required_go_series}".*) ;;
  *) die "go version mismatch: got ${actual_go_version}, want ${required_go_series}.x" ;;
esac

for relative_path in go.mod go.sum vendor LICENSE NOTICE cmd pkg docs plugins; do
  [[ -e "${source_root}/${relative_path}" ]] || continue
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

cloudflared_version=""
cloudflared_build_time=""
cloudflared_module_path=""
cloudflared_module_version=""
if [[ "${include_cloudflared}" == "true" ]]; then
  cloudflared_metadata="$(
    "${python_bin}" "${source_preparer_bin}" cloudflared-metadata \
      --manifest "${source_root}/${cloudflared_manifest_path}" \
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
fi

artifact_ldflags="-X ${module_path}/pkg/version.semanticVersion=${BASELINE_SEMANTIC_VERSION} -X ${module_path}/pkg/version.GitSHA=${BASELINE_GIT_SHA} -X ${module_path}/pkg/version.GoVersion=${actual_go_version} -X ${module_path}/pkg/version.BuildFlags=-trimpath,-buildvcs=false -X ${module_path}/pkg/version.Flavor=${linked_flavor}"
cloudflared_ldflags="-X main.Version=${cloudflared_version} -X main.BuildTime=${cloudflared_build_time}"

prebuilt_payload_root_for_platform() {
  local wanted_platform="$1"
  local index
  for ((index = 0; index < ${#prebuilt_payload_platforms[@]}; index++)); do
    if [[ "${prebuilt_payload_platforms[index]}" == "${wanted_platform}" ]]; then
      printf '%s\n' "${prebuilt_payload_roots[index]}"
      return 0
    fi
  done
  return 1
}

for platform in "${selected_platforms[@]}"; do
  goos="${platform%/*}"
  goarch="${platform#*/}"
  extension=""
  [[ "${goos}" == "windows" ]] && extension=".exe"
  payload_dir="${payload_root}/${goos}_${goarch}"
  mkdir -p "${payload_dir}"

  if [[ ${#prebuilt_payload_platforms[@]} -gt 0 ]]; then
    prebuilt_root="$(prebuilt_payload_root_for_platform "${platform}")" ||
      die "prebuilt payload root is missing for ${platform}"
    prebuilt_dir="${prebuilt_root}/payloads/${goos}_${goarch}"
    [[ -d "${prebuilt_dir}" ]] ||
      die "prebuilt payload is missing payloads/${goos}_${goarch}"
    expected_payload_files=(
      "${binary_name}${extension}"
      "LICENSE"
    )
    if [[ "${include_runtime_sidecars}" == "true" ]]; then
      expected_payload_files+=(
        "NOTICE"
        "${binary_name}-${goos}-${goarch}-licenses.txt"
      )
    fi
    if [[ "${include_cloudflared}" == "true" ]]; then
      expected_payload_files+=(
        "cloudflared${extension}"
        "cloudflared-manifest.json"
      )
    fi
    actual_payload_file_count="$(
      find "${prebuilt_dir}" -mindepth 1 -maxdepth 1 -print | wc -l | tr -d ' '
    )"
    [[ "${actual_payload_file_count}" == "${#expected_payload_files[@]}" ]] ||
      die "prebuilt payload has unexpected entries for ${platform}"
    [[ -z "$(find "${prebuilt_dir}" -type l -print -quit)" ]] ||
      die "prebuilt payload must not contain symlinks for ${platform}"
    for expected_payload_file in "${expected_payload_files[@]}"; do
      [[ -f "${prebuilt_dir}/${expected_payload_file}" ]] ||
        die "prebuilt payload is missing ${platform}/${expected_payload_file}"
    done
    cp -pRL -- "${prebuilt_dir}/." "${payload_dir}/"
    # Remote TreeArtifact inputs can be read-only. Keep the declared input
    # immutable, but make this private copy removable by the EXIT cleanup.
    chmod -R u+rwX "${payload_dir}"
    continue
  fi

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
      -ldflags "${artifact_ldflags}" \
      -o "${payload_dir}/${binary_name}${extension}" \
      "${target}"
  )

  if [[ "${include_cloudflared}" == "true" ]]; then
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
    cp "${source_root}/${cloudflared_manifest_path}" "${payload_dir}/cloudflared-manifest.json"
  fi
  cp "${source_root}/LICENSE" "${payload_dir}/LICENSE"
  if [[ "${include_runtime_sidecars}" == "true" ]]; then
    cp "${source_root}/NOTICE" "${payload_dir}/NOTICE"
    report_args=(
      --flavor "${flavor}"
      --goos "${goos}"
      --goarch "${goarch}"
      --python "${python_bin}"
      --base-report "${base_license_report}"
      --output "${payload_dir}/${binary_name}-${goos}-${goarch}-licenses.txt"
    )
    if [[ -n "${companion_license_report}" ]]; then
      report_args+=(--companion-report "${companion_license_report}")
    fi
    "${license_report_builder}" "${report_args[@]}" >/dev/null
  fi
done

if [[ -n "${payload_only_output}" ]]; then
  [[ ! -e "${payload_only_output}" && ! -L "${payload_only_output}" ]] ||
    die "--payload-only-output must not already exist"
  mkdir -p "${payload_only_output}"
  cp -pRL -- "${stable_root}/." "${payload_only_output}/"
  printf 'built %s SBOM payload for %s\n' "${flavor}" "${requested_platform}"
  exit 0
fi

oai_sbom_args=(
  generate-spdx
  --payload-dir "${stable_root}"
  --output "${output_path}"
  --syft "${syft_bin}"
  --syft-lock "${syft_lock}"
  --source-name "${source_name}"
  --source-version baseline
)
if [[ "${include_cloudflared}" == "true" ]]; then
  oai_sbom_args+=(
    --package-version
    "${cloudflared_module_path}=${cloudflared_module_version}"
    --package-purl
    "${cloudflared_module_path}=pkg:golang/${cloudflared_module_path}@${cloudflared_module_version}"
    --package-cpe23
    "${cloudflared_module_path}=cpe:2.3:a:cloudflare:cloudflared:${cloudflared_version}:*:*:*:*:*:*:*"
  )
fi
oai_sbom_args+=(
  --normalize
  --namespace-prefix https://github.com/openai/tunnel-client/sbom/baseline/
  --cache-dir "${oai_sbom_cache_dir}"
)
"${oai_sbom_bin}" "${oai_sbom_args[@]}"

if [[ -n "${requested_platform}" ]]; then
  printf 'generated %s SBOM for %s\n' "${flavor}" "${requested_platform}"
else
  printf 'generated %s SBOM for six platforms\n' "${flavor}"
fi
