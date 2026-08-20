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
readonly -a COMMON_PUBLIC_BUILD_SCRIPTS=(
  "scripts/rebuild_runtime_from_source.sh"
)

usage() {
  cat <<'EOF'
Usage:
  ./scripts/build_runtime_source_export.sh \
    --flavor runtime|runtime-cloudflared \
    --version <vX.Y.Z-or-dev-version> \
    --output-dir <directory>

Creates a deterministic source archive containing the first-party files,
public rebuild helpers, and required manifests needed to rebuild one runtime
flavor for every supported release platform.
EOF
}

die() {
  echo "build_runtime_source_export.sh: $*" >&2
  exit 1
}

[[ -f "${PROJECT_ROOT}/go.mod" ]] ||
  die "project root is missing go.mod: ${PROJECT_ROOT}"
[[ -x "${SCRIPT_DIR}/check_runtime_boundary.sh" ]] ||
  die "script runfile is missing: ${SCRIPT_DIR}/check_runtime_boundary.sh"

flavor=""
version=""
output_dir=""
while [[ $# -gt 0 ]]; do
  case "$1" in
    --flavor)
      flavor="${2:-}"
      shift 2
      ;;
    --version)
      version="${2:-}"
      shift 2
      ;;
    --output-dir)
      output_dir="${2:-}"
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

public_build_scripts=("${COMMON_PUBLIC_BUILD_SCRIPTS[@]}")
required_manifests=()
if [[ "${flavor}" == "runtime-cloudflared" ]]; then
  public_build_scripts+=("scripts/build_cloudflared.sh")
  required_manifests+=("pkg/cloudflared/runtime/manifest.json")
fi
[[ "${version}" =~ ^v[0-9]+\.[0-9]+\.[0-9]+([.-][0-9A-Za-z][0-9A-Za-z.-]*)?$ ]] ||
  die "--version must be a v-prefixed semantic version or development version"
[[ -n "${output_dir}" ]] || die "--output-dir is required"

command -v go >/dev/null 2>&1 || die "go is required"
if [[ -z "${TUNNEL_CLIENT_RUNTIME_PYTHON:-}" ]]; then
  command -v python3 >/dev/null 2>&1 || die "python3 is required"
fi
command -v gzip >/dev/null 2>&1 || die "gzip is required"

cd "${PROJECT_ROOT}"
readonly GO_CACHE_DIR="${GOCACHE:-${TMPDIR:-/tmp}/tunnel-client-runtime-go-cache}"
readonly GO_MOD_CACHE_DIR="${GOMODCACHE:-${TMPDIR:-/tmp}/tunnel-client-runtime-go-mod-cache}"
mkdir -p "${GO_CACHE_DIR}" "${GO_MOD_CACHE_DIR}"
readonly GO_MOD_FLAG="$(runtime_go_mod_flag_for_root "${PROJECT_ROOT}")"

target=""
archive_stem=""
case "${flavor}" in
  runtime)
    target="./cmd/client-runtime"
    archive_stem="tunnel-client-runtime-source-${version}"
    ;;
  runtime-cloudflared)
    target="./cmd/client-runtime-cloudflared"
    archive_stem="tunnel-client-runtime-cloudflared-source-${version}"
    ;;
esac

"${SCRIPT_DIR}/check_runtime_boundary.sh" --flavor "${flavor}"

readonly MODULE_PATH="$(env GOWORK=off GOCACHE="${GO_CACHE_DIR}" GOMODCACHE="${GO_MOD_CACHE_DIR}" go list -m -f '{{.Path}}')"
[[ -n "${MODULE_PATH}" ]] || die "could not determine the Go module path"
readonly GO_VERSION="$(go version)"
source_commit=""
if [[ -n "${TEST_SRCDIR:-}" && -n "${TEST_WORKSPACE:-}" && -n "${SOURCE_COMMIT:-}" ]]; then
  source_commit="${SOURCE_COMMIT}"
else
  source_commit="$(git rev-parse HEAD 2>/dev/null)" ||
    die "could not determine the source commit"
fi
[[ "${source_commit}" =~ ^[0-9a-f]{40}$ ]] ||
  die "source commit must be a full Git SHA"
