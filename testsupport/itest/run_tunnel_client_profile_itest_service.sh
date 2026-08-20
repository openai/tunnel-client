#!/usr/bin/env bash

set -euo pipefail

client_path=""
profile_file=""
dev_proxy=false
dev_proxy_backend=""
dev_proxy_queue_backend=""
dev_proxy_listen=""
dev_proxy_listen_unix_socket=""
dev_proxy_url_file=""
dev_proxy_health_url_file=""
declare -a client_args=()
declare -a runfile_env=()

resolve_runfile_path() {
  local value="$1"

  if [[ "$value" = /* && -e "$value" ]]; then
    printf '%s\n' "$value"
    return 0
  fi

  if [[ -e "$value" ]]; then
    printf '%s\n' "$value"
    return 0
  fi

  local base_dir=""
  for base_dir in "${RUNFILES_DIR:-}" "${TEST_SRCDIR:-}"; do
    if [[ -n "$base_dir" && -e "$base_dir/$value" ]]; then
      printf '%s\n' "$base_dir/$value"
      return 0
    fi
  done

  printf 'Could not resolve runfile: %s\n' "$value" >&2
  exit 1
}

while (($# > 0)); do
  case "$1" in
    --client-path)
      client_path="$2"
      shift 2
      ;;
    --profile-file)
      profile_file="$2"
      shift 2
      ;;
    --set-env)
      export "$2=$3"
      shift 3
      ;;
    --runfile-env)
      runfile_env+=("$2")
      shift 2
      ;;
    --dev-proxy)
      dev_proxy=true
      shift
      ;;
    --dev-proxy-backend)
      dev_proxy_backend="$2"
      shift 2
      ;;
    --dev-proxy-queue-backend)
      dev_proxy_queue_backend="$2"
      shift 2
      ;;
    --dev-proxy-listen-unix-socket)
      dev_proxy_listen_unix_socket="$2"
      shift 2
      ;;
    --dev-proxy-listen)
      dev_proxy_listen="$2"
      shift 2
      ;;
    --dev-proxy-url-file)
      dev_proxy_url_file="$2"
      shift 2
      ;;
    --dev-proxy-health-url-file)
      dev_proxy_health_url_file="$2"
      shift 2
      ;;
    --)
      shift
      client_args=("$@")
      break
      ;;
    *)
      printf 'Unexpected argument: %s\n' "$1" >&2
      exit 1
      ;;
  esac
done

if [[ -z "$client_path" || -z "$profile_file" ]]; then
  printf 'Both --client-path and --profile-file are required\n' >&2
  exit 1
fi

if ((${#runfile_env[@]} > 0)); then
  for assignment in "${runfile_env[@]}"; do
    env_name="${assignment%%=*}"
    env_value="${assignment#*=}"
    if [[ -z "$env_name" || "$env_name" == "$assignment" ]]; then
      printf 'Invalid --runfile-env assignment: %s\n' "$assignment" >&2
      exit 1
    fi
    export "$env_name=$(resolve_runfile_path "$env_value")"
  done
fi

resolved_client_path="$(resolve_runfile_path "$client_path")"
resolved_profile_path="$(resolve_runfile_path "$profile_file")"

if [[ "$dev_proxy" == "true" ]]; then
  if [[ -z "$dev_proxy_backend" || -z "$dev_proxy_queue_backend" || -z "$dev_proxy_url_file" ]]; then
    printf 'Dev proxy mode requires backend, queue backend, and URL file\n' >&2
    exit 1
  fi
  if [[ -n "$dev_proxy_listen" && -n "$dev_proxy_listen_unix_socket" ]] || [[ -z "$dev_proxy_listen" && -z "$dev_proxy_listen_unix_socket" ]]; then
    printf 'Dev proxy mode requires exactly one of TCP listen address or Unix listen socket\n' >&2
    exit 1
  fi
  command=(
    "$resolved_client_path"
    dev
    proxy
    "--profile-file=$resolved_profile_path"
    "--backend=$dev_proxy_backend"
    "--engine-queue-backend=$dev_proxy_queue_backend"
    "--url-file=$dev_proxy_url_file"
    "--print-json"
  )
  if [[ -n "$dev_proxy_listen" ]]; then
    command+=("--listen=$dev_proxy_listen")
  else
    command+=("--listen-unix-socket=$dev_proxy_listen_unix_socket")
  fi
  if [[ -n "$dev_proxy_health_url_file" ]]; then
    command+=("--health-url-file=$dev_proxy_health_url_file")
  fi
else
  command=(
    "$resolved_client_path"
    run
    "--profile-file=$resolved_profile_path"
  )
fi

if ((${#client_args[@]} > 0)); then
  command+=("${client_args[@]}")
fi

exec "${command[@]}"
