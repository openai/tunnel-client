#!/usr/bin/env bash
set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
PROJECT_DIR="$(cd "${SCRIPT_DIR}/.." && pwd)"
MANIFEST_TEMPLATE="${PROJECT_DIR}/e2e/testdata/runtime-compatibility/k8s/runtime-smoke.yaml"

usage() {
  cat <<'EOF'
Usage:
  TUNNEL_CLIENT_RUNTIME_K8S_COMPAT=1 runtime_k8s_compatibility_test.sh --flavor runtime|runtime-cloudflared --image IMAGE [--backend auto|kind|k3d] [--skip-if-unavailable]

Opt-in Kubernetes deployment smoke. It loads an already-built local image into
a disposable kind or k3d cluster, then waits for default ENTRYPOINT and
explicit command override Pods to serve /healthz from mounted profile/secret
configuration under hardened security settings. The profile intentionally uses
unreachable local endpoints; this does not compare behavior with the full
client or assert /readyz readiness.
EOF
}

die() {
  echo "runtime_k8s_compatibility_test.sh: $*" >&2
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
backend="auto"
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
    --backend)
      [[ $# -ge 2 ]] || die "--backend requires a value"
      backend="$2"
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
case "${backend}" in
  auto|kind|k3d) ;;
  *) die "--backend must be auto, kind, or k3d" ;;
esac
[[ -n "${image}" ]] || die "--image is required"
[[ -f "${MANIFEST_TEMPLATE}" ]] || die "manifest template is missing: ${MANIFEST_TEMPLATE}"
[[ "${image}" =~ ^[A-Za-z0-9][A-Za-z0-9._/:@-]*$ ]] ||
  die "--image must be a Docker image reference"

if [[ "${TUNNEL_CLIENT_RUNTIME_K8S_COMPAT:-}" != "1" ]]; then
  echo "SKIP: runtime Kubernetes deployment smoke is opt-in; set TUNNEL_CLIENT_RUNTIME_K8S_COMPAT=1"
  exit 0
fi

command -v docker >/dev/null 2>&1 || skip_or_die "Docker CLI is unavailable"
docker info >/dev/null 2>&1 || skip_or_die "Docker daemon is unavailable"
command -v kubectl >/dev/null 2>&1 || skip_or_die "kubectl is unavailable"
command -v python3 >/dev/null 2>&1 || die "python3 is required"
docker image inspect "${image}" >/dev/null 2>&1 ||
  die "image does not exist locally: ${image}"

if [[ "${backend}" == "auto" ]]; then
  if command -v kind >/dev/null 2>&1; then
    backend="kind"
  elif command -v k3d >/dev/null 2>&1; then
    backend="k3d"
  else
    skip_or_die "no supported Kubernetes backend is installed"
  fi
elif ! command -v "${backend}" >/dev/null 2>&1; then
  skip_or_die "${backend} is unavailable"
fi

binary_name="tunnel-client-runtime"
if [[ "${flavor}" == "runtime-cloudflared" ]]; then
  binary_name="tunnel-client-runtime-cloudflared"
fi

cluster_name="tc-runtime-${RANDOM}-${RANDOM}"
resource_name="tc-runtime-${RANDOM}"
kube_context=""
tmp_dir="$(mktemp -d "${TMPDIR:-/tmp}/tunnel-client-runtime-k8s.XXXXXX")"
manifest="${tmp_dir}/runtime-smoke.yaml"
cluster_created="false"
cleanup() {
  if [[ "${cluster_created}" == "true" ]]; then
    if [[ "${backend}" == "kind" ]]; then
      kind delete cluster --name "${cluster_name}" >/dev/null 2>&1 || true
    else
      k3d cluster delete "${cluster_name}" >/dev/null 2>&1 || true
    fi
  fi
  rm -rf "${tmp_dir}"
}
trap cleanup EXIT

python3 - "${MANIFEST_TEMPLATE}" "${manifest}" "${image}" "${binary_name}" "${resource_name}" <<'PY'
import pathlib
import sys

template, destination, image, binary, name = sys.argv[1:]
content = pathlib.Path(template).read_text(encoding="utf-8")
for marker, value in (
    ("__IMAGE__", image),
    ("__BINARY__", binary),
    ("__NAME__", name),
):
    if marker not in content:
        raise SystemExit(f"manifest template is missing {marker}")
    content = content.replace(marker, value)
pathlib.Path(destination).write_text(content, encoding="utf-8")
PY

if [[ "${backend}" == "kind" ]]; then
  cluster_created="true"
  kind create cluster --name "${cluster_name}" --wait 90s
  kind load docker-image --name "${cluster_name}" "${image}"
  kube_context="kind-${cluster_name}"
else
  cluster_created="true"
  k3d cluster create "${cluster_name}" --wait
  k3d image import --cluster "${cluster_name}" "${image}"
  kube_context="k3d-${cluster_name}"
fi

kubectl --context "${kube_context}" apply -f "${manifest}"

for suffix in default override; do
  pod="${resource_name}-${suffix}"
  if ! kubectl --context "${kube_context}" wait --for=condition=Ready "pod/${pod}" --timeout=90s; then
    kubectl --context "${kube_context}" describe "pod/${pod}" >&2 || true
    kubectl --context "${kube_context}" logs "pod/${pod}" >&2 || true
    die "Kubernetes ${suffix} smoke Pod did not become ready"
  fi
done

echo "${flavor} Kubernetes deployment smoke (${backend}): passed"
