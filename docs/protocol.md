# Tunnel client wire protocol

This document is for authors of tunnel clients in any language. It describes
the HTTP methods, headers, JSON shapes, and lifecycle used between a client and
the Secure MCP Tunnel control plane.

The machine-readable contract is [`openapi.json`](openapi.json). Use it to
generate types or validate fixtures, and use this document for behavior that
OpenAPI alone cannot express.

## Scope

A tunnel client:

1. authenticates to `https://api.openai.com`;
2. optionally fetches tunnel metadata for startup diagnostics;
3. long-polls for commands addressed to one tunnel;
4. forwards each command to the configured MCP server; and
5. posts the MCP result back to the control plane.

The canonical client endpoints are:

| Method | Path | Purpose |
| --- | --- | --- |
| `GET` | `/v1/tunnels/{tunnel_id}` | Fetch minimal startup metadata. |
| `GET` | `/v1/tunnels/{tunnel_id}/poll` | Long-poll for pending commands. |
| `POST` | `/v1/tunnels/{tunnel_id}/response` | Return the result for one command. |

Use the plural `/v1/tunnels/...` paths. Singular `/v1/tunnel/...` paths are
compatibility aliases and are not part of the contract for new clients.

## Authentication and common headers

Send the tunnel API key on every request:

```http
Authorization: Bearer <tunnel-api-key>
```

Clients should also send a stable implementation name and version. These
headers are diagnostic metadata, not feature negotiation:

```http
X-Tunnel-Client-Name: example-rust-client
X-Tunnel-Client-Version: 1.2.3
```

Treat tunnel IDs, request IDs, and shard tokens as opaque strings. Do not parse
them or infer routing from their contents.

## Poll loop

Request:

```http
GET /v1/tunnels/tunnel_123/poll?limit=25&timeout_ms=15000 HTTP/1.1
Authorization: Bearer <tunnel-api-key>
X-Tunnel-Client-Name: example-rust-client
X-Tunnel-Client-Version: 1.2.3
```

`limit` is optional and must be from `1` through `25`. It is a request hint:
if a successful response contains more commands than requested, process every
command; do not drop the excess. `timeout_ms` is an optional requested
long-poll wait in milliseconds. The service bounds the effective wait, so a
client must not assume the requested duration is exact.

`limit` controls only the requested poll batch size. It is not an execution
concurrency limit; each client chooses its own bounded concurrency.

A `204 No Content` response means the poll completed without commands. Issue
another poll. A `200 OK` response contains a JSON envelope:

```json
{
  "commands": [
    {
      "request_id": "req_123",
      "shard_token": "opaque-shard-token",
      "command_type": "jsonrpc",
      "channel": "main",
      "created_at": "2026-01-01T00:00:00Z",
      "response_timeout": "30s",
      "headers": {
        "Mcp-Session-Id": ["session_123"]
      },
      "jsonrpc": {
        "jsonrpc": "2.0",
        "id": "rpc_123",
        "method": "tools/list",
        "params": {}
      }
    }
  ]
}
```

Common command fields:

| Field | Meaning |
| --- | --- |
| `request_id` | Opaque correlation ID. Echo it as `request_id` in the response body. |
| `shard_token` | Opaque routing token. Echo it only in `X-Tunnel-Shard-Token` when posting the response. |
| `command_type` | Discriminator for the command shape. |
| `channel` | Logical MCP channel; defaults to `main` when absent. Echo it in the response body. |
| `created_at` | RFC 3339 enqueue timestamp. |
| `response_timeout` | Optional relative duration for the complete command lifecycle, anchored when the poll response is received. |
| `headers` | Multi-valued headers to apply to the MCP request. |

### Response timeout

When present, `response_timeout` is a relative duration for the complete
command lifecycle, anchored when the poll response is received. Its wire
grammar is:

```abnf
ResponseTimeout = 1*DIGIT TimeoutUnit
TimeoutUnit     = "ns" / "us" / "ms" / "s" / "m" / "h"
```

The value contains one non-negative integer and one lowercase unit. `30s`,
`4500ms`, and `0s` are valid. Fractions such as `4.5s`, signed values such as
`-1s` or `+1s`, compound values such as `1m30s`, JSON strings such as `" 1s"`
or `"1s "` that contain whitespace, exponents such as `1e3s`, unknown units
such as `30d`, and overflowing values such as `999999999999999999999999h` are
invalid. A JSON number such as `30` is also invalid because the wire value must
be a string.

An absent or JSON `null` value retains legacy no-deadline behavior. The
official Go decoder also fails open for malformed values, wrong JSON types,
unknown units, and values that overflow its duration range: the command remains
decodable and retains legacy behavior. At the contract level, a valid zero such
as `0s` represents immediate expiry.

Compatibility is per command, including when tunnel-service instances produce
different payload shapes during a mixed deployment:

| Poll command | Released official Go client without this field | Contract-aware client |
| --- | --- | --- |
| `response_timeout` omitted or `null` | Decodes normally with legacy behavior. | Decodes normally with legacy behavior. |
| Valid `response_timeout` present | `encoding/json` ignores the unknown property; legacy behavior is retained. | Decodes and validates the field; this contract-preparation release retains legacy runtime behavior. |
| Malformed, wrong-type, unknown-unit, or overflowing value present | `encoding/json` ignores the unknown property; legacy behavior is retained. | Decoding succeeds and legacy behavior is retained. |

