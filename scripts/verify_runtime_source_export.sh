#!/usr/bin/env bash
set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
if [[ -n "${TEST_SRCDIR:-}" && -n "${TEST_WORKSPACE:-}" &&
  -f "${TEST_SRCDIR}/${TEST_WORKSPACE}/api/tunnel-client/scripts/runtime_runfiles.sh" ]]; then
  SCRIPT_DIR="${TEST_SRCDIR}/${TEST_WORKSPACE}/api/tunnel-client/scripts"
fi
# shellcheck source=runtime_runfiles.sh
source "${SCRIPT_DIR}/runtime_runfiles.sh"
materialize_tunnel_client_runfiles "${SCRIPT_DIR}" || exit 1
SCRIPT_DIR="${RUNTIME_SCRIPT_DIR}"
PROJECT_ROOT="${RUNTIME_PROJECT_ROOT}"
readonly SCRIPT_DIR
readonly PROJECT_ROOT
trap runtime_runfiles_cleanup EXIT
use_tunnel_client_bazel_go_sdk || exit 1
readonly -a PLATFORMS=(
  "linux/amd64"
  "linux/arm64"
  "darwin/amd64"
  "darwin/arm64"
  "windows/amd64"
  "windows/arm64"
)
readonly METADATA_DIR="SOURCE_EXPORT_METADATA"

usage() {
  cat <<'EOF'
Usage:
  ./scripts/verify_runtime_source_export.sh \
    --flavor runtime|runtime-cloudflared \
    --archive <path-to-tar.gz> \
    [--platform <goos>/<goarch>]

Checks that a source export contains only its recorded rebuild inputs, that
its dependency manifests match the current source tree, and that every
release platform can rebuild from the archive by default. With --platform,
the proof is restricted to one explicit release platform; a full archive is
also accepted so Bazel can share one deterministic producer across leaves.
EOF
}

die() {
  echo "verify_runtime_source_export.sh: $*" >&2
  exit 1
}

[[ -f "${PROJECT_ROOT}/go.mod" ]] ||
  die "project root is missing go.mod: ${PROJECT_ROOT}"
[[ -x "${SCRIPT_DIR}/check_runtime_boundary.sh" ]] ||
  die "script runfile is missing: ${SCRIPT_DIR}/check_runtime_boundary.sh"

flavor=""
archive=""
platform=""
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
    --platform)
      platform="${2:-}"
      shift 2
      ;;
    --platform=*)
      platform="${1#*=}"
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

case "${flavor}" in
  runtime|runtime-cloudflared) ;;
  *) die "--flavor must be runtime or runtime-cloudflared" ;;
esac
case "${platform}" in
  ""|linux/amd64|linux/arm64|darwin/amd64|darwin/arm64|windows/amd64|windows/arm64) ;;
  *) die "--platform must be one supported goos/goarch release platform" ;;
esac

selected_platforms=("${PLATFORMS[@]}")
if [[ -n "${platform}" ]]; then
  selected_platforms=("${platform}")
fi

public_build_scripts=("scripts/rebuild_runtime_from_source.sh")
required_manifests=()
if [[ "${flavor}" == "runtime-cloudflared" ]]; then
  public_build_scripts+=("scripts/build_cloudflared.sh")
  required_manifests+=("pkg/cloudflared/runtime/manifest.json")
fi
[[ -n "${archive}" ]] || die "--archive is required"
[[ -f "${archive}" ]] || die "archive does not exist: ${archive}"

command -v go >/dev/null 2>&1 || die "go is required"
if [[ -z "${TUNNEL_CLIENT_RUNTIME_PYTHON:-}" ]]; then
  command -v python3 >/dev/null 2>&1 || die "python3 is required"
fi
command -v tar >/dev/null 2>&1 || die "tar is required"

cd "${PROJECT_ROOT}"
readonly GO_CACHE_DIR="${GOCACHE:-${TMPDIR:-/tmp}/tunnel-client-runtime-go-cache}"
readonly GO_MOD_CACHE_DIR="${GOMODCACHE:-${TMPDIR:-/tmp}/tunnel-client-runtime-go-mod-cache}"
mkdir -p "${GO_CACHE_DIR}" "${GO_MOD_CACHE_DIR}"

