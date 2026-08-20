#!/usr/bin/env bash
set -euo pipefail

readonly SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
readonly PROJECT_ROOT="$(cd "${SCRIPT_DIR}/.." && pwd)"
readonly COMPLIANCE_DIR="${PROJECT_ROOT}/compliance"
readonly SYFT_VERSION="1.46.0"
readonly BASELINE_SEMANTIC_VERSION="0.0.0-baseline"
readonly BASELINE_GIT_SHA="baseline"
readonly FIXED_SOURCE_DATE_EPOCH="0"
readonly CANONICAL_MODULE_PATH="github.com/openai/tunnel-client"
# Keep the legacy path split so text-level source rewrites preserve this alias.
readonly LEGACY_MODULE_PATH_PREFIX="go.openai.org/api"
readonly LEGACY_MODULE_PATH="${LEGACY_MODULE_PATH_PREFIX}/tunnel-client"
readonly -a PLATFORMS=(
  "linux/amd64"
  "linux/arm64"
  "darwin/amd64"
  "darwin/arm64"
  "windows/amd64"
  "windows/arm64"
)
readonly -a BASELINE_FILES=(
  "tunnel-client.spdx.json"
  "tunnel-client-runtime.spdx.json"
  "tunnel-client-runtime-cloudflared.spdx.json"
  "sbom-baseline-manifest.json"
)

# Bazel sh_tests provide the SDK as data and pass its rlocation in the
# environment. Direct Make/script invocations retain their existing PATH.
# shellcheck source=runtime_runfiles.sh
source "${SCRIPT_DIR}/runtime_runfiles.sh"

usage() {
  cat <<'EOF'
Usage:
  ./scripts/generate_sbom_baselines.sh --write [--output-dir <directory>]
  ./scripts/generate_sbom_baselines.sh --check [--flavor client|runtime|runtime-cloudflared] [--output-dir <directory>]

Builds canonical six-platform payload unions and generates deterministic
deterministic SPDX 2.3 baselines. --write updates the destination directory
(compliance by default). --check compares freshly generated output with
compliance; --output-dir optionally keeps the fresh output for inspection.
EOF
}

die() {
  echo "generate_sbom_baselines.sh: $*" >&2
  exit 1
}

sha256_file() {
  local file="$1"
  if command -v sha256sum >/dev/null 2>&1; then
    sha256sum "${file}" | awk '{print $1}'
    return 0
  fi
  if command -v shasum >/dev/null 2>&1; then
    shasum -a 256 "${file}" | awk '{print $1}'
    return 0
  fi
  die "sha256sum or shasum is required"
}

is_spdx_23() {
  local path="$1"
  python3 - "${path}" <<'PY'
import json
import pathlib
import sys

try:
    document = json.loads(pathlib.Path(sys.argv[1]).read_text(encoding="utf-8"))
except (OSError, json.JSONDecodeError):
    raise SystemExit(1)
if not isinstance(document, dict) or document.get("spdxVersion") != "SPDX-2.3":
    raise SystemExit(1)
PY
}

mode=""
selected_flavor=""
requested_output_dir=""
while [[ $# -gt 0 ]]; do
  case "$1" in
    --write)
      [[ -z "${mode}" ]] || die "choose exactly one of --write or --check"
      mode="write"
      shift
      ;;
    --check)
      [[ -z "${mode}" ]] || die "choose exactly one of --write or --check"
      mode="check"
      shift
      ;;
    --flavor)
      selected_flavor="${2:-}"
      shift 2
      ;;
    --output-dir)
      requested_output_dir="${2:-}"
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

[[ -n "${mode}" ]] || die "choose exactly one of --write or --check"
case "${selected_flavor}" in
  ""|client|runtime|runtime-cloudflared) ;;
  *) die "--flavor must be client, runtime, or runtime-cloudflared" ;;
esac
if [[ "${mode}" == "write" && -n "${selected_flavor}" ]]; then
  die "--flavor is supported only with --check"
