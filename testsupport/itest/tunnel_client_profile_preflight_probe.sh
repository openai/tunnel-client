#!/usr/bin/env bash

set -euo pipefail

if (($# < 3)); then
  printf 'usage: %s <client-path> <profile-file> <preflight-base-url> [client-args...]\n' "$0" >&2
  exit 1
fi

client_path="$1"
profile_file="$2"
preflight_base_url="$3"
shift 3

startup_timeout_seconds="${STARTUP_TIMEOUT_SECONDS:-120.0}"
disconnect_timeout_seconds="${DISCONNECT_TIMEOUT_SECONDS:-30.0}"
client_pid=""
tmpdir=""
health_url_file=""
health_unix_socket=""
control_plane_socket_path="${TUNNEL_INTEGRATION_TUNNEL_SERVICE_SOCKET_PATH:-}"

stop_client() {
  if [[ -n "$client_pid" ]] && kill -0 "$client_pid" 2>/dev/null; then
    kill "$client_pid" 2>/dev/null || true
    wait "$client_pid" 2>/dev/null || true
  fi
  client_pid=""
}

cleanup() {
  stop_client
  if [[ -n "$tmpdir" && -d "$tmpdir" ]]; then
    rm -rf "$tmpdir"
  fi
}

profile_tunnel_id() {
  local path="$1"

  awk '
    $1 == "control_plane:" { in_control_plane = 1; next }
    in_control_plane && $0 ~ /^[^[:space:]]/ { in_control_plane = 0 }
    in_control_plane && $1 == "tunnel_id:" { print $2; found = 1; exit }
    END {
      if (!found) {
        exit 1
      }
    }
  ' "$path"
}

log_event() {
  printf '[%s] %s\n' "$(date -u +"%Y-%m-%dT%H:%M:%SZ")" "$*" >&2
}

request_ok() {
  local url="$1"
  local timeout_seconds="$2"
  shift 2

  local -a curl_args=(
    --silent
    --show-error
    --output /dev/null
    --write-out '%{http_code}'
    --max-time "$timeout_seconds"
  )
  if [[ -n "$control_plane_socket_path" ]]; then
    curl_args+=(--unix-socket "$control_plane_socket_path")
  fi
  while (($# > 0)); do
    curl_args+=(-H "$1")
    shift
  done

  local status=""
  status="$(curl "${curl_args[@]}" "$url" 2>/dev/null || true)"
  [[ "$status" == 2* ]]
}

request_json() {
  local url="$1"
  local timeout_seconds="$2"
  local payload="$3"
  shift 3

  local -a curl_args=(
    --silent
    --show-error
    --include
    --max-time "$timeout_seconds"
    --request POST
    --header "Content-Type: application/json"
    --data "$payload"
  )
  if [[ -n "$control_plane_socket_path" ]]; then
    curl_args+=(--unix-socket "$control_plane_socket_path")
  fi
  while (($# > 0)); do
    curl_args+=(-H "$1")
    shift
  done

  curl "${curl_args[@]}" "$url" 2>/dev/null || true
}

emit_http_snapshot() {
  local url="$1"
  local timeout_seconds="$2"
  shift 2

  log_event "http snapshot for $url"
  local -a curl_args=(
    --silent
    --show-error
    --include
    --max-time "$timeout_seconds"
  )
  if [[ -n "$control_plane_socket_path" ]]; then
    curl_args+=(--unix-socket "$control_plane_socket_path")
  fi
  while (($# > 0)); do
    curl_args+=(-H "$1")
    shift
  done
  curl "${curl_args[@]}" "$url" >&2 || true
}

emit_health_snapshot() {
  if [[ -z "$health_url_file" ]]; then
    log_event "health snapshot unavailable: health url file path not initialized"
    return 0
  fi
  if [[ ! -f "$health_url_file" ]]; then
    log_event "health snapshot unavailable: $health_url_file does not exist"
    return 0
  fi

  local raw_health_url=""
  raw_health_url="$(<"$health_url_file")"
  raw_health_url="${raw_health_url//$'\n'/}"
  log_event "health url file $health_url_file -> ${raw_health_url:-<empty>}"

  if [[ -z "$raw_health_url" ]]; then
    return 0
  fi

  log_event "tunnel-client health snapshot"
  "$client_path" health --url-file "$health_url_file" >&2 || true
  log_event "tunnel-client health snapshot json"
  "$client_path" health --url-file "$health_url_file" --json >&2 || true
}

wait_for_running_process() {
  local description="$1"

  if kill -0 "$client_pid" 2>/dev/null; then
    return 0
  fi

  local exit_code=0
  if wait "$client_pid"; then
    exit_code=0
  else
    exit_code=$?
  fi
  log_event "tunnel-client exited before $description (exit_code=$exit_code)"
  emit_health_snapshot
  exit 1
}

trap cleanup EXIT

tunnel_id="$(profile_tunnel_id "$profile_file")" || {
  printf 'Missing control_plane.tunnel_id in %s\n' "$profile_file" >&2
  exit 1
}
preflight_url="${preflight_base_url%/}/.well-known/oauth-protected-resource/v1/mcp/${tunnel_id}"
stopped_probe_url="${preflight_base_url%/}/v1/mcp/${tunnel_id}"
stopped_probe_payload='{"jsonrpc":"2.0","id":"stopped-client-preflight","method":"ping","params":{}}'

timeout_ceiling="${startup_timeout_seconds%.*}"
if [[ "$startup_timeout_seconds" == *.* ]]; then
  timeout_ceiling="$((timeout_ceiling + 1))"
fi
if [[ "$timeout_ceiling" -le 0 ]]; then
  timeout_ceiling=1
fi

tmp_parent="${TMPDIR:-${TEST_TMPDIR:-/tmp}}"
mkdir -p "$tmp_parent"
tmpdir="$(mktemp -d "$tmp_parent/tunnel-client-preflight-XXXXXX")"
health_url_file="$tmpdir/tunnel-client-health-url"
health_unix_socket="$tmpdir/tunnel-client-health.sock"
if [[ -n "${HEALTH_URL_FILE:-}" ]]; then
  health_url_file="$HEALTH_URL_FILE"
fi
if [[ -n "${HEALTH_UNIX_SOCKET:-}" ]]; then
  health_unix_socket="$HEALTH_UNIX_SOCKET"
fi

command=(
  "$client_path"
  run
  "--profile-file=$profile_file"
  "--health.unix-socket=$health_unix_socket"
  "--health.url-file=$health_url_file"
)

if (($# > 0)); then
  command+=("$@")
fi

"${command[@]}" &
client_pid="$!"

log_event "started tunnel-client preflight pid=$client_pid tunnel_id=$tunnel_id startup_timeout=${startup_timeout_seconds}s"
log_event "preflight target ${preflight_url}"

deadline="$((SECONDS + timeout_ceiling))"
health_url=""
while ((SECONDS < deadline)); do
  wait_for_running_process "becoming ready"
  if [[ -f "$health_url_file" ]]; then
    candidate="$(<"$health_url_file")"
    candidate="${candidate//$'\n'/}"
    if [[ -n "$candidate" ]]; then
      if [[ "$candidate" != "$health_url" ]]; then
        health_url="$candidate"
        log_event "discovered tunnel-client health url ${health_url}"
      fi
      if "$client_path" health --url-file "$health_url_file" >/dev/null 2>&1; then
        log_event "tunnel-client reported healthy and ready at ${health_url}"
        break
      fi
    fi
  fi
  sleep 0.2
done

if [[ -z "$health_url" ]] || ! "$client_path" health --url-file "$health_url_file" >/dev/null 2>&1; then
  log_event "tunnel-client did not become ready within ${startup_timeout_seconds}s"
  emit_health_snapshot
  exit 1
fi

preflight_deadline="$((SECONDS + timeout_ceiling))"
while ((SECONDS < preflight_deadline)); do
  wait_for_running_process "preflight succeeding"
  if request_ok "$preflight_url" 2.0 "X-OPENAI-SKIP-AUTH: true"; then
    log_event "preflight succeeded for $preflight_url"
    stop_client
    break
  fi
  sleep 0.2
done

if [[ -n "$client_pid" ]]; then
  log_event "tunnel-client preflight did not succeed before timeout"
  emit_health_snapshot
  emit_http_snapshot "$preflight_url" 2.0 "X-OPENAI-SKIP-AUTH: true"
  exit 1
fi

disconnect_timeout_ceiling="${disconnect_timeout_seconds%.*}"
if [[ "$disconnect_timeout_seconds" == *.* ]]; then
  disconnect_timeout_ceiling="$((disconnect_timeout_ceiling + 1))"
fi
if [[ "$disconnect_timeout_ceiling" -le 0 ]]; then
  disconnect_timeout_ceiling=1
fi

disconnect_deadline="$((SECONDS + disconnect_timeout_ceiling))"
while ((SECONDS < disconnect_deadline)); do
  stopped_probe_response="$(
    request_json \
      "$stopped_probe_url" \
      2.5 \
      "$stopped_probe_payload" \
      "X-OPENAI-SKIP-AUTH: true"
  )"
  if [[ "$stopped_probe_response" == *"HTTP/"*" 404 "* ]] &&
    [[ "$stopped_probe_response" == *"tunnel_client_not_connected"* ]]; then
    log_event "stopped-client probe reached tunnel_client_not_connected for $stopped_probe_url"
    exit 0
  fi

  log_event "stopped-client probe not ready for $stopped_probe_url"
  printf '%s\n' "$stopped_probe_response" >&2
  sleep 0.2
done

log_event "stopped-client probe did not reach tunnel_client_not_connected before timeout"
emit_health_snapshot
emit_http_snapshot "$stopped_probe_url" 2.5 "X-OPENAI-SKIP-AUTH: true"
exit 1
