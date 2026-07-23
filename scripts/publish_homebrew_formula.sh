#!/usr/bin/env bash
set -euo pipefail

readonly SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
readonly RELEASE_TAG_SCRIPT="${SCRIPT_DIR}/release_tag.sh"
readonly DEFAULT_REPOSITORY="openai/homebrew-tools"
readonly DEFAULT_BASE_BRANCH="main"
readonly FORMULA_DESTINATION="Formula/tunnel-client.rb"

usage() {
  cat <<'EOF'
Usage:
  ./scripts/publish_homebrew_formula.sh \
    --tag <release-tag> \
    --formula <formula.rb> \
    [--repository <owner/repo>] \
    [--base <branch>] \
    [--draft --branch <test-branch>]

Creates a same-repository branch and pull request in the OpenAI Homebrew tap.
Stable releases use the deterministic branch tunnel-client-<version> and open
a non-draft PR. Draft mode is only for an explicit test publish; it requires a
test-tunnel-client-* branch and may use a prerelease tag.

Authentication comes from gh CLI auth or GH_TOKEN. When this is wired into
release automation, GH_TOKEN must be a GitHub App token scoped to
openai/homebrew-tools with contents:write and pull_requests:write.
EOF
}

die() {
  echo "publish_homebrew_formula.sh: $*" >&2
  exit 1
}

tag=""
formula=""
repository="${HOMEBREW_TAP_REPOSITORY:-${DEFAULT_REPOSITORY}}"
base_branch="${HOMEBREW_TAP_BASE_BRANCH:-${DEFAULT_BASE_BRANCH}}"
branch=""
draft="false"

while [[ $# -gt 0 ]]; do
  case "$1" in
    --tag)
      tag="${2:-}"
      shift 2
      ;;
    --formula)
      formula="${2:-}"
      shift 2
      ;;
    --repository)
      repository="${2:-}"
      shift 2
      ;;
    --base)
      base_branch="${2:-}"
      shift 2
      ;;
    --branch)
      branch="${2:-}"
      shift 2
      ;;
    --draft)
      draft="true"
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

[[ -n "${tag}" ]] || die "--tag is required"
[[ -n "${formula}" ]] || die "--formula is required"
[[ -f "${formula}" ]] || die "formula file does not exist: ${formula}"
[[ "${repository}" =~ ^[A-Za-z0-9_.-]+/[A-Za-z0-9_.-]+$ ]] ||
  die "repository must be owner/name: ${repository}"
[[ "${base_branch}" =~ ^[A-Za-z0-9._-]+$ ]] ||
  die "base branch contains unsupported characters: ${base_branch}"

command -v gh >/dev/null 2>&1 || die "gh is required"
command -v jq >/dev/null 2>&1 || die "jq is required"
command -v git >/dev/null 2>&1 || die "git is required"

eval "$("${RELEASE_TAG_SCRIPT}" parse "${tag}")"
if [[ "${draft}" == "true" ]]; then
  [[ -n "${branch}" ]] || die "--branch is required with --draft"
  [[ "${branch}" == test-tunnel-client-* ]] ||
    die "draft branches must start with test-tunnel-client-"
else
  [[ "${prerelease}" == "false" ]] ||
    die "prerelease tags may only be published with --draft"
  expected_branch="tunnel-client-${release_version}"
  if [[ -z "${branch}" ]]; then
    branch="${expected_branch}"
  fi
  [[ "${branch}" == "${expected_branch}" ]] ||
    die "stable publication branch must be ${expected_branch}"
fi

git check-ref-format "refs/heads/${branch}" >/dev/null 2>&1 ||
  die "invalid branch name: ${branch}"
[[ "${branch}" =~ ^[A-Za-z0-9._-]+$ ]] ||
  die "branch contains unsupported characters: ${branch}"
grep -Fqx "class TunnelClient < Formula" "${formula}" ||
  die "formula does not declare TunnelClient"
grep -Fqx "  version \"${release_version}\"" "${formula}" ||
  die "formula version does not match ${release_tag}"

api() {
  gh api "$@"
}

api_optional_body=""