Previously generated OpenAPI clients are outside this compatibility guarantee.
Command schemas remain open with `additionalProperties: true`, and clients must
accept unknown future command properties.

### `jsonrpc` commands

For `command_type: "jsonrpc"`, `jsonrpc` is the raw JSON-RPC request or
notification to send to the MCP server. Preserve JSON-RPC IDs and do not
reinterpret the payload as a tunnel-protocol object.

### `session_termination` commands

For `command_type: "session_termination"`, close the Streamable HTTP session
identified by the `Mcp-Session-Id` header. The command has no `jsonrpc` field.
After closing the session, post a response with
`resp_type: "session_termination_response"`, typically `resp_code: 204`, and
no `resp_json`.

### Future command types

Dispatch on `command_type`, not on field presence. If a client receives an
unknown command type, it must not reinterpret it as JSON-RPC. Log the
unsupported discriminator with the opaque `request_id`, continue serving
known commands, and keep polling.

## Posting a response

Every response POST must include the `shard_token` from the polled command:

```http
POST /v1/tunnels/tunnel_123/response HTTP/1.1
Authorization: Bearer <tunnel-api-key>
Content-Type: application/json
X-Tunnel-Shard-Token: opaque-shard-token
```

The shard token belongs in the HTTP header only; never put it in the JSON
body. `X-Client-Request-Id` is optional diagnostic correlation when the client
has one.

JSON-RPC result example:

```json
{
  "request_id": "req_123",
  "channel": "main",
  "resp_json": {
    "jsonrpc": "2.0",
    "id": "rpc_123",
    "result": {
      "tools": []
    }
  },
  "resp_headers": {
    "Content-Type": ["application/json"]
  },
  "resp_code": 200,
  "resp_type": "jsonrpc_response"
}
```

Response fields:

| Field | Required | Meaning |
| --- | --- | --- |
| `request_id` | yes | The polled command's opaque request ID. |
| `channel` | no | Logical channel; send the command's channel when present. |
| `resp_json` | depends | JSON-RPC payload; omit for acknowledgment-only responses. |
| `resp_headers` | no | Multi-valued upstream MCP response headers. |
| `resp_code` | yes | HTTP-style status code from the MCP interaction. |
| `resp_type` | no | Payload discriminator; defaults to `jsonrpc_response`. |

Supported `resp_type` values:

| Value | Final? | Use |
| --- | --- | --- |
| `jsonrpc_response` | yes | Terminal JSON-RPC result or error with `resp_json`. |
| `jsonrpc_notify` | no | Intermediate JSON-RPC notification with `resp_json`. |
| `notify_ack` | yes | Terminal acknowledgment for a JSON-RPC notification that has no result. |
| `session_termination_response` | yes | Terminal acknowledgment after closing an MCP session. |

For a JSON-RPC request with an ID, a client may post zero or more
`jsonrpc_notify` payloads while processing the command, followed by one
terminal `jsonrpc_response`. Every POST for the command must reuse its
`request_id` in the body and its `shard_token` in the
`X-Tunnel-Shard-Token` header, and should echo its `channel` when present. A
`jsonrpc_notify` does not complete the command. `notify_ack` is the terminal
acknowledgment for a JSON-RPC notification without an ID; it is not a progress
event.

A successful POST returns:

```json
{
  "status": "ok"
}
```

## Errors, retries, and concurrency

- Keep polling until the process is stopped; `204` is normal, not an error.
- Retry transient network failures, `429`, and `5xx` with bounded backoff.
- Treat `401` and `403` as authentication or authorization failures that need
  operator action instead of a tight retry loop.
- A response POST can return `404` when the request has already been fulfilled
  or is no longer pending. Treat that command as terminal and do not replay
  the MCP operation.
- A client chooses its own bounded execution concurrency. Concurrent command
  processing does not require overlapping poll requests; one poll loop can
  submit returned commands to workers.
- Commands may complete in any order. Correlation is always per command: pair
  each `request_id`, `channel`, and `shard_token` from one poll item with every
  response for that item.
- Preserve multi-valued headers. Do not collapse repeated values into a
  comma-separated string unless the MCP transport itself requires it.

## Language-neutral implementation sketch

```text
workers = bounded_worker_pool(client_defined_concurrency)

process(command):
  # Dispatch yields zero or more notifications, then a terminal response.
  for response in dispatch_by_command_type(command):
    POST /v1/tunnels/{tunnel_id}/response
      header X-Tunnel-Shard-Token = command.shard_token
      body.request_id = command.request_id
      body.channel = command.channel
      body.resp_* = response

loop:
  poll = GET /v1/tunnels/{tunnel_id}/poll?limit=25&timeout_ms=15000
  if poll.status == 204:
    continue
  if poll.status != 200:
    handle_control_plane_error(poll)
    continue

  for command in poll.body.commands:
    workers.submit(process, command)
```

## Implementation checklist

- Generate or hand-write models from [`openapi.json`](openapi.json).
- Send bearer auth and stable client name/version headers.
- Use only the canonical plural endpoints.
- Handle `200` and `204` poll responses.
- Support both documented `command_type` values.
- Preserve raw JSON-RPC payloads and multi-valued headers.
- Echo `request_id`, `channel`, and `shard_token` in the correct locations.
- Use bounded command concurrency and preserve correlation when responses
  complete out of order.
- Forward non-final `jsonrpc_notify` payloads before the terminal response.
- Cover each response discriminator with fixtures.
- Ignore unknown JSON fields for forward compatibility.
- Validate fixtures against the OpenAPI document in CI.
