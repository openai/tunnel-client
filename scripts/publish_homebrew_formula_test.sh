#!/usr/bin/env bash
set -euo pipefail

readonly SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
readonly PUBLISHER="${SCRIPT_DIR}/publish_homebrew_formula.sh"

fail() {
  echo "publish_homebrew_formula_test.sh: $*" >&2
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
mkdir -p "${tmp_dir}/bin"

gh_log="${tmp_dir}/gh.log"
payload_dir="${tmp_dir}/payloads"
mkdir -p "${payload_dir}"
cat > "${tmp_dir}/bin/gh" <<'EOF'
#!/usr/bin/env bash
set -euo pipefail

printf '%s\n' "$*" >> "${GH_LOG}"
endpoint="${2:-}"
include="false"
if [[ " $* " == *" --include "* ]]; then
  include="true"
fi

emit_optional_status() {
  local status="$1"
  if [[ "${include}" == "true" ]]; then
    printf 'HTTP/1.1 %s\n\n' "${status}"
  fi
}

case "${endpoint}" in
  */contents/Formula/tunnel-client.rb\?ref=main)
    if [[ -n "${GH_BASE_FORMULA_ERROR_STATUS:-}" ]]; then
      emit_optional_status "${GH_BASE_FORMULA_ERROR_STATUS} Test Error"
      exit 1
    fi
    if [[ -n "${GH_BASE_FORMULA_SHA:-}" ]]; then
      emit_optional_status '200 OK'
      if [[ "$*" == *"application/vnd.github.raw"* ]]; then
        printf '%s' "${GH_BASE_FORMULA_CONTENT:-}"
      else
        printf '{"sha":"%s"}\n' "${GH_BASE_FORMULA_SHA}"
      fi
    else
      emit_optional_status '404 Not Found'
      exit 1
    fi
    ;;
  */git/ref/heads/main)
    printf '%s\n' '{"object":{"sha":"base-sha"}}'
    ;;
  */git/ref/heads/*)
    if [[ -n "${GH_BRANCH_REF_ERROR_STATUS:-}" ]]; then
      emit_optional_status "${GH_BRANCH_REF_ERROR_STATUS} Test Error"
      exit 1
    fi
    if [[ "${endpoint##*/}" == "${GH_EXISTING_BRANCH:-}" ]]; then
      emit_optional_status '200 OK'
      printf '%s\n' '{"object":{"sha":"existing-commit-sha"}}'
    else
      emit_optional_status '404 Not Found'
      exit 1
    fi
    ;;
  */git/commits/base-sha)
    printf '%s\n' '{"tree":{"sha":"base-tree"}}'
    ;;
  */contents/Formula/tunnel-client.rb\?ref=*)
    if [[ -n "${GH_BRANCH_FORMULA_SHA:-}" ]]; then
      printf '{"sha":"%s"}\n' "${GH_BRANCH_FORMULA_SHA}"
    else
      exit 1
    fi
    ;;
  */git/blobs)
    cat > "${GH_PAYLOAD_DIR}/blob.json"
    printf '%s\n' '{"sha":"blob-sha"}'
    ;;
  */git/trees)
    cat > "${GH_PAYLOAD_DIR}/tree.json"
    printf '%s\n' '{"sha":"tree-sha"}'
    ;;
  */git/commits)
    cat > "${GH_PAYLOAD_DIR}/commit.json"
    printf '%s\n' '{"sha":"commit-sha"}'
    ;;
  */git/refs)
    cat > "${GH_PAYLOAD_DIR}/ref.json"
    printf '%s\n' '{"ref":"refs/heads/test"}'
    ;;
  */pulls\?state=open*)
    if [[ -n "${GH_OPEN_PR_URL:-}" ]]; then
      printf '[{"number":99,"html_url":"%s"}]\n' "${GH_OPEN_PR_URL}"
    else
      printf '%s\n' '[]'
    fi
    ;;
  */pulls/*/files)
    printf '%s\n' '[{"filename":"Formula/tunnel-client.rb"}]'
    ;;
  */compare/*)
    printf '%s\n' '{"files":[{"filename":"Formula/tunnel-client.rb"}]}'
    ;;
  */pulls)
    cat > "${GH_PAYLOAD_DIR}/pr.json"
    printf '%s\n' '{"html_url":"https://github.com/openai/homebrew-tools/pull/99"}'
    ;;
  *)
    echo "unexpected gh invocation: $*" >&2
    exit 1
    ;;
esac
EOF
chmod +x "${tmp_dir}/bin/gh"

formula="${tmp_dir}/tunnel-client.rb"
cat > "${formula}" <<'EOF'
class TunnelClient < Formula
  version "1.2.3"
end
EOF

export GH_LOG="${gh_log}"
export GH_PAYLOAD_DIR="${payload_dir}"
export PATH="${tmp_dir}/bin:${PATH}"

