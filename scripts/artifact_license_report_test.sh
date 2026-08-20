#!/usr/bin/env bash
set -euo pipefail

resolved_script_path="$(python3 - "${BASH_SOURCE[0]}" <<'PY'
import os
import sys

print(os.path.realpath(sys.argv[1]))
PY
)"
script_dir="$(cd "$(dirname "${resolved_script_path}")" && pwd)"
if [[ -x "${script_dir}/scripts/build_artifact_license_report.sh" ]]; then
  script_dir="${script_dir}/scripts"
fi
report_script="${script_dir}/build_artifact_license_report.sh"

tmp_dir="$(mktemp -d "${TMPDIR:-/tmp}/tunnel-client-license-report-test.XXXXXX")"
trap 'rm -rf "${tmp_dir}"' EXIT

assert_rejected() {
  local expected_message="$1"
  shift
  if "${report_script}" "$@" >"${tmp_dir}/rejected.out" 2>&1; then
    echo "expected artifact license report invocation to fail: $*" >&2
    exit 1
  fi
  grep -F -- "${expected_message}" "${tmp_dir}/rejected.out" >/dev/null || {
    echo "artifact license report invocation failed for the wrong reason: $*" >&2
    cat "${tmp_dir}/rejected.out" >&2
    exit 1
  }
}

assert_only_platform() {
  local report="$1"
  local platform="$2"
  grep -Fx "Platforms: ${platform}" "${report}" >/dev/null
  awk -F '|' -v platform="${platform}" '
    $0 == "DEPENDENCY|TYPE|VERSION|LICENSES|LICENSE_FILE|PLATFORMS" {
      in_table = 1
      next
    }
    in_table && NF {
      saw_row = 1
      if (NF != 6 || $6 != platform) {
        print "unexpected platform row: " $0 > "/dev/stderr"
        bad = 1
      }
    }
    END {
      if (!saw_row || bad) {
        exit 1
      }
    }
  ' "${report}"
}

assert_rejected "--goos is required" --flavor runtime --goarch amd64 --output "${tmp_dir}/missing-goos.txt"
assert_rejected "--goarch is required" --flavor runtime --goos linux --output "${tmp_dir}/missing-goarch.txt"

linux_report="${tmp_dir}/runtime-linux-amd64.txt"
"${report_script}" --flavor runtime --goos linux --goarch amd64 --output "${linux_report}" >/dev/null
assert_only_platform "${linux_report}" "linux/amd64"
if grep -F "github.com/inconshreveable/mousetrap|" "${linux_report}" >/dev/null; then
  echo "linux artifact report unexpectedly retained a windows-only dependency" >&2
  exit 1
fi

windows_report="${tmp_dir}/runtime-windows-amd64.txt"
"${report_script}" --flavor runtime --goos windows --goarch amd64 --output "${windows_report}" >/dev/null
assert_only_platform "${windows_report}" "windows/amd64"
grep -F "github.com/inconshreveable/mousetrap|" "${windows_report}" >/dev/null

cloudflared_report="${tmp_dir}/runtime-cloudflared-linux-amd64.txt"
"${report_script}" --flavor runtime-cloudflared --goos linux --goarch amd64 --output "${cloudflared_report}" >/dev/null
assert_only_platform "${cloudflared_report}" "linux/amd64"
grep -F "github.com/cloudflare/backoff|" "${cloudflared_report}" >/dev/null

cloudflared_report_again="${tmp_dir}/runtime-cloudflared-linux-amd64-again.txt"
"${report_script}" --flavor runtime-cloudflared --goos linux --goarch amd64 --output "${cloudflared_report_again}" >/dev/null
cmp "${cloudflared_report}" "${cloudflared_report_again}"