fi

use_tunnel_client_bazel_go_sdk || exit 1
command -v go >/dev/null 2>&1 || die "go is required"
command -v python3 >/dev/null 2>&1 || die "python3 is required"

export LC_ALL=C
export TZ=UTC
export SOURCE_DATE_EPOCH="${FIXED_SOURCE_DATE_EPOCH}"

cd "${PROJECT_ROOT}"

required_go_version="$(
  awk '
    $1 == "toolchain" { print $2; found = 1; exit }
    $1 == "go" { fallback = "go" $2 }
    END { if (!found && fallback != "") print fallback }
  ' go.mod
)"
[[ "${required_go_version}" =~ ^go[0-9]+\.[0-9]+(\.[0-9]+)?$ ]] ||
  die "go.mod does not declare an exact Go version"
actual_go_version="$(go version | awk '{print $3}')"
[[ "${actual_go_version}" == "${required_go_version}" ]] ||
  die "go version mismatch: got ${actual_go_version}, want ${required_go_version}"

go_cache_dir="${GOCACHE:-${TMPDIR:-/tmp}/tunnel-client-runtime-go-cache}"
go_mod_cache_dir="${GOMODCACHE:-${TMPDIR:-/tmp}/tunnel-client-runtime-go-mod-cache}"
mkdir -p "${go_cache_dir}" "${go_mod_cache_dir}"

for required_file in LICENSE NOTICE; do
  [[ -f "${required_file}" ]] || die "required payload file is missing: ${required_file}"
done

cloudflared_manifest="${PROJECT_ROOT}/pkg/cloudflared/manifest.json"
[[ -f "${cloudflared_manifest}" ]] ||
  die "companion manifest is missing: pkg/cloudflared/manifest.json"
cloudflared_manifest_sha="$(sha256_file "${cloudflared_manifest}")"

