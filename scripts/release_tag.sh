#!/usr/bin/env bash
set -euo pipefail

readonly PUBLIC_BUCKET_PREFIX="tunnel-client"
readonly PUBLIC_BASE_URL_ROOT="https://persistent.oaistatic.com"
readonly SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
readonly REPO_ROOT="$(cd "${SCRIPT_DIR}/.." && pwd)"
readonly SOURCE_VERSION_FILE="${REPO_ROOT}/pkg/version/VERSION"

usage() {
  cat <<'EOF'
Usage:
  ./scripts/release_tag.sh make <version> <word>
  ./scripts/release_tag.sh parse <tag>
  ./scripts/release_tag.sh source-version
  ./scripts/release_tag.sh set-source-version <version>
  ./scripts/release_tag.sh check-source-version <tag-or-version>

Examples:
  ./scripts/release_tag.sh make 0.3.1 ember-orchid
  ./scripts/release_tag.sh parse v0.3.1--ember-orchid
  ./scripts/release_tag.sh parse v0.3.1-rc.1--ember-orchid
  ./scripts/release_tag.sh set-source-version 0.3.1
  ./scripts/release_tag.sh check-source-version v0.3.1--ember-orchid
EOF
}

die() {
  echo "release_tag.sh: $*" >&2
  exit 1
}

version_re='^[0-9]+\.[0-9]+\.[0-9]+(-[0-9A-Za-z.-]+)?(\+[0-9A-Za-z.-]+)?$'
word_re='^[a-z0-9]+(-[a-z0-9]+)*$'

validate_version() {
  local version="$1"
  [[ "$version" =~ $version_re ]] || die "version must be a semver like 1.2.3 or 1.2.3-rc.1"
}

validate_word() {
  local word="$1"
  [[ "$word" =~ $word_re ]] || die "word must contain only lowercase letters, digits, and single hyphen separators"
}

release_version_from_tag_or_version() {
  local input="$1"
  local version word version_prefix

  if [[ "$input" == v*--* ]]; then
    version_prefix="${input%%--*}"
    word="${input#*--}"
    version="${version_prefix#v}"
    [[ -n "$version" && -n "$word" ]] || die "tag must include both a version and a word"
    validate_version "$version"
    validate_word "$word"
    printf '%s\n' "$version"
    return
  fi

  validate_version "$input"
  printf '%s\n' "$input"
}

source_version() {
  [[ -f "$SOURCE_VERSION_FILE" ]] || die "source version file missing: ${SOURCE_VERSION_FILE}"
  local version
  version="$(tr -d '[:space:]' < "$SOURCE_VERSION_FILE")"
  validate_version "$version"
  printf '%s\n' "$version"
}

set_source_version() {
  local version="$1"
  validate_version "$version"
  printf '%s\n' "$version" > "$SOURCE_VERSION_FILE"
}

check_source_version() {
  local expected actual
  expected="$(release_version_from_tag_or_version "$1")"
  actual="$(source_version)"
  [[ "$actual" == "$expected" ]] || die "source version ${actual} in pkg/version/VERSION does not match release version ${expected}"
}

make_tag() {
  local version="$1"
  local word="$2"
  validate_version "$version"
  validate_word "$word"
  printf 'v%s--%s\n' "$version" "$word"
}

parse_tag() {
  local tag="$1"
  local version_prefix word version prerelease public_blob_path public_base_url

  [[ "$tag" == v*--* ]] || die "tag must look like v<semver>--<word>"

  version_prefix="${tag%%--*}"
  word="${tag#*--}"
  version="${version_prefix#v}"

  [[ -n "$version" && -n "$word" ]] || die "tag must include both a version and a word"
  validate_version "$version"
  validate_word "$word"

  prerelease=false
  if [[ "$version" == *-* ]]; then
    prerelease=true
  fi

  public_blob_path="${PUBLIC_BUCKET_PREFIX}/${tag}"
  public_base_url="${PUBLIC_BASE_URL_ROOT}/${public_blob_path}"

  printf 'release_tag=%s\n' "$tag"
  printf 'release_version=%s\n' "$version"
  printf 'release_word=%s\n' "$word"
  printf 'prerelease=%s\n' "$prerelease"
  printf 'public_blob_path=%s\n' "$public_blob_path"
  printf 'public_base_url=%s\n' "$public_base_url"
}

main() {
  local command="${1:-}"
  case "$command" in
    make)
      [[ $# -eq 3 ]] || {
        usage >&2
        exit 1
      }
      make_tag "$2" "$3"
      ;;
    parse)
      [[ $# -eq 2 ]] || {
        usage >&2
        exit 1
      }
      parse_tag "$2"
      ;;
    source-version)
      [[ $# -eq 1 ]] || {
        usage >&2
        exit 1
      }
      source_version
      ;;
    set-source-version)
      [[ $# -eq 2 ]] || {
        usage >&2
        exit 1
      }
      set_source_version "$2"
      ;;
    check-source-version)
      [[ $# -eq 2 ]] || {
        usage >&2
        exit 1
      }
      check_source_version "$2"
      ;;
    *)
      usage >&2
      exit 1
      ;;
  esac
}

main "$@"
