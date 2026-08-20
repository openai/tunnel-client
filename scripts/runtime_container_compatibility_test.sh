#!/usr/bin/env bash
set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"

usage() {
  cat <<'EOF'
Usage:
  runtime_container_compatibility_test.sh --flavor runtime|runtime-cloudflared --image IMAGE [--skip-if-unavailable]

Deployment-smokes the release image as a non-root, read-only container with
mounted profile and secret files. It verifies both the image's default
ENTRYPOINT and an explicit Kubernetes-style command override against
intentionally unreachable local endpoints. It does not compare behavior with
the full client, require /readyz readiness, or contact Cloudflare.
EOF
}

die() {
  echo "runtime_container_compatibility_test.sh: $*" >&2
  exit 1
}

skip_or_die() {
  local message="$1"
  if [[ "${skip_if_unavailable}" == "true" ]]; then
    echo "SKIP: ${message}"
    exit 0
  fi
  die "${message}"
}

flavor=""
image=""
skip_if_unavailable="false"

while [[ $# -gt 0 ]]; do
  case "$1" in
    --flavor)
      [[ $# -ge 2 ]] || die "--flavor requires a value"
      flavor="$2"
      shift 2
      ;;
    --image)
      [[ $# -ge 2 ]] || die "--image requires a value"
      image="$2"
      shift 2
      ;;
    --skip-if-unavailable)
      skip_if_unavailable="true"
      shift
      ;;
    -h|--help)
      usage
      exit 0
      ;;
    *)
      die "unknown argument: $1"
      ;;
  esac
done

case "${flavor}" in
  runtime|runtime-cloudflared) ;;
  *) die "--flavor must be runtime or runtime-cloudflared" ;;
esac
[[ -n "${image}" ]] || die "--image is required"

command -v docker >/dev/null 2>&1 || skip_or_die "Docker CLI is unavailable"
docker info >/dev/null 2>&1 || skip_or_die "Docker daemon is unavailable"
docker image inspect "${image}" >/dev/null 2>&1 ||
  die "image does not exist locally: ${image}"
command -v curl >/dev/null 2>&1 || die "curl is required"

binary_name="tunnel-client-runtime"
if [[ "${flavor}" == "runtime-cloudflared" ]]; then
  binary_name="tunnel-client-runtime-cloudflared"
fi

"${SCRIPT_DIR}/check_runtime_image_contents.sh" --flavor "${flavor}" --image "${image}"

tmp_dir="$(mktemp -d "${TMPDIR:-/tmp}/tunnel-client-runtime-container.XXXXXX")"
profile_dir="${tmp_dir}/profile"
secret_dir="${tmp_dir}/secrets"
profile_path="${profile_dir}/profile.yaml"
secret_path="${secret_dir}/control-plane-api-key"
container_id=""
cleanup() {
  if [[ -n "${container_id}" ]]; then
    docker rm -f "${container_id}" >/dev/null 2>&1 || true
  fi
  chmod u+w "${profile_dir}" "${secret_dir}" >/dev/null 2>&1 || true
  rm -rf "${tmp_dir}"
}
trap cleanup EXIT
mkdir -p "${profile_dir}" "${secret_dir}"

cat >"${profile_path}" <<'EOF'
config_version: 1
control_plane:
  base_url: http://127.0.0.1:1
  tunnel_id: tunnel_aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa
  api_key: file:/run/secrets/control-plane-api-key
mcp:
  server_urls:
    - channel: main
      url: http://127.0.0.1:1
health:
  listen_addr: 0.0.0.0:8080
admin_ui:
  open_browser: false
log:
  level: error
  format: struct-text
EOF
printf '%s\n' 'test-only-control-plane-api-key' >"${secret_path}"
chmod 0555 "${profile_dir}" "${secret_dir}"
chmod 0444 "${profile_path}" "${secret_path}"

common_args=(
  --read-only
  --cap-drop=ALL
  --security-opt=no-new-privileges
  --pids-limit=64
  --tmpfs /tmp:rw,noexec,nosuid,nodev,size=16m
  --mount "type=bind,src=${profile_dir},dst=/etc/tunnel-client,readonly"
  --mount "type=bind,src=${secret_dir},dst=/run/secrets,readonly"
)

docker run --rm "${common_args[@]}" --entrypoint /bin/sh "${image}" -c 'test "$(id -u)" = "65532" && test -r /etc/tunnel-client/profile.yaml && ! test -w /etc/tunnel-client/profile.yaml && test -r /run/secrets/control-plane-api-key && ! test -w /run/secrets/control-plane-api-key'

run_health_surface() {
  local label="$1"
  shift
  local host_port=""
  local health_url=""
  local ready="false"
  local ready_status=""
  local ui_status=""
  local exit_code=""

  container_id="$(
    docker run -d "${common_args[@]}" -p 127.0.0.1::8080 "$@"
  )"

  host_port="$(docker port "${container_id}" 8080/tcp | awk -F: 'END {print $NF}')"
  [[ -n "${host_port}" ]] || die "${label}: container did not publish the health port"
  health_url="http://127.0.0.1:${host_port}"

  for _ in $(seq 1 100); do
    if curl --noproxy '*' --silent --show-error --fail "${health_url}/healthz" >/dev/null 2>&1; then
      ready="true"
      break
    fi
    if [[ "$(docker inspect -f '{{.State.Running}}' "${container_id}")" != "true" ]]; then
      docker logs "${container_id}" >&2 || true
      die "${label}: container exited before health became available"
    fi
    sleep 0.1
  done
  if [[ "${ready}" != "true" ]]; then
    docker logs "${container_id}" >&2 || true
    die "${label}: container health endpoint did not become available"
  fi

  curl --noproxy '*' --silent --show-error --fail "${health_url}/healthz" >/dev/null
  curl --noproxy '*' --silent --show-error --fail "${health_url}/metrics" >/dev/null
  ready_status="$(curl --noproxy '*' --silent --output /dev/null --write-out '%{http_code}' "${health_url}/readyz")"
  [[ "${ready_status}" == "200" || "${ready_status}" == "503" ]] ||
    die "${label}: unexpected /readyz status: ${ready_status}"
  ui_status="$(curl --noproxy '*' --silent --output /dev/null --write-out '%{http_code}' "${health_url}/ui")"
  [[ "${ui_status}" == "404" ]] ||
    die "${label}: unexpected /ui status: ${ui_status}"

  docker stop --time 10 "${container_id}" >/dev/null
  exit_code="$(docker inspect -f '{{.State.ExitCode}}' "${container_id}")"
  [[ "${exit_code}" == "0" ]] || {
    docker logs "${container_id}" >&2 || true
    die "${label}: container did not exit cleanly after SIGTERM (exit ${exit_code})"
  }
  docker rm "${container_id}" >/dev/null
  container_id=""
}

run_health_surface "default entrypoint" "${image}" --config /etc/tunnel-client/profile.yaml
run_health_surface "explicit command override" --entrypoint "/usr/bin/${binary_name}" "${image}" run --config /etc/tunnel-client/profile.yaml

echo "${flavor} container deployment smoke: passed"