readonly SOURCE_COMMIT="${source_commit}"
source_date_epoch="${SOURCE_DATE_EPOCH:-}"
if [[ -z "${source_date_epoch}" ]]; then
  source_date_epoch="$(git show -s --format=%ct "${SOURCE_COMMIT}" 2>/dev/null)" ||
    die "could not determine the source date epoch"
fi
readonly SOURCE_DATE_EPOCH="${source_date_epoch}"
[[ "${SOURCE_DATE_EPOCH}" =~ ^[0-9]+$ ]] ||
  die "SOURCE_DATE_EPOCH must be a non-negative integer"

if runtime_is_bazel_test; then
  tmp_dir="$(mktemp -d "${TEST_TMPDIR}/tunnel-client-runtime-source-build.XXXXXX")"
else
  tmp_dir="$(mktemp -d)"
fi
trap 'rm -rf "${tmp_dir}"; runtime_runfiles_cleanup' EXIT

archive_root="${tmp_dir}/${archive_stem}"
metadata_root="${archive_root}/${METADATA_DIR}"
dependency_root="${metadata_root}/dependencies"
mkdir -p "${dependency_root}"

source_files="${tmp_dir}/source-files.txt"
: >"${source_files}"
required_source_files=(
  go.mod
  go.sum
  LICENSE
  NOTICE
  "${public_build_scripts[@]}"
)
if [[ "${#required_manifests[@]}" -gt 0 ]]; then
  required_source_files+=("${required_manifests[@]}")
fi
for required_file in "${required_source_files[@]}"; do
  [[ -f "${required_file}" ]] || die "required source file is missing: ${required_file}"
  printf '%s\n' "${required_file}" >>"${source_files}"
done

go_list_json_records() {
  local json_path="$1"
  local output_kind="$2"
  runtime_python - "${json_path}" "${MODULE_PATH}" "${output_kind}" <<'PY'
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
  local json_path="$1"
  local output_path="$2"
  go_list_json_records "${json_path}" packages |
    LC_ALL=C sort -u >"${output_path}"
}

append_selected_source_files() {
  local json_path="$1"
  local absolute_path relative_path
  go_list_json_records "${json_path}" source-files |
    while IFS= read -r absolute_path; do
      [[ -n "${absolute_path}" ]] || continue
      case "${absolute_path}" in
        "${PROJECT_ROOT}"/*)
          relative_path="${absolute_path#"${PROJECT_ROOT}/"}"
          [[ -f "${relative_path}" ]] ||
            die "selected source file is missing: ${relative_path}"
          printf '%s\n' "${relative_path}"
          ;;
        *)
          die "selected first-party source file is outside the project root: ${absolute_path}"
          ;;
      esac
    done
}

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

platform_json="${tmp_dir}/platform.json"
for platform in "${PLATFORMS[@]}"; do
  goos="${platform%/*}"
  goarch="${platform#*/}"
  manifest_path="${dependency_root}/${goos}_${goarch}.txt"

  if ! env \
    GOWORK=off \
    GOCACHE="${GO_CACHE_DIR}" \
    GOMODCACHE="${GO_MOD_CACHE_DIR}" \
    GOOS="${goos}" \
    GOARCH="${goarch}" \
    CGO_ENABLED=0 \
    go list -buildvcs=false "${GO_MOD_FLAG}" -deps -json "${target}" >"${platform_json}"; then
    die "${flavor} dependency listing failed for ${platform}"
  fi

  relative_package_manifest "${platform_json}" "${manifest_path}"
  append_selected_source_files "${platform_json}" >>"${source_files}"
done

LC_ALL=C sort -u "${source_files}" >"${metadata_root}/files.txt"

while IFS= read -r relative_path; do
  [[ -n "${relative_path}" ]] || continue
  if reject_export_path "${relative_path}"; then
    die "selected source file is outside the approved export boundary: ${relative_path}"
  fi
  destination_path="${archive_root}/${relative_path}"
  mkdir -p "$(dirname "${destination_path}")"
  cp -p "${relative_path}" "${destination_path}"
done <"${metadata_root}/files.txt"

runtime_python - \
  "${flavor}" \
  "${version}" \
  "${SOURCE_COMMIT}" \
  "${GO_VERSION}" \
  "${MODULE_PATH}" \
  "${SOURCE_DATE_EPOCH}" \
  "${archive_root}" \
  "${metadata_root}" <<'PY'
