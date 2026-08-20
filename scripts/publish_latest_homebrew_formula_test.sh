#!/usr/bin/env bash
set -euo pipefail

readonly SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
readonly WRAPPER="${SCRIPT_DIR}/publish_latest_homebrew_formula.sh"

fail() {
  echo "publish_latest_homebrew_formula_test.sh: $*" >&2
  exit 1
}

assert_contains() {
  local haystack="$1"
  local needle="$2"
  [[ "${haystack}" == *"${needle}"* ]] || fail "expected output to contain ${needle}"
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
mkdir -p "${tmp_dir}/scripts" "${tmp_dir}/bin"
cp "${WRAPPER}" "${tmp_dir}/scripts/publish_latest_homebrew_formula.sh"
chmod +x "${tmp_dir}/scripts/publish_latest_homebrew_formula.sh"

cat > "${tmp_dir}/scripts/release_tag.sh" <<'EOF'
#!/usr/bin/env bash
set -euo pipefail
[[ "$1" == "parse" ]] || exit 1
case "$2" in
  v1.2.3)
    printf '%s\n' 'release_tag=v1.2.3' 'release_version=1.2.3' 'prerelease=false' 'public_base_url=https://persistent.example/tunnel-client/v1.2.3'
    ;;
  *)
    exit 1
    ;;
esac
EOF
chmod +x "${tmp_dir}/scripts/release_tag.sh"

cat > "${tmp_dir}/scripts/generate_homebrew_formula.sh" <<'EOF'
#!/usr/bin/env bash
set -euo pipefail
printf '%s\n' "$*" > "${GENERATOR_LOG}"
while [[ $# -gt 0 ]]; do
  if [[ "$1" == "--output" ]]; then
    output="$2"
    break
  fi
  shift
done
cat > "${output}" <<'FORMULA'
class TunnelClient < Formula
  version "1.2.3"
end
FORMULA
EOF
chmod +x "${tmp_dir}/scripts/generate_homebrew_formula.sh"

cat > "${tmp_dir}/scripts/publish_homebrew_formula.sh" <<'EOF'
#!/usr/bin/env bash
set -euo pipefail
[[ "${GH_TOKEN:-}" == "installation-token" ]] || exit 1
printf '%s\n' "$*" > "${PUBLISHER_LOG}"
printf '%s\n' 'status=created' 'branch=tunnel-client-1.2.3'
EOF
chmod +x "${tmp_dir}/scripts/publish_homebrew_formula.sh"

cat > "${tmp_dir}/bin/curl" <<'EOF'
#!/usr/bin/env bash
set -euo pipefail
printf '%s\n' "$*" >> "${CURL_LOG}"
output=""
url=""
config=""
while [[ $# -gt 0 ]]; do
  case "$1" in
    --output)
      output="$2"
      shift 2
      ;;
    --retry|--retry-delay|--connect-timeout|--max-time|--retry-max-time)
      shift 2
      ;;
    --header)
      shift 2
      ;;
    --config)
      config="$2"
      shift 2
      ;;
    *)
      url="$1"
      shift
      ;;
  esac
done
if [[ -n "${config}" ]]; then
  grep -Fqx 'header = "Authorization: Bearer read-token"' "${config}"
fi
if [[ "${url}" == *"/releases/latest" ]]; then
  printf '%s' "${CURL_RELEASE_JSON}"
  exit 0
fi
[[ -n "${output}" ]] || exit 1
printf '%s\n' 'deadbeef  tunnel-client-v1.2.3-darwin-amd64.zip' > "${output}"
EOF
chmod +x "${tmp_dir}/bin/curl"

cat > "${tmp_dir}/bin/ruby" <<'EOF'
#!/usr/bin/env bash
set -euo pipefail
echo "runtime wrapper must not invoke ruby" >&2
exit 1
EOF
chmod +x "${tmp_dir}/bin/ruby"

export GENERATOR_LOG="${tmp_dir}/generator.log"
export PUBLISHER_LOG="${tmp_dir}/publisher.log"
export CURL_LOG="${tmp_dir}/curl.log"
export CURL_RELEASE_JSON='{"tag_name":"v1.2.3","draft":false,"prerelease":false,"published_at":"2026-08-10T00:00:00Z"}'
token_json_file="${tmp_dir}/token-json"
printf '%s\n' '{"token":"installation-token"}' > "${token_json_file}"