syft_bin="$("${SCRIPT_DIR}/install_syft.sh")"
[[ "${syft_bin}" == /* && -x "${syft_bin}" ]] ||
  die "installer did not return an executable absolute path"
version_output="$("${syft_bin}" version 2>&1)" ||
  die "could not run Syft version check"
printf '%s\n' "${version_output}" |
  grep -Eq '^[[:space:]]*Version:[[:space:]]+v?1\.46\.0([[:space:]]|$)' ||
  die "Syft must report version ${SYFT_VERSION}"

tmp_dir="$(mktemp -d "${TMPDIR:-/tmp}/tunnel-client-sbom-baselines.XXXXXX")"
trap 'rm -rf "${tmp_dir}"' EXIT
generated_dir="${tmp_dir}/generated"
stable_root_base="${tmp_dir}/stable-root"
raw_dir="${tmp_dir}/raw"
syft_cache_dir="${SYFT_CACHE_DIR:-${tmp_dir}/syft-cache}"
canonical_source_root="${tmp_dir}/canonical-source"
mkdir -p \
  "${generated_dir}" \
  "${stable_root_base}" \
  "${raw_dir}" \
  "${syft_cache_dir}" \
  "${canonical_source_root}"

create_canonical_source() {
  local relative_path
  for relative_path in go.mod go.sum LICENSE NOTICE cmd pkg docs plugins; do
    [[ -e "${PROJECT_ROOT}/${relative_path}" ]] ||
      die "canonical source input is missing: ${relative_path}"
    cp -R \
      "${PROJECT_ROOT}/${relative_path}" \
      "${canonical_source_root}/${relative_path}"
  done

  python3 - \
    "${canonical_source_root}" \
    "${LEGACY_MODULE_PATH}" \
    "${CANONICAL_MODULE_PATH}" <<'PY'
import pathlib
import sys

root = pathlib.Path(sys.argv[1])
internal_module_path = sys.argv[2].encode("utf-8")
canonical_module_path = sys.argv[3].encode("utf-8")

for path in sorted(root.rglob("*")):
    if path.is_symlink() or not path.is_file():
        continue
    contents = path.read_bytes()
    if internal_module_path in contents:
        path.write_bytes(contents.replace(internal_module_path, canonical_module_path))
PY
}

create_canonical_source
module_path="$(
  cd "${canonical_source_root}"
  env \
    GOWORK=off \
    GOCACHE="${go_cache_dir}" \
    GOMODCACHE="${go_mod_cache_dir}" \
    go list -m -f '{{.Path}}'
)"
[[ "${module_path}" == "${CANONICAL_MODULE_PATH}" ]] ||
  die "canonical source module path mismatch: got ${module_path}, want ${CANONICAL_MODULE_PATH}"

license_report_for() {
  local selected_flavor="$1"
  case "${selected_flavor}" in
    client) printf '%s\n' "${COMPLIANCE_DIR}/oss-license-report-client.txt" ;;
    runtime) printf '%s\n' "${COMPLIANCE_DIR}/oss-license-report-runtime.txt" ;;
    runtime-cloudflared) printf '%s\n' "${COMPLIANCE_DIR}/oss-license-report-runtime-cloudflared.txt" ;;
    *) die "unexpected flavor: ${selected_flavor}" ;;
  esac
}

build_baseline() {
  local selected_flavor="$1"
  local target binary_name document_name flavor_value include_cloudflared source_name
  case "${selected_flavor}" in
    client)
      target="./cmd/client"
      binary_name="tunnel-client"
      document_name="tunnel-client.spdx.json"
      flavor_value="full"
      include_cloudflared="true"
      source_name="tunnel-client-baseline"
      ;;
    runtime)
      target="./cmd/client-runtime"
      binary_name="tunnel-client-runtime"
      document_name="tunnel-client-runtime.spdx.json"
      flavor_value="runtime"
      include_cloudflared="false"
      source_name="tunnel-client-runtime-baseline"
      ;;
    runtime-cloudflared)
      target="./cmd/client-runtime-cloudflared"
      binary_name="tunnel-client-runtime-cloudflared"
      document_name="tunnel-client-runtime-cloudflared.spdx.json"
      flavor_value="runtime-cloudflared"
      include_cloudflared="true"
      source_name="tunnel-client-runtime-cloudflared-baseline"
      ;;
    *)
      die "unexpected flavor: ${selected_flavor}"
      ;;
  esac

  local license_report
  license_report="$(license_report_for "${selected_flavor}")"
  [[ -f "${license_report}" ]] ||
    die "license report is missing for ${selected_flavor}: ${license_report}"

  local stable_root="${stable_root_base}/${selected_flavor}"
  local payload_root="${stable_root}/payloads"
  mkdir -p "${payload_root}"

  local platform goos goarch extension payload_dir binary_path report_name
  local ldflags
  ldflags="-X ${module_path}/pkg/version.semanticVersion=${BASELINE_SEMANTIC_VERSION} -X ${module_path}/pkg/version.GitSHA=${BASELINE_GIT_SHA} -X ${module_path}/pkg/version.GoVersion=${required_go_version} -X ${module_path}/pkg/version.BuildFlags=-trimpath,-buildvcs=false -X ${module_path}/pkg/version.Flavor=${flavor_value}"

  for platform in "${PLATFORMS[@]}"; do
    goos="${platform%/*}"
    goarch="${platform#*/}"
    extension=""
    [[ "${goos}" == "windows" ]] && extension=".exe"
    payload_dir="${payload_root}/${goos}_${goarch}"
    mkdir -p "${payload_dir}"
    binary_path="${payload_dir}/${binary_name}${extension}"

    if ! (
      cd "${canonical_source_root}"
      env \
        GOWORK=off \
        GOCACHE="${go_cache_dir}" \
        GOMODCACHE="${go_mod_cache_dir}" \
        GOOS="${goos}" \
        GOARCH="${goarch}" \
        CGO_ENABLED=0 \
        go build \
          -mod=readonly \
          -trimpath \
          -buildvcs=false \
          -ldflags "${ldflags}" \
          -o "${binary_path}" \
          "${target}"
    ); then
      die "${selected_flavor} build failed for ${platform}"
    fi

    cp "${canonical_source_root}/LICENSE" "${canonical_source_root}/NOTICE" "${payload_dir}/"
    report_name="${binary_name}-${goos}-${goarch}-licenses.txt"
    "${SCRIPT_DIR}/build_artifact_license_report.sh" \
      --flavor "${selected_flavor}" \
      --goos "${goos}" \
      --goarch "${goarch}" \
      --output "${payload_dir}/${report_name}" >/dev/null

    if [[ "${include_cloudflared}" == "true" ]]; then
      if ! env \
        GOPROXY="${GOPROXY:-https://proxy.golang.org}" \
        GOMODCACHE="${go_mod_cache_dir}" \
        "${SCRIPT_DIR}/build_cloudflared.sh" \
          --goos "${goos}" \
          --goarch "${goarch}" \
          --manifest "${cloudflared_manifest}" \
          --output "${payload_dir}/cloudflared${extension}"; then
        die "companion build failed for ${selected_flavor} ${platform}"
      fi
      cp "${cloudflared_manifest}" "${payload_dir}/cloudflared-manifest.json"
    fi
  done

  raw_path="${raw_dir}/${selected_flavor}.spdx.json"
  augmented_path="${raw_dir}/${selected_flavor}.augmented.spdx.json"
  normalized_path="${generated_dir}/${document_name}"
  if ! env \
    SYFT_CHECK_FOR_APP_UPDATE=false \
    SYFT_CACHE_DIR="${syft_cache_dir}" \
    "${syft_bin}" \
    "dir:${stable_root}" \
    --parallelism 1 \
    --base-path "${stable_root}" \
    --source-name "${source_name}" \
    --source-version baseline \
    -o "spdx-json=${raw_path}"; then
    die "Syft failed for ${selected_flavor} baseline"
  fi
  is_spdx_23 "${raw_path}" ||
    die "Syft output is not SPDX 2.3 for ${selected_flavor}"

  "${SCRIPT_DIR}/augment_sbom_files.sh" \
    --input "${raw_path}" \
    --staged-dir "${stable_root}" \
    --output "${augmented_path}" >/dev/null

  SBOM_NORMALIZE_ROOT="${stable_root}" \
    "${SCRIPT_DIR}/normalize_sbom.sh" \
      --input "${augmented_path}" \
      --output "${normalized_path}" >/dev/null
  is_spdx_23 "${normalized_path}" ||
    die "normalized output is not SPDX 2.3 for ${selected_flavor}"
}