api_optional() {
  local endpoint="$1"
  local response=""
  local status=""

  api_optional_body=""
  if response="$(gh api "$@" --include 2>&1)"; then
    # `--include` prepends the HTTP status line and headers. Keep only the
    # response body so callers can continue to parse the JSON response.
    api_optional_body="$(printf '%s\n' "${response}" | sed '1,/^[[:space:]]*$/d')"
    return 0
  fi

  status="$(printf '%s\n' "${response}" | sed -n '1s/^[^ ]* \([0-9][0-9][0-9]\).*/\1/p')"
  if [[ "${status}" == "404" ]]; then
    return 1
  fi
  if [[ -n "${status}" ]]; then
    die "GitHub API request failed for ${endpoint}: HTTP ${status}"
  fi
  die "GitHub API request failed for ${endpoint}"
}

compare_stable_versions() {
  local left="$1"
  local right="$2"
  local -a left_parts
  local -a right_parts
  local index

  IFS='.' read -r -a left_parts <<< "${left}"
  IFS='.' read -r -a right_parts <<< "${right}"
  for index in 0 1 2; do
    if ((10#${left_parts[${index}]} > 10#${right_parts[${index}]})); then
      printf '1\n'
      return
    fi
    if ((10#${left_parts[${index}]} < 10#${right_parts[${index}]})); then
      printf '%s\n' '-1'
      return
    fi
  done
  printf '0\n'
}

repository_owner="${repository%%/*}"
branch_exists="false"
publication_status="created"
base_sha="$(
  api "repos/${repository}/git/ref/heads/${base_branch}" |
    jq -er '.object.sha'
)"
base_tree="$(
  api "repos/${repository}/git/commits/${base_sha}" |
    jq -er '.tree.sha'
)"
blob_payload="$(jq -n --rawfile content "${formula}" '{content: $content, encoding: "utf-8"}')"
blob_sha="$(
  printf '%s' "${blob_payload}" |
    api "repos/${repository}/git/blobs" --method POST --input - |
    jq -er '.sha'
)"

if api_optional "repos/${repository}/contents/${FORMULA_DESTINATION}?ref=${base_branch}"; then
  base_formula_json="${api_optional_body}"
  base_formula_sha="$(printf '%s' "${base_formula_json}" | jq -er '.sha')"
  if [[ "${base_formula_sha}" == "${blob_sha}" ]]; then
    printf 'status=already-published\n'
    printf 'branch=%s\n' "${base_branch}"
    printf 'commit=%s\n' "${base_sha}"
    printf 'pr_url=\n'
    exit 0
  fi
  if [[ "${draft}" == "false" ]]; then
    base_formula_content="$(
      api "repos/${repository}/contents/${FORMULA_DESTINATION}?ref=${base_branch}" \
        -H "Accept: application/vnd.github.raw"
    )"
    base_formula_version="$(
      printf '%s\n' "${base_formula_content}" |
        sed -n 's/^  version "\([0-9][0-9.]*\)"$/\1/p'
    )"
    [[ "${base_formula_version}" =~ ^[0-9]+\.[0-9]+\.[0-9]+$ ]] ||
      die "base formula has no stable semantic version"
    version_order="$(compare_stable_versions "${base_formula_version}" "${release_version}")"
    if [[ "${version_order}" == "0" ]]; then
      die "base formula already has version ${release_version} with different content"
    fi
    if [[ "${version_order}" == "1" ]]; then
      die "base formula version ${base_formula_version} is newer than ${release_version}"
    fi
  fi
fi