target=""
binary_name=""
case "${flavor}" in
  runtime)
    target="./cmd/client-runtime"
    binary_name="tunnel-client-runtime"
    ;;
  runtime-cloudflared)
    target="./cmd/client-runtime-cloudflared"
    binary_name="tunnel-client-runtime-cloudflared"
    ;;
esac

boundary_args=(--flavor "${flavor}")
if [[ -n "${platform}" ]]; then
  boundary_args+=(--platform "${platform}")
fi
"${SCRIPT_DIR}/check_runtime_boundary.sh" "${boundary_args[@]}"

if runtime_is_bazel_test; then
  tmp_dir="$(mktemp -d "${TEST_TMPDIR}/tunnel-client-runtime-source-verify.XXXXXX")"
else
  tmp_dir="$(mktemp -d)"
fi
trap 'rm -rf "${tmp_dir}"; runtime_runfiles_cleanup' EXIT

member_list="${tmp_dir}/members.txt"
tar -tzf "${archive}" >"${member_list}"
[[ -s "${member_list}" ]] || die "archive is empty"
if LC_ALL=C grep -E '(^/|(^|/)\.\.(/|$))' "${member_list}" >/dev/null; then
  die "archive contains an unsafe path"
fi

tar -xzf "${archive}" -C "${tmp_dir}"
roots=()
while IFS= read -r root; do
  roots+=("${root}")
done < <(find "${tmp_dir}" -mindepth 1 -maxdepth 1 -type d -print | LC_ALL=C sort)
[[ "${#roots[@]}" -eq 1 ]] || die "archive must contain exactly one root directory"
archive_root="${roots[0]}"
metadata_root="${archive_root}/${METADATA_DIR}"
manifest_path="${metadata_root}/manifest.json"
file_manifest="${metadata_root}/files.txt"
[[ -f "${manifest_path}" ]] || die "archive is missing ${METADATA_DIR}/manifest.json"
[[ -f "${file_manifest}" ]] || die "archive is missing ${METADATA_DIR}/files.txt"
manifest_fields="$(
  runtime_python - "${manifest_path}" "${flavor}" "${selected_platforms[@]}" <<'PY'
import json
import pathlib
import sys

manifest_path = pathlib.Path(sys.argv[1])
flavor = sys.argv[2]
expected_platforms = sys.argv[3:]


def die(message: str) -> None:
    print(f"verify_runtime_source_export.sh: {message}", file=sys.stderr)
    raise SystemExit(1)


try:
    manifest = json.loads(manifest_path.read_text(encoding="utf-8"))
except (OSError, json.JSONDecodeError):
    die("archive manifest is not valid JSON")

if not isinstance(manifest, dict):
    die("archive manifest does not match --flavor")
schema_version = manifest.get("schemaVersion")
if (
    type(schema_version) is not int
    or schema_version != 1
    or manifest.get("flavor") != flavor
):
    die("archive manifest does not match --flavor")
if not (
    isinstance(manifest.get("includedFiles"), list)
    and len(manifest["includedFiles"]) > 0
    and isinstance(manifest.get("dependencyPackages"), dict)
    and isinstance(manifest.get("excludedDirectoryAssertions"), list)
    and len(manifest["excludedDirectoryAssertions"]) > 0
):
    die("archive manifest is missing source-boundary evidence")
supported_platforms = [
    "linux/amd64",
    "linux/arm64",
    "darwin/amd64",
    "darwin/arm64",
    "windows/amd64",
    "windows/arm64",
]
recorded_platforms = manifest.get("platforms")
if (
    not isinstance(recorded_platforms, list)
    or recorded_platforms
    != [platform for platform in supported_platforms if platform in recorded_platforms]
):
    die("archive manifest platforms are not a supported canonical selection")
if len(expected_platforms) == 1:
    if expected_platforms[0] not in recorded_platforms:
        die("archive manifest does not contain the requested platform")
elif recorded_platforms != expected_platforms:
    die("archive manifest platforms do not match the requested selection")
if set(manifest["dependencyPackages"]) != set(recorded_platforms):
    die("archive manifest dependencyPackages do not match its platforms")