if ! output="$(
  PATH="${tmp_dir}/bin:${PATH}" OPENAI_GITHUB_INSTALLATION_TOKEN_FILE="${token_json_file}" "${tmp_dir}/scripts/publish_latest_homebrew_formula.sh" 2>&1
)"; then
  fail "stable publication wrapper failed: ${output}"
fi
assert_contains "${output}" 'status=created'
assert_contains "$(<"${GENERATOR_LOG}")" '--tag v1.2.3'
assert_contains "$(<"${PUBLISHER_LOG}")" '--tag v1.2.3'
[[ "$(grep -c -- '--connect-timeout 10' "${CURL_LOG}")" == "2" ]] ||
  fail "expected both downloads to set a connection timeout"
[[ "$(grep -c -- '--max-time 60' "${CURL_LOG}")" == "2" ]] ||
  fail "expected both downloads to set a transfer timeout"
[[ "$(grep -c -- '--retry-max-time 180' "${CURL_LOG}")" == "2" ]] ||
  fail "expected both downloads to set a retry-window timeout"

if ! output="$(
  PATH="${tmp_dir}/bin:${PATH}" TUNNEL_CLIENT_RELEASE_READ_TOKEN='read-token' OPENAI_GITHUB_INSTALLATION_TOKEN_JSON='{"token":"installation-token"}' "${tmp_dir}/scripts/publish_latest_homebrew_formula.sh" 2>&1
)"; then
  fail "authenticated release lookup failed: ${output}"
fi
assert_contains "${output}" 'status=created'
assert_contains "$(<"${CURL_LOG}")" '--config'
if grep -Fq 'read-token' "${CURL_LOG}"; then
  fail "release read token must not appear in curl arguments"
fi

export CURL_RELEASE_JSON='{"tag_name":"v1.2.3-rc.1","draft":false,"prerelease":true,"published_at":"2026-08-10T00:00:00Z"}'
assert_fails_with "latest GitHub release is a prerelease" env PATH="${tmp_dir}/bin:${PATH}" OPENAI_GITHUB_INSTALLATION_TOKEN_JSON='{"token":"installation-token"}' "${tmp_dir}/scripts/publish_latest_homebrew_formula.sh"

export CURL_RELEASE_JSON='{"tag_name":"v1.2.3+build.1","draft":false,"prerelease":false,"published_at":"2026-08-10T00:00:00Z"}'
assert_fails_with "latest GitHub release tag is not a stable v<semver> tag" env PATH="${tmp_dir}/bin:${PATH}" OPENAI_GITHUB_INSTALLATION_TOKEN_JSON='{"token":"installation-token"}' "${tmp_dir}/scripts/publish_latest_homebrew_formula.sh"

export CURL_RELEASE_JSON='{"tag_name":"v1.2.3","draft":false,"prerelease":false,"published_at":"2026-08-10T00:00:00Z"}'
assert_fails_with "OPENAI_GITHUB_INSTALLATION_TOKEN_FILE or OPENAI_GITHUB_INSTALLATION_TOKEN_JSON is required" env -u OPENAI_GITHUB_INSTALLATION_TOKEN_FILE -u OPENAI_GITHUB_INSTALLATION_TOKEN_JSON PATH="${tmp_dir}/bin:${PATH}" "${tmp_dir}/scripts/publish_latest_homebrew_formula.sh"

assert_fails_with "GitHub installation token JSON does not contain a token" env PATH="${tmp_dir}/bin:${PATH}" OPENAI_GITHUB_INSTALLATION_TOKEN_JSON='{"token":""}' "${tmp_dir}/scripts/publish_latest_homebrew_formula.sh"

assert_fails_with "GitHub installation token file is not readable" env PATH="${tmp_dir}/bin:${PATH}" OPENAI_GITHUB_INSTALLATION_TOKEN_FILE="${tmp_dir}/missing-token-json" "${tmp_dir}/scripts/publish_latest_homebrew_formula.sh"
