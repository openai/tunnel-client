#!/usr/bin/env bash
set -euo pipefail

readonly BINARY_NAME="tunnel-client"
readonly SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"

usage() {
  cat <<'EOF'
Usage:
  ./scripts/build_distribution_package.sh \
    --tag <release-tag> \
    --binary-dir <directory-with-raw-binaries> \
    --output-dir <directory-for-archives>

Example:
  ./scripts/build_distribution_package.sh \
    --tag v0.3.1--ember-orchid \
    --binary-dir dist/package-input \
    --output-dir dist/public
EOF
}

die() {
  echo "build_distribution_package.sh: $*" >&2
  exit 1
}

tag=""
binary_dir=""
output_dir=""

while [[ $# -gt 0 ]]; do
  case "$1" in
    --tag)
      tag="${2:-}"
      shift 2
      ;;
    --binary-dir)
      binary_dir="${2:-}"
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

[[ -n "$tag" ]] || die "--tag is required"
[[ -n "$binary_dir" ]] || die "--binary-dir is required"
[[ -n "$output_dir" ]] || die "--output-dir is required"
[[ -d "$binary_dir" ]] || die "binary directory does not exist: $binary_dir"

"${SCRIPT_DIR}/release_tag.sh" check-source-version "$tag"

mkdir -p "$output_dir"
output_dir="$(cd "$output_dir" && pwd)"

bundle_name="${BINARY_NAME}-${tag}-all"
tmp_dir="$(mktemp -d)"
bundle_dir="${tmp_dir}/${bundle_name}"
trap 'rm -rf "$tmp_dir"' EXIT

mkdir -p "${bundle_dir}/bin"

# Reuse git archive so the bundled source tree respects export-ignore rules.
git archive --worktree-attributes --format=tar HEAD | tar -xf - -C "$bundle_dir"

shopt -s nullglob
binary_count=0
prefix="${BINARY_NAME}-${tag}-"

for path in "${binary_dir}"/*; do
  base="$(basename "$path")"
  [[ -f "$path" ]] || continue
  [[ "$base" == "${prefix}"* ]] || die "unexpected binary artifact name: ${base}"

  suffix="${base#${prefix}}"
  ext=""
  if [[ "$suffix" == *.exe ]]; then
    ext=".exe"
    suffix="${suffix%.exe}"
  fi

  goos="${suffix%-*}"
  goarch="${suffix##*-}"
  [[ -n "$goos" && -n "$goarch" && "$goos" != "$goarch" ]] || die "could not parse platform from ${base}"

  platform_dir="${bundle_dir}/bin/${goos}_${goarch}"
  mkdir -p "$platform_dir"
  cp "$path" "${platform_dir}/${BINARY_NAME}${ext}"
  binary_count=$((binary_count + 1))
done

[[ "$binary_count" -gt 0 ]] || die "no binaries found under ${binary_dir}"

tar -C "$tmp_dir" -czf "${output_dir}/${bundle_name}.tar.gz" "$bundle_name"
(
  cd "$tmp_dir"
  zip -q -9 -r "${output_dir}/${bundle_name}.zip" "$bundle_name"
)
