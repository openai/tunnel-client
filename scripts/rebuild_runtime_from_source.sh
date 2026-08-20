#!/usr/bin/env bash
set -euo pipefail

readonly SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
readonly PROJECT_ROOT="$(cd "${SCRIPT_DIR}/.." && pwd)"
readonly METADATA_PATH="${PROJECT_ROOT}/SOURCE_EXPORT_METADATA/manifest.json"
readonly MODULE_PATH="github.com/openai/tunnel-client"

usage() {
  cat <<'EOF'
Usage:
  ./scripts/rebuild_runtime_from_source.sh \
    --flavor runtime|runtime-cloudflared \
    --goos <linux|darwin|windows> \
    --goarch <amd64|arm64> \
    --output <path>

Rebuilds one tunnel-client runtime binary from a published runtime source
archive. The archive manifest supplies the release version and source commit;
the build uses the same trimpath, buildvcs, and CGO settings as release
artifacts.
EOF
}

die() {
  echo "rebuild_runtime_from_source.sh: $*" >&2
  exit 1
}

runtime_python_bin="${TUNNEL_CLIENT_RUNTIME_PYTHON:-python3}"
runtime_python_home="${TUNNEL_CLIENT_RUNTIME_PYTHON_HOME:-}"
runtime_python() {
  if [[ -n "${runtime_python_home}" ]]; then
    PYTHONHOME="${runtime_python_home}" "${runtime_python_bin}" "$@"
  else
    "${runtime_python_bin}" "$@"
  fi
}

flavor=""
goos=""
goarch=""
output=""
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
case "${goos}" in
  linux|darwin|windows) ;;
  *) die "--goos must be linux, darwin, or windows" ;;
esac
case "${goarch}" in
  amd64|arm64) ;;
  *) die "--goarch must be amd64 or arm64" ;;
esac
[[ -n "${output}" ]] || die "--output is required"
[[ -f "${METADATA_PATH}" ]] ||
  die "source export manifest is missing: SOURCE_EXPORT_METADATA/manifest.json"
[[ -f "${PROJECT_ROOT}/go.mod" ]] || die "go.mod is missing"

command -v go >/dev/null 2>&1 || die "go is required"
if [[ "${runtime_python_bin}" == */* ]]; then
  [[ -x "${runtime_python_bin}" ]] || die "declared Python is not executable: ${runtime_python_bin}"
else
  command -v "${runtime_python_bin}" >/dev/null 2>&1 || die "python3 is required"
fi

if ! IFS=$'\t' read -r source_version source_commit < <(
  runtime_python - "${METADATA_PATH}" "${flavor}" "${goos}/${goarch}" <<'PY'
import json
import pathlib
import sys

manifest_path, flavor, platform = sys.argv[1:4]
manifest = json.loads(pathlib.Path(manifest_path).read_text(encoding="utf-8"))
if manifest.get("schemaVersion") != 1:
    raise SystemExit("source export manifest has an unsupported schemaVersion")
if manifest.get("flavor") != flavor:
    raise SystemExit("source export manifest flavor does not match --flavor")
if platform not in manifest.get("platforms", []):
    raise SystemExit("source export manifest does not support " + platform)
version = manifest.get("version")
source_commit = manifest.get("sourceCommit")
if not isinstance(version, str) or not version.startswith("v"):
    raise SystemExit("source export manifest version is invalid")
if not isinstance(source_commit, str) or len(source_commit) != 40:
    raise SystemExit("source export manifest sourceCommit is invalid")
print(version + "\t" + source_commit)
PY
); then
  die "source export manifest validation failed"
fi

target=""
linked_flavor=""
case "${flavor}" in
  runtime)
    target="./cmd/client-runtime"
    linked_flavor="runtime"
    ;;
  runtime-cloudflared)
    target="./cmd/client-runtime-cloudflared"
    linked_flavor="runtime-cloudflared"
    ;;
esac

output_dir="$(dirname "${output}")"
mkdir -p "${output_dir}"
output="$(cd "${output_dir}" && pwd)/$(basename "${output}")"
go_version="$(go env GOVERSION)"
go_mod_flag="-mod=readonly"
if [[ -f "${PROJECT_ROOT}/vendor/modules.txt" ]]; then
  go_mod_flag="-mod=vendor"
fi
build_flags="-trimpath -buildvcs=false"
semantic_version="${source_version#v}"
ldflags="-X ${MODULE_PATH}/pkg/version.semanticVersion=${semantic_version} -X ${MODULE_PATH}/pkg/version.GitSHA=${source_commit} -X ${MODULE_PATH}/pkg/version.GoVersion=${go_version} -X '${MODULE_PATH}/pkg/version.BuildFlags=${build_flags}' -X ${MODULE_PATH}/pkg/version.Flavor=${linked_flavor}"

(
  cd "${PROJECT_ROOT}"
  env \
    GOWORK=off \
    GOOS="${goos}" \
    GOARCH="${goarch}" \
    CGO_ENABLED=0 \
    go build \
      "${go_mod_flag}" \
      -trimpath \
      -buildvcs=false \
      -ldflags "${ldflags}" \
      -o "${output}" \
      "${target}"
)

if [[ "${goos}" != "windows" ]]; then
  chmod 0755 "${output}"
fi
printf 'rebuilt %s for %s/%s to %s\n' "${flavor}" "${goos}" "${goarch}" "${output}"