verify_subset_contracts() {
  python3 - \
    "${generated_dir}" \
    "${COMPLIANCE_DIR}" \
    "${COMPLIANCE_DIR}/oss-license-report-cloudflared-binary.txt" <<'PY'
import json
import pathlib
import sys
import urllib.parse

generated_dir = pathlib.Path(sys.argv[1])
compliance_dir = pathlib.Path(sys.argv[2])
companion_report = pathlib.Path(sys.argv[3])


def document(name: str) -> pathlib.Path:
    generated = generated_dir / name
    if generated.is_file():
        return generated
    checked_in = compliance_dir / name
    if checked_in.is_file():
        return checked_in
    raise SystemExit(f"missing SBOM document for subset check: {name}")


def go_purl_names(path: pathlib.Path) -> set[str]:
    with path.open(encoding="utf-8") as source:
        payload = json.load(source)
    names = set()
    for package in payload.get("packages", []):
        for reference in package.get("externalRefs", []):
            locator = reference.get("referenceLocator", "")
            if reference.get("referenceType") != "purl" or not locator.startswith("pkg:golang/"):
                continue
            name = locator.removeprefix("pkg:golang/").split("@", 1)[0]
            names.add(urllib.parse.unquote(name))
    return names


def report_dependencies(path: pathlib.Path) -> set[str]:
    dependencies = set()
    in_table = False
    for line in path.read_text(encoding="utf-8").splitlines():
        if line == "DEPENDENCY|TYPE|VERSION|LICENSES|LICENSE_FILE|PLATFORMS":
            in_table = True
            continue
        if in_table and line:
            fields = line.split("|")
            if len(fields) != 6:
                raise SystemExit(f"malformed license row in {path}: {line!r}")
            dependency, _, _, _, license_file, _ = fields
            dependencies.add(dependency)
            if "@" in license_file:
                dependencies.add(license_file.split("@", 1)[0])
    return dependencies


client = go_purl_names(document("tunnel-client.spdx.json"))
runtime = go_purl_names(document("tunnel-client-runtime.spdx.json"))
runtime_cloudflared = go_purl_names(
    document("tunnel-client-runtime-cloudflared.spdx.json")
)
approved_companion = report_dependencies(companion_report)

if not runtime <= client:
    missing = ", ".join(sorted(runtime - client))
    raise SystemExit("runtime SBOM dependencies are not a subset of client: " + missing)
if not runtime_cloudflared <= client:
    missing = ", ".join(sorted(runtime_cloudflared - client))
    raise SystemExit(
        "runtime-cloudflared SBOM dependencies are not a subset of client: " + missing
    )

companion_delta = runtime_cloudflared - runtime
unapproved = companion_delta - approved_companion
if unapproved:
    raise SystemExit(
        "runtime-cloudflared SBOM delta is outside the pinned companion report: "
        + ", ".join(sorted(unapproved))
    )
PY
}