expected_build_scripts = ["scripts/rebuild_runtime_from_source.sh"]
expected_manifests = []
if flavor == "runtime-cloudflared":
    expected_build_scripts.append("scripts/build_cloudflared.sh")
    expected_manifests.append("pkg/cloudflared/runtime/manifest.json")
if (
    manifest.get("publicBuildScripts") != expected_build_scripts
    or manifest.get("requiredManifests") != expected_manifests
):
    die("archive manifest is missing the required public rebuild inputs")

module_path = manifest.get("modulePath")
if not isinstance(module_path, str) or not module_path:
    die("archive manifest is missing modulePath")
source_commit = manifest.get("sourceCommit")
if not isinstance(source_commit, str):
    source_commit = ""

print(module_path)
print(source_commit)
PY
)" || exit 1
module_path="${manifest_fields%%$'\n'*}"
recorded_commit="${manifest_fields#*$'\n'}"

current_commit=""
if [[ -n "${TEST_SRCDIR:-}" && -n "${TEST_WORKSPACE:-}" && -n "${SOURCE_COMMIT:-}" ]]; then
  current_commit="${SOURCE_COMMIT}"
else
  current_commit="$(git rev-parse HEAD 2>/dev/null)" ||
    die "could not determine the current source commit"
fi
[[ "${current_commit}" =~ ^[0-9a-f]{40}$ ]] ||
  die "current source commit must be a full Git SHA"
[[ "${recorded_commit}" == "${current_commit}" ]] ||
  die "archive source commit does not match the current source commit"

required_archive_files=(
  go.mod
  go.sum
  LICENSE
  NOTICE
  "${public_build_scripts[@]}"
)
if [[ "${#required_manifests[@]}" -gt 0 ]]; then
  required_archive_files+=("${required_manifests[@]}")
fi
for required_file in "${required_archive_files[@]}"; do
  [[ -f "${archive_root}/${required_file}" ]] ||
    die "archive is missing required file: ${required_file}"
done

if [[ "${flavor}" == "runtime-cloudflared" ]]; then
  [[ ! -e "${archive_root}/pkg/cloudflared/manifest.json" ]] ||
    die "archive unexpectedly contains the non-runtime cloudflared manifest"
  if ! "${archive_root}/scripts/build_cloudflared.sh" \
    --goos linux \
    --goarch amd64 \
    --describe >/dev/null; then
    die "exported build_cloudflared.sh default manifest lookup failed"
  fi
fi

actual_payload_files="${tmp_dir}/actual-payload-files.txt"
(
  cd "${archive_root}"
  find . -type f -print |
    sed 's#^\./##' |
    LC_ALL=C sort |
    grep -v "^${METADATA_DIR}/" || true
) >"${actual_payload_files}"
if ! diff -u "${file_manifest}" "${actual_payload_files}"; then
  die "archive payload does not match its recorded file list"
fi

runtime_python - "${archive_root}" "${manifest_path}" "${file_manifest}" <<'PY'
import hashlib
import json
import pathlib
import sys

archive_root = pathlib.Path(sys.argv[1])
manifest = json.loads(pathlib.Path(sys.argv[2]).read_text(encoding="utf-8"))
file_manifest = [
    line.strip()
    for line in pathlib.Path(sys.argv[3]).read_text(encoding="utf-8").splitlines()
    if line.strip()
]
included_files = manifest.get("includedFiles", [])
recorded_paths = [entry.get("path") for entry in included_files]
if recorded_paths != file_manifest:
    raise SystemExit("archive manifest includedFiles does not match files.txt")
for entry in included_files:
    relative_path = entry.get("path")
    expected_digest = entry.get("sha256")
    if not isinstance(relative_path, str) or not isinstance(expected_digest, str):
        raise SystemExit("archive manifest contains an invalid includedFiles entry")
    actual_digest = hashlib.sha256((archive_root / relative_path).read_bytes()).hexdigest()
    if actual_digest != expected_digest:
        raise SystemExit(f"archive file digest mismatch: {relative_path}")
PY

# The published source archive intentionally remains first-party-only. Bazel
# tests inject the separately declared, checksum-verified vendor snapshot only
# after archive bytes and manifests have been verified, so the rebuild proof is
# offline without changing the public archive contract.
runtime_stage_vendor_into_root "${archive_root}" || exit 1

