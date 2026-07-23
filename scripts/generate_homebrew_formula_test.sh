#!/usr/bin/env bash
set -euo pipefail

readonly SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
readonly GENERATOR="${SCRIPT_DIR}/generate_homebrew_formula.sh"

fail() {
  echo "generate_homebrew_formula_test.sh: $*" >&2
  exit 1
}

assert_contains() {
  local haystack="$1"
  local needle="$2"
  [[ "${haystack}" == *"${needle}"* ]] ||
    fail "expected output to contain ${needle}"
}

assert_fails_with() {
  local expected="$1"
  shift

  local output
  if output="$("$@" 2>&1)"; then
    fail "expected command to fail: $*"
  fi
  assert_contains "${output}" "${expected}"
}

tmp_dir="$(mktemp -d)"
trap 'rm -rf "${tmp_dir}"' EXIT

checksums="${tmp_dir}/SHA256SUMS.txt"
formula="${tmp_dir}/tunnel-client.rb"
cat > "${checksums}" <<'EOF'
aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa  tunnel-client-v1.2.3-darwin-amd64.zip
bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb  tunnel-client-v1.2.3-darwin-arm64.zip
cccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccc  tunnel-client-v1.2.3-linux-amd64.zip
dddddddddddddddddddddddddddddddddddddddddddddddddddddddddddddddd  tunnel-client-v1.2.3-linux-arm64.zip
eeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeee  tunnel-client-v1.2.3-all.tar.gz
EOF

"${GENERATOR}" --tag v1.2.3 --checksums "${checksums}" --output "${formula}"
generated="$(<"${formula}")"

assert_contains "${generated}" 'class TunnelClient < Formula'
assert_contains "${generated}" '# typed: strict'
assert_contains "${generated}" 'version "1.2.3"'
assert_contains "${generated}" 'url "https://persistent.oaistatic.com/tunnel-client/v#{version}/tunnel-client-v#{version}-darwin-amd64.zip"'
assert_contains "${generated}" 'sha256 "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"'
assert_contains "${generated}" 'sha256 "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"'
assert_contains "${generated}" 'sha256 "cccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccc"'
assert_contains "${generated}" 'sha256 "dddddddddddddddddddddddddddddddddddddddddddddddddddddddddddddddd"'
assert_contains "${generated}" 'if Hardware::CPU.is_64_bit?'
assert_contains "${generated}" 'libexec.install "tunnel-client", "cloudflared", "cloudflared-manifest.json"'
assert_contains "${generated}" 'bin.write_exec_script libexec/"tunnel-client"'
assert_contains "${generated}" 'assert_match version.to_s, shell_output("#{bin}/tunnel-client --version")'

if command -v ruby >/dev/null 2>&1; then
  ruby -c "${formula}" >/dev/null
fi

missing_checksums="${tmp_dir}/missing.txt"
grep -v 'darwin-arm64' "${checksums}" > "${missing_checksums}"
assert_fails_with "expected exactly one checksum for tunnel-client-v1.2.3-darwin-arm64.zip, found 0" \
  "${GENERATOR}" --tag v1.2.3 --checksums "${missing_checksums}" --output "${formula}"

duplicate_checksums="${tmp_dir}/duplicate.txt"
cp "${checksums}" "${duplicate_checksums}"
printf '%s\n' 'ffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffff  tunnel-client-v1.2.3-linux-amd64.zip' >> "${duplicate_checksums}"
assert_fails_with "expected exactly one checksum for tunnel-client-v1.2.3-linux-amd64.zip, found 2" \
  "${GENERATOR}" --tag v1.2.3 --checksums "${duplicate_checksums}" --output "${formula}"

invalid_checksums="${tmp_dir}/invalid.txt"
sed 's/cccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccc/not-a-digest/' \
  "${checksums}" > "${invalid_checksums}"
assert_fails_with "checksum for tunnel-client-v1.2.3-linux-amd64.zip is not a lowercase SHA-256 digest" \
  "${GENERATOR}" --tag v1.2.3 --checksums "${invalid_checksums}" --output "${formula}"

assert_fails_with "prerelease tags do not produce a Homebrew formula" \
  "${GENERATOR}" --tag v1.2.3-rc.1 --checksums "${checksums}" --output "${formula}"

prerelease_checksums="${tmp_dir}/prerelease-SHA256SUMS.txt"
sed 's/v1\.2\.3/v1.2.3-rc.1/g' "${checksums}" > "${prerelease_checksums}"
"${GENERATOR}" \
  --tag v1.2.3-rc.1 \
  --checksums "${prerelease_checksums}" \
  --output "${formula}" \
  --allow-prerelease
generated_prerelease="$(<"${formula}")"
assert_contains "${generated_prerelease}" 'version "1.2.3-rc.1"'
assert_contains "${generated_prerelease}" 'url "https://persistent.oaistatic.com/tunnel-client/v#{version}/tunnel-client-v#{version}-darwin-arm64.zip"'