if [[ -n "${selected_flavor}" ]]; then
  build_baseline "${selected_flavor}"
  verify_subset_contracts

  selected_document=""
  case "${selected_flavor}" in
    client) selected_document="tunnel-client.spdx.json" ;;
    runtime) selected_document="tunnel-client-runtime.spdx.json" ;;
    runtime-cloudflared) selected_document="tunnel-client-runtime-cloudflared.spdx.json" ;;
  esac
  [[ -f "${COMPLIANCE_DIR}/${selected_document}" ]] ||
    die "baseline is missing: compliance/${selected_document}"
  if ! diff -u "${COMPLIANCE_DIR}/${selected_document}" "${generated_dir}/${selected_document}"; then
    die "baseline is stale: compliance/${selected_document}"
  fi

  python3 - \
    "${COMPLIANCE_DIR}/sbom-baseline-manifest.json" \
    "${COMPLIANCE_DIR}" \
    "${SYFT_VERSION}" \
    "${required_go_version}" \
    "${cloudflared_manifest_sha}" <<'PY'
import hashlib
import json
import pathlib
import sys

manifest_path, compliance_arg, syft_version, go_version, cloudflared_sha = sys.argv[1:]
compliance_dir = pathlib.Path(compliance_arg)
with pathlib.Path(manifest_path).open(encoding="utf-8") as source:
    manifest = json.load(source)

if manifest.get("schemaVersion") != 1:
    raise SystemExit("SBOM baseline manifest schema version mismatch")
if manifest.get("syftVersion") != syft_version:
    raise SystemExit("SBOM baseline manifest Syft version mismatch")
if manifest.get("goVersion") != go_version:
    raise SystemExit("SBOM baseline manifest Go version mismatch")
if manifest.get("cloudflaredManifestSHA256") != cloudflared_sha:
    raise SystemExit("SBOM baseline manifest cloudflared checksum mismatch")

documents = manifest.get("documents")
if not isinstance(documents, dict):
    raise SystemExit("SBOM baseline manifest has no documents")
for name in (
    "tunnel-client.spdx.json",
    "tunnel-client-runtime.spdx.json",
    "tunnel-client-runtime-cloudflared.spdx.json",
):
    entry = documents.get(name)
    if not isinstance(entry, dict) or not isinstance(entry.get("sha256"), str):
        raise SystemExit(f"SBOM baseline manifest is missing checksum for {name}")
    digest = hashlib.sha256((compliance_dir / name).read_bytes()).hexdigest()
    if digest != entry["sha256"]:
        raise SystemExit(f"SBOM baseline manifest checksum mismatch for {name}")