reject_export_path() {
  local relative_path="$1"
  case "${relative_path}" in
    *_test.go|pkg/app|pkg/app/*|pkg/config|pkg/config/*|pkg/health|pkg/health/*|pkg/harpoon|pkg/harpoon/*|pkg/proxyhealth|pkg/proxyhealth/*|cmd/client|cmd/client/*)
      return 0
      ;;
  esac
  if [[ "${relative_path}" =~ (^|/)(adminui|plugins|localproxy|docs|examples|e2e|tests|testdata|testsupport)(/|$) ]]; then
    return 0
  fi
  if [[ "${relative_path}" =~ (^|/)codex[^/]*(/|$) ]]; then
    return 0
  fi
  if [[ "${relative_path}" =~ ^pkg/cloudflared(/|$) ]]; then
    if [[ "${flavor}" == "runtime-cloudflared" &&
      ( "${relative_path}" == "pkg/cloudflared/runtime" ||
        "${relative_path}" == pkg/cloudflared/runtime/* ) ]]; then
      return 1
    fi
    return 0
  fi
  return 1
}

while IFS= read -r relative_path; do
  [[ -n "${relative_path}" ]] || continue
  if reject_export_path "${relative_path}"; then
    die "archive contains excluded path: ${relative_path}"
  fi
done <"${file_manifest}"

go_list_json_records() {
  local json_path="$1"
  local selected_module_path="$2"
  local output_kind="$3"
  runtime_python - "${json_path}" "${selected_module_path}" "${output_kind}" <<'PY'
import json
import pathlib
import sys

json_path = pathlib.Path(sys.argv[1])
module_path = sys.argv[2]
output_kind = sys.argv[3]
source_file_fields = (
    "GoFiles",
    "CgoFiles",
    "CFiles",
    "CXXFiles",
    "HFiles",
    "SFiles",
    "SwigFiles",
    "SysoFiles",
    "EmbedFiles",
)


def records():
    payload = json_path.read_text(encoding="utf-8")
    decoder = json.JSONDecoder()
    offset = 0
    while offset < len(payload):
        while offset < len(payload) and payload[offset].isspace():
            offset += 1
        if offset == len(payload):
            return
        record, offset = decoder.raw_decode(payload, offset)
        yield record


def source_files(record, field):
    value = record.get(field)
    if value is None or value is False:
        return []
    if not isinstance(value, list) or not all(isinstance(item, str) for item in value):
        raise SystemExit(f"go list record has invalid {field}")
    return value


for record in records():
    if not isinstance(record, dict):
        raise SystemExit("go list output record is not an object")
    module = record.get("Module")
    if not isinstance(module, dict) or module.get("Path") != module_path:
        continue
    if output_kind == "packages":
        import_path = record.get("ImportPath")
        if not isinstance(import_path, str):
            raise SystemExit("first-party go list record is missing ImportPath")
        prefix = module_path + "/"
        if import_path == module_path:
            print(".")
        elif import_path.startswith(prefix):
            print(import_path[len(prefix) :])
        else:
            print(import_path)
        continue
    if output_kind != "source-files":
        raise SystemExit(f"unknown go list output kind: {output_kind}")
    directory = record.get("Dir")
    if not isinstance(directory, str):
        raise SystemExit("first-party go list record is missing Dir")
    for field in source_file_fields:
        for filename in source_files(record, field):
            print(f"{directory}/{filename}")
PY
}

relative_package_manifest() {
  local root="$1"
  local selected_target="$2"
  local goos="$3"
  local goarch="$4"
  local output_path="$5"
  local selected_files_output="${6:-}"
  local module_path json_path absolute_path relative_path go_mod_flag

  root="$(cd "${root}" && pwd)"
  go_mod_flag="$(runtime_go_mod_flag_for_root "${root}")"
  module_path="$(cd "${root}" && env GOWORK=off GOCACHE="${GO_CACHE_DIR}" GOMODCACHE="${GO_MOD_CACHE_DIR}" go list -m -f '{{.Path}}')"
  [[ -n "${module_path}" ]] || die "could not determine the Go module path under ${root}"
  json_path="${tmp_dir}/$(basename "${root}")-${goos}_${goarch}.json"
  if ! (
    cd "${root}"
    env \
      GOWORK=off \
      GOCACHE="${GO_CACHE_DIR}" \
      GOMODCACHE="${GO_MOD_CACHE_DIR}" \
      GOOS="${goos}" \
      GOARCH="${goarch}" \
      CGO_ENABLED=0 \
      go list -buildvcs=false "${go_mod_flag}" -deps -json "${selected_target}"
  ) >"${json_path}"; then
    die "dependency listing failed under ${root} for ${goos}/${goarch}"
  fi

  go_list_json_records "${json_path}" "${module_path}" packages |
    LC_ALL=C sort -u >"${output_path}"

  if [[ -n "${selected_files_output}" ]]; then
    go_list_json_records "${json_path}" "${module_path}" source-files |
      while IFS= read -r absolute_path; do
        [[ -n "${absolute_path}" ]] || continue
        case "${absolute_path}" in
          "${root}"/*)
            relative_path="${absolute_path#"${root}/"}"
            [[ -f "${root}/${relative_path}" ]] ||
              die "selected exported source file is missing: ${relative_path}"
            printf '%s\n' "${relative_path}" >>"${selected_files_output}"
            ;;
          *)
            die "selected exported source file is outside the archive root: ${absolute_path}"
            ;;
        esac
      done
  fi
}

build_root="${tmp_dir}/build"
mkdir -p "${build_root}"
exported_selected_files="${tmp_dir}/exported-selected-files.txt"
exported_required_files=(
  go.mod
  go.sum
  LICENSE
  NOTICE
  "${public_build_scripts[@]}"
)
if [[ "${#required_manifests[@]}" -gt 0 ]]; then
  exported_required_files+=("${required_manifests[@]}")
fi
printf '%s\n' "${exported_required_files[@]}" >"${exported_selected_files}"

for platform in "${selected_platforms[@]}"; do
  goos="${platform%/*}"
  goarch="${platform#*/}"
  expected_manifest="${metadata_root}/dependencies/${goos}_${goarch}.txt"
  [[ -f "${expected_manifest}" ]] ||
    die "archive is missing dependency manifest for ${platform}"

  current_manifest="${tmp_dir}/current-${goos}_${goarch}.txt"
  exported_manifest="${tmp_dir}/exported-${goos}_${goarch}.txt"
  relative_package_manifest "${PROJECT_ROOT}" "${target}" "${goos}" "${goarch}" "${current_manifest}"
  relative_package_manifest "${archive_root}" "${target}" "${goos}" "${goarch}" "${exported_manifest}" "${exported_selected_files}"

  if ! diff -u "${expected_manifest}" "${current_manifest}"; then
    die "recorded dependency manifest differs from current source for ${platform}"
  fi
  if ! diff -u "${expected_manifest}" "${exported_manifest}"; then
    die "recorded dependency manifest differs from exported source for ${platform}"
  fi

  output_dir="${build_root}/${goos}_${goarch}"
  mkdir -p "${output_dir}"
  output_path="${output_dir}/${binary_name}"
  if [[ "${goos}" == "windows" ]]; then
    output_path="${output_path}.exe"
  fi
  if ! env \
    GOCACHE="${GO_CACHE_DIR}" \
    GOMODCACHE="${GO_MOD_CACHE_DIR}" \
    "${archive_root}/scripts/rebuild_runtime_from_source.sh" \
      --flavor "${flavor}" \
      --platform "${platform}" \
      --output "${output_path}"; then
    die "exported source rebuild failed for ${platform}"
  fi

  printf '%s source export: %s passed\n' "${flavor}" "${platform}"
done

LC_ALL=C sort -u "${exported_selected_files}" -o "${exported_selected_files}"
if [[ -n "${platform}" ]]; then
  while IFS= read -r relative_path; do
    [[ -n "${relative_path}" ]] || continue
    if ! grep -Fqx -- "${relative_path}" "${file_manifest}"; then
      die "archive is missing selected rebuild input: ${relative_path}"
    fi
  done <"${exported_selected_files}"
elif ! diff -u "${file_manifest}" "${exported_selected_files}"; then
  die "archive contains files outside the selected rebuild closure"
fi