output="$(
  "${PUBLISHER}" \
    --tag v1.2.3 \
    --formula "${formula}"
)"
assert_contains "${output}" 'status=created'
assert_contains "${output}" 'branch=tunnel-client-1.2.3'
assert_contains "${output}" 'commit=commit-sha'
assert_contains "${output}" 'pr_url=https://github.com/openai/homebrew-tools/pull/99'
assert_contains "$(<"${gh_log}")" 'repos/openai/homebrew-tools/git/ref/heads/main'
assert_contains "$(jq -r '.tree[0].path' "${payload_dir}/tree.json")" 'Formula/tunnel-client.rb'
assert_contains "$(jq -r '.ref' "${payload_dir}/ref.json")" 'refs/heads/tunnel-client-1.2.3'
[[ "$(jq -r '.draft' "${payload_dir}/pr.json")" == "false" ]] ||
  fail "stable publication should not be draft"

export GH_EXISTING_BRANCH="tunnel-client-1.2.3"
export GH_BRANCH_FORMULA_SHA="blob-sha"
export GH_OPEN_PR_URL="https://github.com/openai/homebrew-tools/pull/99"
existing_output="$(
  "${PUBLISHER}" \
    --tag v1.2.3 \
    --formula "${formula}"
)"
assert_contains "${existing_output}" 'status=existing-open-pr'
assert_contains "${existing_output}" 'commit=existing-commit-sha'
unset GH_OPEN_PR_URL
recovered_output="$(
  "${PUBLISHER}" \
    --tag v1.2.3 \
    --formula "${formula}"
)"
assert_contains "${recovered_output}" 'status=created-pr-for-existing-branch'
assert_contains "${recovered_output}" 'commit=existing-commit-sha'
export GH_BRANCH_FORMULA_SHA="different-blob-sha"
assert_fails_with "branch tunnel-client-1.2.3 already exists without a matching open publication PR" \
  "${PUBLISHER}" --tag v1.2.3 --formula "${formula}"
unset GH_EXISTING_BRANCH GH_BRANCH_FORMULA_SHA

export GH_BASE_FORMULA_SHA="blob-sha"
published_output="$(
  "${PUBLISHER}" \
    --tag v1.2.3 \
    --formula "${formula}"
)"
assert_contains "${published_output}" 'status=already-published'
assert_contains "${published_output}" 'commit=base-sha'
unset GH_BASE_FORMULA_SHA

export GH_BASE_FORMULA_SHA="newer-base-blob-sha"
export GH_BASE_FORMULA_CONTENT=$'class TunnelClient < Formula\n  version "2.0.0"\nend\n'
assert_fails_with "base formula version 2.0.0 is newer than 1.2.3" \
  "${PUBLISHER}" --tag v1.2.3 --formula "${formula}"

export GH_BASE_FORMULA_CONTENT=$'class TunnelClient < Formula\n  version "1.2.3"\nend\n'
assert_fails_with "base formula already has version 1.2.3 with different content" \
  "${PUBLISHER}" --tag v1.2.3 --formula "${formula}"
unset GH_BASE_FORMULA_SHA GH_BASE_FORMULA_CONTENT

export GH_BASE_FORMULA_ERROR_STATUS="500"
assert_fails_with "GitHub API request failed for repos/openai/homebrew-tools/contents/Formula/tunnel-client.rb?ref=main: HTTP 500" \
  "${PUBLISHER}" --tag v1.2.3 --formula "${formula}"
unset GH_BASE_FORMULA_ERROR_STATUS

export GH_BRANCH_REF_ERROR_STATUS="429"
assert_fails_with "GitHub API request failed for repos/openai/homebrew-tools/git/ref/heads/tunnel-client-1.2.3: HTTP 429" \
  "${PUBLISHER}" --tag v1.2.3 --formula "${formula}"
unset GH_BRANCH_REF_ERROR_STATUS

assert_fails_with "prerelease tags may only be published with --draft" \
  "${PUBLISHER}" --tag v1.2.3-rc.1 --formula "${formula}"

cat > "${formula}" <<'EOF'
class TunnelClient < Formula
  version "1.2.3-rc.1"
end
EOF
draft_output="$(
  "${PUBLISHER}" \
    --tag v1.2.3-rc.1 \
    --formula "${formula}" \
    --draft \
    --branch test-tunnel-client-1.2.3-rc.1-123
)"
assert_contains "${draft_output}" 'status=created'
assert_contains "${draft_output}" 'branch=test-tunnel-client-1.2.3-rc.1-123'
[[ "$(jq -r '.draft' "${payload_dir}/pr.json")" == "true" ]] ||
  fail "test publication should be draft"

assert_fails_with "draft branches must start with test-tunnel-client-" \
  "${PUBLISHER}" \
  --tag v1.2.3-rc.1 \
  --formula "${formula}" \
  --draft \
  --branch tunnel-client-1.2.3-rc.1