PY

  if [[ -n "${requested_output_dir}" ]]; then
    mkdir -p "${requested_output_dir}"
    cp "${generated_dir}/${selected_document}" "${requested_output_dir}/${selected_document}"
  fi
  printf 'SBOM baseline for %s is up to date\n' "${selected_flavor}"
  exit 0
fi

build_baseline client
build_baseline runtime
build_baseline runtime-cloudflared
verify_subset_contracts

client_sha="$(sha256_file "${generated_dir}/tunnel-client.spdx.json")"
runtime_sha="$(sha256_file "${generated_dir}/tunnel-client-runtime.spdx.json")"
runtime_cloudflared_sha="$(sha256_file "${generated_dir}/tunnel-client-runtime-cloudflared.spdx.json")"

python3 - \
  "${generated_dir}/sbom-baseline-manifest.json" \
  "${SYFT_VERSION}" \
  "${required_go_version}" \
  "${cloudflared_manifest_sha}" \
  "${client_sha}" \
  "${runtime_sha}" \
  "${runtime_cloudflared_sha}" <<'PY'
import json
import pathlib
import sys

(
    output_path,
    syft_version,
    go_version,
    cloudflared_manifest_sha,
    client_sha,
    runtime_sha,
    runtime_cloudflared_sha,
) = sys.argv[1:]
manifest = {
    "schemaVersion": 1,
    "syftVersion": syft_version,
    "goVersion": go_version,
    "environment": {
        "LC_ALL": "C",
        "TZ": "UTC",
        "SOURCE_DATE_EPOCH": 0,
    },
    "platforms": [
        "linux/amd64",
        "linux/arm64",
        "darwin/amd64",
        "darwin/arm64",
        "windows/amd64",
        "windows/arm64",
    ],
    "buildMetadata": {
        "semanticVersion": "0.0.0-baseline",
        "gitSHA": "baseline",
        "cgoEnabled": "0",
        "flags": ["-trimpath", "-buildvcs=false"],
        "flavors": ["full", "runtime", "runtime-cloudflared"],
    },
    "cloudflaredManifestSHA256": cloudflared_manifest_sha,
    "baselineScope": "canonical six-platform staged payload union",
    "documents": {
        "tunnel-client.spdx.json": {"sha256": client_sha},
        "tunnel-client-runtime.spdx.json": {"sha256": runtime_sha},
        "tunnel-client-runtime-cloudflared.spdx.json": {
            "sha256": runtime_cloudflared_sha
        },
    },
}
pathlib.Path(output_path).write_text(
    json.dumps(manifest, indent=2, ensure_ascii=False) + "\n",
    encoding="utf-8",
)
PY

if [[ "${mode}" == "write" ]]; then
  destination_dir="${requested_output_dir:-${COMPLIANCE_DIR}}"
  mkdir -p "${destination_dir}"
  for file in "${BASELINE_FILES[@]}"; do
    cp "${generated_dir}/${file}" "${destination_dir}/${file}"
  done
  printf 'wrote SBOM baselines to %s\n' "${destination_dir}"
  exit 0
fi

for file in "${BASELINE_FILES[@]}"; do
  [[ -f "${COMPLIANCE_DIR}/${file}" ]] ||
    die "baseline is missing: compliance/${file}"
  if ! diff -u "${COMPLIANCE_DIR}/${file}" "${generated_dir}/${file}"; then
    die "baseline is stale: compliance/${file}"
  fi
done

if [[ -n "${requested_output_dir}" ]]; then
  mkdir -p "${requested_output_dir}"
  for file in "${BASELINE_FILES[@]}"; do
    cp "${generated_dir}/${file}" "${requested_output_dir}/${file}"
  done
fi

printf 'SBOM baselines are up to date\n'