if api_optional "repos/${repository}/git/ref/heads/${branch}"; then
  existing_ref_json="${api_optional_body}"
  existing_commit_sha="$(printf '%s' "${existing_ref_json}" | jq -er '.object.sha')"
  branch_formula_json="$(
    api "repos/${repository}/contents/${FORMULA_DESTINATION}?ref=${branch}"
  )"
  branch_formula_sha="$(printf '%s' "${branch_formula_json}" | jq -er '.sha')"
  open_prs_json="$(
    api "repos/${repository}/pulls?state=open&head=${repository_owner}:${branch}&base=${base_branch}"
  )"
  existing_pr_number="$(printf '%s' "${open_prs_json}" | jq -r '.[0].number // empty')"
  existing_pr_url="$(printf '%s' "${open_prs_json}" | jq -r '.[0].html_url // empty')"
  if [[ "${branch_formula_sha}" == "${blob_sha}" ]]; then
    if [[ -n "${existing_pr_url}" ]]; then
      if [[ "${draft}" == "false" ]]; then
        existing_pr_files="$(
          api "repos/${repository}/pulls/${existing_pr_number}/files"
        )"
        printf '%s' "${existing_pr_files}" |
          jq -e --arg path "${FORMULA_DESTINATION}" \
            'length == 1 and .[0].filename == $path' >/dev/null ||
          die "existing stable publication PR changes files beyond ${FORMULA_DESTINATION}"
      fi
      printf 'status=existing-open-pr\n'
      printf 'branch=%s\n' "${branch}"
      printf 'commit=%s\n' "${existing_commit_sha}"
      printf 'pr_url=%s\n' "${existing_pr_url}"
      exit 0
    fi

    existing_compare="$(
      api "repos/${repository}/compare/${base_branch}...${branch}"
    )"
    printf '%s' "${existing_compare}" |
      jq -e --arg path "${FORMULA_DESTINATION}" \
        '.files | length == 1 and .[0].filename == $path' >/dev/null ||
      die "existing branch ${branch} changes files beyond ${FORMULA_DESTINATION}"
    branch_exists="true"
    publication_status="created-pr-for-existing-branch"
    commit_sha="${existing_commit_sha}"
  else
    die "branch ${branch} already exists without a matching open publication PR"
  fi
fi

if [[ "${branch_exists}" == "false" ]]; then
  tree_payload="$(
    jq -n \
      --arg base_tree "${base_tree}" \
      --arg blob_sha "${blob_sha}" \
      --arg path "${FORMULA_DESTINATION}" \
      '{
        base_tree: $base_tree,
        tree: [{path: $path, mode: "100644", type: "blob", sha: $blob_sha}]
      }'
  )"
  tree_sha="$(
    printf '%s' "${tree_payload}" |
      api "repos/${repository}/git/trees" --method POST --input - |
      jq -er '.sha'
  )"
  commit_message="Update tunnel-client Homebrew formula to ${release_version}"
  commit_payload="$(
    jq -n \
      --arg message "${commit_message}" \
      --arg tree "${tree_sha}" \
      --arg parent "${base_sha}" \
      '{message: $message, tree: $tree, parents: [$parent]}'
  )"
  commit_sha="$(
    printf '%s' "${commit_payload}" |
      api "repos/${repository}/git/commits" --method POST --input - |
      jq -er '.sha'
  )"
  ref_payload="$(
    jq -n \
      --arg ref "refs/heads/${branch}" \
      --arg sha "${commit_sha}" \
      '{ref: $ref, sha: $sha}'
  )"
  printf '%s' "${ref_payload}" |
    api "repos/${repository}/git/refs" --method POST --input - >/dev/null
fi

pr_title="[tunnel-client] update Homebrew formula to ${release_version}"
pr_body="$(
  cat <<EOF
Automated by tunnel-client release automation.

Source release: \`${release_tag}\`
Formula path: \`${FORMULA_DESTINATION}\`

This PR was generated from the release checksum manifest. Stable release PRs
contain only the generated Formula file; draft PRs are reserved for explicit
test publishes.
EOF
)"
pr_payload="$(
  jq -n \
    --arg title "${pr_title}" \
    --arg head "${branch}" \
    --arg base "${base_branch}" \
    --arg body "${pr_body}" \
    --argjson draft "${draft}" \
    '{title: $title, head: $head, base: $base, body: $body, draft: $draft}'
)"
pr_url="$(
  printf '%s' "${pr_payload}" |
    api "repos/${repository}/pulls" --method POST --input - |
    jq -er '.html_url'
)"

printf 'status=%s\n' "${publication_status}"
printf 'branch=%s\n' "${branch}"
printf 'commit=%s\n' "${commit_sha}"
printf 'pr_url=%s\n' "${pr_url}"
