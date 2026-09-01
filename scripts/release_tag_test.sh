#!/usr/bin/env bash
set -euo pipefail

readonly SCRIPT_DIR="$(cd "$(dirname "$BASH_SOURCE")" && pwd)"
readonly RELEASE_TAG_SCRIPT="$SCRIPT_DIR/release_tag.sh"

fail() {
  echo "release_tag_test.sh: $*" >&2
  exit 1
}

assert_eq() {
  local expected="$1"
  local actual="$2"
  [[ "$actual" == "$expected" ]] ||
    fail "expected $expected, got $actual"
}

assert_contains() {
  local haystack="$1"
  local needle="$2"
  [[ "$haystack" == *"$needle"* ]] ||
    fail "expected output to contain $needle, got $haystack"
}

assert_fails_with() {
  local expected="$1"
  shift

  local output
  if output="$("$@" 2>&1)"; then
    fail "expected command to fail: $*"
  fi
  assert_contains "$output" "$expected"
}

stable_metadata="$("$RELEASE_TAG_SCRIPT" parse v0.0.10)"
assert_eq "v0.0.10" "$("$RELEASE_TAG_SCRIPT" make 0.0.10)"
assert_contains "$stable_metadata" "release_tag=v0.0.10"
assert_contains "$stable_metadata" "release_version=0.0.10"
assert_contains "$stable_metadata" "prerelease=false"
assert_contains "$stable_metadata" "public_blob_path=tunnel-client/v0.0.10"
assert_contains "$stable_metadata" "public_base_url=https://persistent.oaistatic.com/tunnel-client/v0.0.10"

prerelease_metadata="$("$RELEASE_TAG_SCRIPT" parse v0.0.10-rc.1)"
assert_contains "$prerelease_metadata" "release_version=0.0.10-rc.1"
assert_contains "$prerelease_metadata" "prerelease=true"

"$RELEASE_TAG_SCRIPT" check-source-version v0.0.14
"$RELEASE_TAG_SCRIPT" check-source-version 0.0.14

assert_fails_with "version must not include a release-word suffix" \
  "$RELEASE_TAG_SCRIPT" parse v0.0.10--context-conduit-topaz
assert_fails_with "tag must look like v<semver>" \
  "$RELEASE_TAG_SCRIPT" parse 0.0.10
assert_fails_with "source version 0.0.14 in pkg/version/VERSION does not match release version 0.0.15" \
  "$RELEASE_TAG_SCRIPT" check-source-version v0.0.15
assert_fails_with "Usage:" \
  "$RELEASE_TAG_SCRIPT" make 0.0.10 context-conduit-topaz
