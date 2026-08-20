#!/usr/bin/env bash
set -euo pipefail

readonly SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
readonly RELEASE_TAG_SCRIPT="${SCRIPT_DIR}/release_tag.sh"
readonly GENERATOR="${SCRIPT_DIR}/generate_homebrew_formula.sh"
readonly PUBLISHER="${SCRIPT_DIR}/publish_homebrew_formula.sh"
readonly RELEASE_API_URL="${TUNNEL_CLIENT_RELEASE_API_URL:-https://api.github.com/repos/openai/tunnel-client/releases/latest}"
readonly CURL_CONNECT_TIMEOUT_SECONDS="10"
readonly CURL_MAX_TIME_SECONDS="60"
readonly CURL_RETRY_MAX_TIME_SECONDS="180"

die() {
  echo "publish_latest_homebrew_formula.sh: $*" >&2
  exit 1
}

command -v curl >/dev/null 2>&1 || die "curl is required"
command -v jq >/dev/null 2>&1 || die "jq is required"

tmp_dir="$(mktemp -d "${TMPDIR:-/tmp}/tunnel-client-homebrew.XXXXXX")"
cleanup() {
  rm -rf "${tmp_dir}"
}
trap cleanup EXIT

release_curl_args=(
  --fail
  --silent
  --show-error
  --location
  --retry 3
  --retry-delay 2
  --connect-timeout "${CURL_CONNECT_TIMEOUT_SECONDS}"
  --max-time "${CURL_MAX_TIME_SECONDS}"
  --retry-max-time "${CURL_RETRY_MAX_TIME_SECONDS}"
)
release_read_token="${TUNNEL_CLIENT_RELEASE_READ_TOKEN:-}"
release_curl_config="${tmp_dir}/release-read.curl"
if [[ -n "${release_read_token}" ]]; then
  (
    umask 077
    printf 'header = "Authorization: Bearer %s"\n' "${release_read_token}" > "${release_curl_config}"
  )
  release_curl_args+=(--config "${release_curl_config}")
fi
unset TUNNEL_CLIENT_RELEASE_READ_TOKEN
unset release_read_token
release_json="$(
  curl "${release_curl_args[@]}" "${RELEASE_API_URL}"
)" || die "could not read latest tunnel-client release"
rm -f "${release_curl_config}"
release_tag="$(printf '%s' "${release_json}" | jq -er '.tag_name')"
release_draft="$(printf '%s' "${release_json}" | jq -r '.draft')"
release_prerelease="$(printf '%s' "${release_json}" | jq -r '.prerelease')"
release_published_at="$(printf '%s' "${release_json}" | jq -er '.published_at')"

[[ "${release_draft}" == "false" ]] || die "latest GitHub release is a draft"
[[ "${release_prerelease}" == "false" ]] || die "latest GitHub release is a prerelease"
[[ "${release_published_at}" != "null" && -n "${release_published_at}" ]] ||
  die "latest GitHub release is not published"
[[ "${release_tag}" =~ ^v[0-9]+\.[0-9]+\.[0-9]+$ ]] ||
  die "latest GitHub release tag is not a stable v<semver> tag: ${release_tag}"

eval "$("${RELEASE_TAG_SCRIPT}" parse "${release_tag}")"
[[ "${prerelease}" == "false" ]] ||
  die "latest GitHub release tag is not stable: ${release_tag}"

token_json_file="${OPENAI_GITHUB_INSTALLATION_TOKEN_FILE:-}"
token_json="${OPENAI_GITHUB_INSTALLATION_TOKEN_JSON:-}"
if [[ -n "${token_json_file}" ]]; then
  [[ -r "${token_json_file}" ]] ||
    die "GitHub installation token file is not readable: ${token_json_file}"
  github_token="$(
    jq -er '.token | select(type == "string" and length > 0)' "${token_json_file}"
  )" || die "GitHub installation token JSON does not contain a token"
elif [[ -n "${token_json}" ]]; then
  github_token="$(
    printf '%s' "${token_json}" |
      jq -er '.token | select(type == "string" and length > 0)'
  )" || die "GitHub installation token JSON does not contain a token"
else
  die "OPENAI_GITHUB_INSTALLATION_TOKEN_FILE or OPENAI_GITHUB_INSTALLATION_TOKEN_JSON is required"
fi
unset OPENAI_GITHUB_INSTALLATION_TOKEN_FILE
unset OPENAI_GITHUB_INSTALLATION_TOKEN_JSON
unset token_json_file
unset token_json
if [[ "${GITHUB_ACTIONS:-}" == "true" ]]; then
  echo "::add-mask::${github_token}"
fi

checksums="${tmp_dir}/SHA256SUMS.txt"
formula="${tmp_dir}/tunnel-client.rb"
curl --fail --silent --show-error --location --retry 3 --retry-delay 2 --connect-timeout "${CURL_CONNECT_TIMEOUT_SECONDS}" --max-time "${CURL_MAX_TIME_SECONDS}" --retry-max-time "${CURL_RETRY_MAX_TIME_SECONDS}" --output "${checksums}" "${public_base_url}/SHA256SUMS.txt" ||
  die "could not download release checksums for ${release_tag}"
"${GENERATOR}" --tag "${release_tag}" --checksums "${checksums}" --output "${formula}"

GH_TOKEN="${github_token}" "${PUBLISHER}" --tag "${release_tag}" --formula "${formula}"