import hashlib
import json
import pathlib
import sys

(
    flavor,
    version,
    source_commit,
    go_version,
    module_path,
    source_date_epoch,
    archive_root_arg,
    metadata_root_arg,
) = sys.argv[1:]
archive_root = pathlib.Path(archive_root_arg)
metadata_root = pathlib.Path(metadata_root_arg)
platforms = [
    "linux/amd64",
    "linux/arm64",
    "darwin/amd64",
    "darwin/arm64",
    "windows/amd64",
    "windows/arm64",
]

selected_paths = [
    line.strip()
    for line in (metadata_root / "files.txt").read_text(encoding="utf-8").splitlines()
    if line.strip()
]
included_files = []
for relative_path in selected_paths:
    path = archive_root / relative_path
    digest = hashlib.sha256(path.read_bytes()).hexdigest()
    included_files.append({"path": relative_path, "sha256": digest})

dependency_packages = {}
for platform in platforms:
    goos, goarch = platform.split("/", 1)
    manifest_path = metadata_root / "dependencies" / f"{goos}_{goarch}.txt"
    dependency_packages[platform] = [
        line.strip()
        for line in manifest_path.read_text(encoding="utf-8").splitlines()
        if line.strip()
    ]

document = {
    "schemaVersion": 1,
    "flavor": flavor,
    "version": version,
    "sourceCommit": source_commit,
    "goVersion": go_version,
    "modulePath": module_path,
    "sourceDateEpoch": int(source_date_epoch),
    "platforms": platforms,
    "build": {
        "cgoEnabled": "0",
        "trimpath": True,
        "buildvcs": False,
    },
    "publicBuildScripts": ["scripts/rebuild_runtime_from_source.sh"],
    "requiredManifests": [],
    "includedFiles": included_files,
    "dependencyPackages": dependency_packages,
    "excludedDirectoryAssertions": [
        "adminui",
        "cmd/client",
        "docs",
        "e2e",
        "examples",
        "pkg/app",
        "pkg/config",
        "pkg/health",
        "pkg/harpoon",
        "pkg/localproxy",
        "pkg/plugins",
        "pkg/proxyhealth",
        "testsupport",
    ],
}
if flavor == "runtime":
    document["excludedDirectoryAssertions"].append("pkg/cloudflared")
else:
    document["publicBuildScripts"].append("scripts/build_cloudflared.sh")
    document["requiredManifests"].append("pkg/cloudflared/runtime/manifest.json")
    document["excludedDirectoryAssertions"].extend(
        ["pkg/cloudflared/configgen", "pkg/cloudflared (except runtime)"]
    )

(metadata_root / "manifest.json").write_text(
    json.dumps(document, indent=2, sort_keys=True) + "\n",
    encoding="utf-8",
)
PY

mkdir -p "${output_dir}"
output_dir="$(cd "${output_dir}" && pwd)"
tar_path="${tmp_dir}/${archive_stem}.tar"
archive_path="${output_dir}/${archive_stem}.tar.gz"

runtime_python - "${tmp_dir}" "${archive_stem}" "${tar_path}" "${SOURCE_DATE_EPOCH}" <<'PY'
import pathlib
import stat
import sys
import tarfile

parent = pathlib.Path(sys.argv[1])
root_name = sys.argv[2]
tar_path = pathlib.Path(sys.argv[3])
mtime = int(sys.argv[4])
root = parent / root_name

with tarfile.open(tar_path, "w", format=tarfile.PAX_FORMAT) as archive:
    paths = [root, *sorted(root.rglob("*"), key=lambda path: path.relative_to(parent).as_posix())]
    for path in paths:
        relative = path.relative_to(parent).as_posix()
        info = archive.gettarinfo(str(path), arcname=relative)
        info.uid = 0
        info.gid = 0
        info.uname = ""
        info.gname = ""
        info.mtime = mtime
        info.pax_headers = {}
        if path.is_file():
            mode = stat.S_IMODE(path.stat().st_mode)
            info.mode = 0o755 if mode & 0o111 else 0o644
            with path.open("rb") as source:
                archive.addfile(info, source)
        else:
            info.mode = 0o755
            archive.addfile(info)
PY

gzip -n -c "${tar_path}" >"${archive_path}"
printf 'wrote %s\n' "${archive_path}"
