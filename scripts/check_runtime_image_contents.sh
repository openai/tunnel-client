#!/usr/bin/env bash
set -euo pipefail

usage() {
  cat <<'EOF'
Usage:
  ./scripts/check_runtime_image_contents.sh \
    --flavor runtime|runtime-cloudflared \
    --image <local-image-reference>

Checks a locally built runtime image for its non-root identity and payload
allowlist. The image must already exist locally.
EOF
}

die() {
  echo "check_runtime_image_contents.sh: $*" >&2
  exit 1
}

flavor=""
image=""
while [[ $# -gt 0 ]]; do
  case "$1" in
    --flavor)
      flavor="${2:-}"
      shift 2
      ;;
    --image)
      image="${2:-}"
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

case "${flavor}" in
  runtime|runtime-cloudflared) ;;
  *) die "--flavor must be runtime or runtime-cloudflared" ;;
esac
[[ -n "${image}" ]] || die "--image is required"
command -v docker >/dev/null 2>&1 || die "docker is required"

user="$(docker image inspect --format '{{.Config.User}}' "${image}")"
[[ "${user}" == "65532:65532" ]] ||
  die "image user must be 65532:65532, got ${user}"

entrypoint="$(docker image inspect --format '{{json .Config.Entrypoint}}' "${image}")"
if [[ "${flavor}" == "runtime" ]]; then
  [[ "${entrypoint}" == *"tunnel-client-runtime"* ]] ||
    die "runtime image entrypoint is not the runtime binary"
else
  [[ "${entrypoint}" == *"tunnel-client-runtime-cloudflared"* ]] ||
    die "runtime-cloudflared image entrypoint is not the companion runtime binary"
fi

container_id="$(docker create "${image}")"
tmp_dir="$(mktemp -d "${TMPDIR:-/tmp}/tunnel-client-runtime-image.XXXXXX")"
cleanup() {
  docker rm -f "${container_id}" >/dev/null 2>&1 || true
  rm -rf "${tmp_dir}"
}
trap cleanup EXIT
docker export "${container_id}" >"${tmp_dir}/rootfs.tar"
tar -tf "${tmp_dir}/rootfs.tar" | sed 's#^\./##' | LC_ALL=C sort >"${tmp_dir}/members.txt"

python3 - "${flavor}" "${tmp_dir}/members.txt" <<'PY'
import pathlib
import sys

flavor = sys.argv[1]
members = {
    line.rstrip("\n")
    for line in pathlib.Path(sys.argv[2]).read_text(encoding="utf-8").splitlines()
    if line.rstrip("\n") and not line.rstrip("\n").endswith("/")
}
expected = {"usr/bin/tunnel-client-runtime"}
if flavor == "runtime-cloudflared":
    expected = {
        "usr/bin/tunnel-client-runtime-cloudflared",
        "usr/bin/cloudflared",
        "usr/share/tunnel-client/cloudflared-manifest.json",
    }

application_scope = {
    member
    for member in members
    if member.startswith("usr/bin/tunnel-client")
    or member == "usr/bin/cloudflared"
    or member.startswith("usr/share/tunnel-client/")
}
if application_scope != expected:
    missing = sorted(expected - application_scope)
    unexpected = sorted(application_scope - expected)
    raise SystemExit(
        "image application payload allowlist mismatch: "
        + f"missing={missing} unexpected={unexpected}"
    )
if any(member.startswith("app/") for member in members):
    raise SystemExit("image /app workdir contains unexpected payload files")
PY

if [[ "${flavor}" == "runtime" ]]; then
  grep -Eq '(^|/)usr/bin/tunnel-client-runtime$' "${tmp_dir}/members.txt" ||
    die "image is missing runtime binary"
  if grep -Eq '(^|/)usr/bin/cloudflared$' "${tmp_dir}/members.txt"; then
    die "runtime image unexpectedly contains companion binary"
  fi
else
  grep -Eq '(^|/)usr/bin/tunnel-client-runtime-cloudflared$' "${tmp_dir}/members.txt" ||
    die "image is missing runtime-cloudflared binary"
  grep -Eq '(^|/)usr/bin/cloudflared$' "${tmp_dir}/members.txt" ||
    die "image is missing companion binary"
  grep -Eq '(^|/)usr/share/tunnel-client/cloudflared-manifest.json$' "${tmp_dir}/members.txt" ||
    die "image is missing companion manifest"
fi

if grep -Eq '(^|/)(adminui|plugins|localproxy|docs|examples|testsupport)(/|$)' "${tmp_dir}/members.txt"; then
  die "image contains an excluded support or development path"
fi

printf '%s image contents: passed\n' "${flavor}"
