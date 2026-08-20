#!/usr/bin/env bash
set -euo pipefail

flavor="${1:-}"
case "${flavor}" in
  runtime|runtime-cloudflared) ;;
  *)
    echo "usage: runtime_source_export_rebuild_test.sh runtime|runtime-cloudflared" >&2
    exit 1
    ;;
esac

script_dir="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
if [[ -n "${TEST_SRCDIR:-}" && -n "${TEST_WORKSPACE:-}" ]]; then
  script_dir="${TEST_SRCDIR}/${TEST_WORKSPACE}/api/tunnel-client/scripts"
elif [[ -x "${script_dir}/scripts/build_runtime_source_export.sh" ]]; then
  script_dir="${script_dir}/scripts"
fi
[[ -x "${script_dir}/build_runtime_source_export.sh" ]] || {
  echo "runtime source-export build script is missing from runfiles" >&2
  exit 1
}
if [[ -n "${BAZEL_TEST:-}" && -n "${TEST_TMPDIR:-}" ]]; then
  output_dir="$(mktemp -d "${TEST_TMPDIR}/tunnel-client-runtime-source-test.XXXXXX")"
else
  output_dir="$(mktemp -d "${TMPDIR:-/tmp}/tunnel-client-runtime-source-test.XXXXXX")"
fi
trap 'rm -rf "${output_dir}"' EXIT

"${script_dir}/build_runtime_source_export.sh" \
  --flavor "${flavor}" \
  --version v0.0.0-test \
  --output-dir "${output_dir}"

archive="${output_dir}/tunnel-client-runtime-source-v0.0.0-test.tar.gz"
if [[ "${flavor}" == "runtime-cloudflared" ]]; then
  archive="${output_dir}/tunnel-client-runtime-cloudflared-source-v0.0.0-test.tar.gz"
fi
"${script_dir}/verify_runtime_source_export.sh" \
  --flavor "${flavor}" \
  --archive "${archive}"
