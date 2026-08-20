#!/bin/sh
set -eu

case "$0" in
  */*) script_dir=${0%/*} ;;
  *) script_dir=. ;;
esac
plugin_root=$(CDPATH= cd -- "$script_dir/.." && pwd)
tunnel_client_bin=""
codex_home=""
attempts=""

append_attempt() {
  if [ -z "$attempts" ]; then
    attempts="- $1"
  else
    attempts="$attempts
- $1"
  fi
}

is_executable_file() {
  [ -f "$1" ] && [ -x "$1" ]
}

find_bazel_client_binary() {
  search_root=$1
  [ -d "$search_root/bazel-bin" ] || return 1
  find_bin=find
  if [ -x /usr/bin/find ]; then
    find_bin=/usr/bin/find
  fi
  found=$(
    "$find_bin" "$search_root/bazel-bin" -type f \( -name client -o -name client.exe \) -print 2>/dev/null |
      while IFS= read -r candidate; do
        if [ "${candidate#*/cmd/client/}" != "$candidate" ] && is_executable_file "$candidate"; then
          printf '%s\n' "$candidate"
          break
        fi
      done
  )
  [ -n "$found" ] || return 1
  printf '%s\n' "$found"
}

find_adjacent_binary() {
  search_root=$plugin_root
  while [ -n "$search_root" ]; do
    for candidate in \
      "$search_root/tunnel-client" \
      "$search_root/tunnel-client.exe" \
      "$search_root/bin/tunnel-client" \
      "$search_root/bin/tunnel-client.exe" \
      "$search_root/bazel-bin/cmd/client/client" \
      "$search_root/bazel-bin/cmd/client/client.exe" \
      "$search_root/bazel-bin/api/tunnel-client/cmd/client/client" \
      "$search_root/bazel-bin/api/tunnel-client/cmd/client/client.exe"
    do
      if is_executable_file "$candidate"; then
        printf '%s\n' "$candidate"
        return 0
      fi
    done
    if find_bazel_client_binary "$search_root"; then
      return 0
    fi
    case "$search_root" in
      */*) parent=${search_root%/*} ;;
      *) parent=. ;;
    esac
    [ "$parent" = "$search_root" ] && break
    search_root=$parent
  done
  return 1
}

print_help() {
  cat <<'EOF'
Usage: install_plugin.sh [--tunnel-client-bin /path/to/tunnel-client] [--codex-home /path/to/codex-home]

Delegates to the selected tunnel-client binary:
  tunnel-client codex plugin install [--codex-home ...]

Binary discovery order:
  --tunnel-client-bin
  TUNNEL_CLIENT_BIN
  adjacent local build outputs
  PATH
EOF
}

while [ $# -gt 0 ]; do
  case "$1" in
    --help|-h)
      print_help
      exit 0
      ;;
    --tunnel-client-bin)
      [ $# -ge 2 ] || { echo "error: --tunnel-client-bin requires a value" >&2; exit 2; }
      if is_executable_file "$2"; then
        tunnel_client_bin=$2
      else
        append_attempt "--tunnel-client-bin: $2 was not an executable file"
      fi
      shift 2
      ;;
    --codex-home)
      [ $# -ge 2 ] || { echo "error: --codex-home requires a value" >&2; exit 2; }
      codex_home=$2
      shift 2
      ;;
    *)
      echo "error: unsupported argument: $1" >&2
      exit 2
      ;;
  esac
done

if [ -z "$tunnel_client_bin" ]; then
  append_attempt "--tunnel-client-bin: not provided"
fi

if [ -z "$tunnel_client_bin" ] && [ -n "${TUNNEL_CLIENT_BIN:-}" ]; then
  if is_executable_file "$TUNNEL_CLIENT_BIN"; then
    tunnel_client_bin=$TUNNEL_CLIENT_BIN
  else
    append_attempt "TUNNEL_CLIENT_BIN: $TUNNEL_CLIENT_BIN was not an executable file"
  fi
elif [ -z "$tunnel_client_bin" ]; then
  append_attempt "TUNNEL_CLIENT_BIN: not set"
fi

if [ -z "$tunnel_client_bin" ]; then
  adjacent_bin=$(find_adjacent_binary || true)
  if [ -n "$adjacent_bin" ]; then
    tunnel_client_bin=$adjacent_bin
  else
    append_attempt "adjacent build outputs: no executable tunnel-client binary found next to the plugin"
  fi
fi

if [ -z "$tunnel_client_bin" ]; then
  path_candidate=$(command -v tunnel-client 2>/dev/null || true)
  if [ -z "$path_candidate" ]; then
    path_candidate=$(command -v tunnel-client.exe 2>/dev/null || true)
  fi
  if [ -n "$path_candidate" ]; then
    tunnel_client_bin=$path_candidate
  else
    append_attempt "PATH: no tunnel-client executable found"
  fi
fi

if [ -z "$tunnel_client_bin" ]; then
  printf 'error: tunnel-client was not found.\n\n' >&2
  printf 'Discovery methods tried:\n%s\n\n' "$attempts" >&2
  printf '%s\n' \
    'Next steps:' \
    '- Download a release binary from https://github.com/openai/tunnel-client/releases/latest' \
    '- Or clone and build from source from https://github.com/openai/tunnel-client:' \
    '  git clone https://github.com/openai/tunnel-client.git' \
    '  cd tunnel-client' \
    '  go build -o bin/tunnel-client ./cmd/client' \
    '  # Windows: go build -o bin/tunnel-client.exe ./cmd/client' \
    '- Then rerun this installer with --tunnel-client-bin /path/to/tunnel-client' >&2
  exit 2
fi

if [ -n "$codex_home" ]; then
  exec "$tunnel_client_bin" codex plugin install --codex-home "$codex_home"
fi

exec "$tunnel_client_bin" codex plugin install
