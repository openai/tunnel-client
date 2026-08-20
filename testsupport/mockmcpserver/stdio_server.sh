#!/usr/bin/env bash
set -euo pipefail

while IFS= read -r line; do
  [[ -z "$line" ]] && continue

  id=""
  if [[ $line =~ \"id\"[[:space:]]*:[[:space:]]*\"([^\"]+)\" ]]; then
    id="\"${BASH_REMATCH[1]}\""
  elif [[ $line =~ \"id\"[[:space:]]*:[[:space:]]*([0-9]+) ]]; then
    id="${BASH_REMATCH[1]}"
  else
    continue
  fi

  case "$line" in
    *\"initialize\"*)
      printf '{"jsonrpc":"2.0","id":%s,"result":{"protocolVersion":"2024-11-05","capabilities":{"tools":{}},"serverInfo":{"name":"bash","version":"0.0"}}}\n' "$id"
      ;;
    *\"tools/list\"*)
      printf '{"jsonrpc":"2.0","id":%s,"result":{"tools":[{"name":"hello","description":"hello","inputSchema":{"type":"object","properties":{"name":{"type":"string"}},"required":["name"]}}]}}\n' "$id"
      ;;
    *\"tools/call\"*)
      name=""
      request_id=""
      if [[ $line =~ \"arguments\"[[:space:]]*:[[:space:]]*\{[^\}]*\"name\"[[:space:]]*:[[:space:]]*\"([^\"]*)\" ]]; then
        name="${BASH_REMATCH[1]}"
      fi
      if [[ $line =~ \"request_id\"[[:space:]]*:[[:space:]]*\"([^\"]*)\" ]]; then
        request_id="${BASH_REMATCH[1]}"
      fi
      if [[ -n "${MOCK_MCP_INVOCATION_LOG:-}" ]]; then
        printf '%s\n' "$request_id" >> "$MOCK_MCP_INVOCATION_LOG"
      fi
      if [[ -n "${MOCK_MCP_DROP_RESPONSE_NAME:-}" && "$name" == "$MOCK_MCP_DROP_RESPONSE_NAME" ]]; then
        continue
      fi
      if [[ -n "${MOCK_MCP_SERVER_REQUEST_BEFORE_RESPONSE_NAME:-}" && "$name" == "$MOCK_MCP_SERVER_REQUEST_BEFORE_RESPONSE_NAME" ]]; then
        printf '{"jsonrpc":"2.0","id":"late-server-request","method":"sampling/createMessage","params":{"messages":[],"maxTokens":1}}\n'
      fi
      message="hello $name"
      printf '{"jsonrpc":"2.0","id":%s,"result":{"content":[{"type":"text","text":"%s"}],"structuredContent":{"message":"%s"}}}\n' "$id" "$message" "$message"
      ;;
  esac
done
