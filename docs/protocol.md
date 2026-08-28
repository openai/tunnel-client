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
3. optionally fetches managed Cloudflare runtime material to launch the bundled
   `cloudflared` companion;
4. long-polls for commands addressed to one tunnel;
5. forwards each command to the configured MCP server; and
6. posts the MCP result back to the control plane.

The canonical client endpoints are:

| Method | Path | Purpose |
| --- | --- | --- |
| `GET` | `/v1/tunnels/{tunnel_id}` | Fetch minimal startup metadata. |
| `GET` | `/v1/tunnels/{tunnel_id}/cloudflare/runtime` | Fetch managed Cloudflare metadata plus the runtime token for an explicitly enabled bundled companion. |
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

### Tunnel wire protocol version

The official tunnel-client also sends an additive, protected wire-contract
version on metadata, managed Cloudflare runtime, poll, and response requests:

```http
X-Tunnel-Client-Wire-Protocol-Version: 2026-08-25
```

This is distinct from the diagnostic `X-Tunnel-Client-Version` release string
and from MCP's per-request `Mcp-Protocol-Version`. Wire versions use the same
`YYYY-MM-DD` shape as MCP versions. A missing header means legacy/backfill
semantics for an older tunnel-client; older tunnel-service versions ignore the
new header. Unknown future values must not be treated as this version.

The wire version says which additive tunnel-client/control-plane contract the
client understands. It does not by itself advertise any channel behavior:
tunnel-service must still inspect `X-Tunnel-MCP-Server-Info` and its
per-channel `stateless` declaration before selecting self-contained Harpoon
behavior. In particular, a client may send the current wire version while
still emitting an unchanged v1 server-info value because none of its enabled
channels advertises `stateless`.

### MCP server information

On control-plane requests, clients may send the optional
`X-Tunnel-MCP-Server-Info` header alongside
`X-Tunnel-Client-Instance-Id`. The official tunnel-client sends it on tunnel
metadata, managed Cloudflare runtime, poll, and response requests. The
declarations reflect the channels enabled when each request is sent, so
Harpoon appears only after at least one target is registered.

The header value is compact JSON. Version 1 remains unchanged and has this
exact shape:

```json
{
  "version": 1,
  "channels": [
    {"name": "main", "proc_affinity": true},
    {"name": "harpoon", "proc_affinity": true}
  ]
}
```

Version 1 permits only these keys:

| Location | Key | Required | Meaning |
| --- | --- | --- | --- |
| Top level | `version` | yes | Integer protocol version; version 1 is the only v1 value. |
| Top level | `channels` | yes | Array of MCP channel declarations. |
| Channel | `name` | yes | Canonical channel name, such as `main` or `harpoon`. |
| Channel | `proc_affinity` | no | When `true`, session work for that channel must stay on one tunnel-client process. |

An omitted `proc_affinity` means `false`; clients serialize false by
omitting the key rather than sending `"proc_affinity": false`.

The v1 object is deliberately narrow. It must not contain URLs, commands,
transport details, headers, request or response payloads, secrets, targets, or
customer IDs.

Version 2 adds the independent optional `stateless` capability:

```json
{
  "version": 2,
  "channels": [
    {"name": "harpoon", "stateless": true, "proc_affinity": true}
  ]
}
```

Version 2 permits only these keys:

| Location | Key | Required | Meaning |
| --- | --- | --- | --- |
| Top level | `version` | yes | Integer protocol version; version 2 is the only v2 value. |
| Top level | `channels` | yes | Array of MCP channel declarations. |
| Channel | `name` | yes | Canonical channel name, such as `main` or `harpoon`. |
| Channel | `stateless` | no | When `true`, the channel accepts MCP `2026-07-28` self-contained requests. |
| Channel | `proc_affinity` | no | When `true`, routing still needs one tunnel-client process because protocol or application state is local. |

`stateless` and `proc_affinity` are independent booleans. Either, neither, or
both may be true; clients must not reject a declaration merely because both
are true. An omitted value means `false`, and clients serialize false by
omitting the key instead of sending `false`.

Clients emit the narrowest version that represents their declarations: emit
v1 when no channel advertises `stateless`, and emit v2 when at least one
channel does. Both versions preserve the compact canonical key order shown
above (`version`, `channels`; then `name`, `stateless`, `proc_affinity`),
canonical unique channel names, at most 32 channel declarations, and at most
4096 UTF-8 bytes. Clients must reject duplicate, non-canonical, invalid, or
over-limit declarations before sending them.

Capability declarations describe the effective channel binding:

- A remote Streamable HTTP `main` channel advertises neither capability.
- A legacy stdio- or in-memory-backed `main` channel uses
  `"proc_affinity": true`.
- An enabled built-in in-memory `harpoon` channel uses both
  `"stateless": true` and `"proc_affinity": true`. Its MCP `2026-07-28`
  requests are self-contained, but Harpoon target and catalog registration
  remain replica-local. Include it only while Harpoon has at least one
  registered target; never include target details.
- Other configured channels use their canonical names and the same transport
  rule: process-local stdio or in-memory work uses `proc_affinity`; remote
  Streamable HTTP advertises neither capability unless it independently
  supports self-contained requests.

For a remote Streamable HTTP main channel without enabled Harpoon, the value
is:

```json
{
  "version": 1,
  "channels": [
    {"name": "main"}
  ]
}
```

For a remote Streamable HTTP main channel with enabled Harpoon, the value is:

```json
{
  "version": 2,
  "channels": [
    {"name": "main"},
    {"name": "harpoon", "stateless": true, "proc_affinity": true}
  ]
}
```

The header is additive metadata. Older tunnel-service versions ignore it, and
its presence does not change current routing, queueing, Redis, shard-token,
poll, or response behavior. Version 2 advertises protocol sessionlessness; it
does not make Harpoon or OAuth active-active while target and catalog
registration remain local to one tunnel-client replica. A later registry-safe
change can remove Harpoon's `proc_affinity` declaration. Neither version adds
a transport field, affinity token, token echo, keyed FIFO lane, body field,
command, or endpoint. This reader-first client protocol prerequisite does not
provide service-side lazy-owner/FIFO behavior; that is a separate service
implementation.

Treat tunnel IDs, request IDs, and shard tokens as opaque strings. Do not parse
them or infer routing from their contents.

## Managed Cloudflare runtime fetch

When managed bundled-`cloudflared` mode is explicitly enabled and no static
token is configured, fetch
`GET /v1/tunnels/{tunnel_id}/cloudflare/runtime` before starting the child
process. The response contains non-secret Cloudflare metadata and one runtime
token. Treat the full response as secret-bearing: do not write it to disk,
argv, logs, metrics, support exports, or generic raw HTTP traces. The service
returns `Cache-Control: no-store`; clients must not cache the response.

The static `cloudflared.token` / `CLOUDFLARED_TUNNEL_TOKEN` path takes
precedence and must bypass this fetch. A 401 or 403 is an authorization failure
and must not fall back to another credential path.

## Poll loop

Request:

```http
GET /v1/tunnels/tunnel_123/poll?limit=25&timeout_ms=15000 HTTP/1.1
Authorization: Bearer <tunnel-api-key>
X-Tunnel-Client-Name: example-rust-client
X-Tunnel-Client-Version: 1.2.3
```

When an operator explicitly configures poll subscriptions, the client adds one
repeated `channel` query parameter per sorted allowlist entry, for example:

```http
GET /v1/tunnels/tunnel_123/poll?channel=harpoon&channel=main&limit=25&timeout_ms=15000 HTTP/1.1
```

Omitting the allowlist preserves the legacy request above with no `channel`
parameters. These parameters describe drain subscriptions; they do not replace
`X-Tunnel-MCP-Server-Info`, which remains capability metadata. End-to-end
channel isolation also requires a tunnel-service reader/filtering implementation.

`limit` is optional and must be from `1` through `25`. It is a request hint:
if a successful response contains more commands than requested, process every
command; do not drop the excess. `timeout_ms` is an optional requested
long-poll wait in milliseconds. The service bounds the effective wait, so a
client must not assume the requested duration is exact.

`limit` controls only the requested poll batch size. It is not an execution
concurrency limit; each client chooses its own bounded concurrency.

A `204 No Content` response means the poll completed without commands. Issue
another poll. When a poll response arrives, record the local receipt time as
soon as the response headers are available and before decoding the body. Use
one receipt time for every command in the response. A `200 OK` response
contains a JSON envelope:

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
| Valid `response_timeout` present | `encoding/json` ignores the unknown property; legacy behavior is retained. | Anchors a local deadline at poll-response receipt and enforces it across MCP work and response delivery. |
| Malformed, wrong-type, unknown-unit, or overflowing value present | `encoding/json` ignores the unknown property; legacy behavior is retained. | Decoding succeeds and legacy behavior is retained. |

Previously generated OpenAPI clients are outside this compatibility guarantee.
Command schemas remain open with `additionalProperties: true`, and clients must
accept unknown future command properties.

The receipt time must retain the platform's monotonic-clock component. For a
valid timeout, derive the local deadline without adding another allowance:

```text
local_response_deadline = local_poll_response_received_at + response_timeout
```

No wall-clock synchronization or server-time field is required, and clients
must not derive this deadline from `created_at`. The deadline bounds the whole
command lifecycle: MCP connect, write, read, and the response POST. Drop a
command that is already expired without contacting MCP or posting a response.
If the deadline passes during MCP work, cancel the operation and close its
connection unless the shared stdio exception below applies. If it passes after
MCP completes, cancel the response POST without closing a shared connection
that may already serve another command. Never synthesize a late error response.
Progress notifications do not restart the deadline.

For a shared stdio binding, a non-`initialize` response deadline is an
exception to closing the connection: keep the process-affine child pipes open,
retire the timed-out JSON-RPC ID before admitting later work, and discard any
later response for that ID so it cannot be mistaken for the next command. While
a retired response remains outstanding, downstream server requests and
notifications are ambiguous and may be dropped, but later terminal responses
continue by ID. If a later command reuses a still-retired caller ID, use a fresh
downstream-only ID for that command and restore the caller ID on its matching
response; this keeps the stale response unambiguous without rejecting future
logical sessions forever. An `initialize` deadline remains fail-closed because
initialization cannot be safely cancelled or replayed.

Shared stdio also has one opt-in lifecycle compatibility guard. When
`--mcp.stdio-send-initialized-notification` (or
`MCP_STDIO_SEND_INITIALIZED_NOTIFICATION`) is enabled, a successful ID-bearing
`initialize` response makes tunnel-client write `notifications/initialized` to
the local stdio server before admitting the next command. If the caller later
supplies that same no-ID notification, the client acknowledges the polled
command without writing a duplicate downstream. The guard is disabled by
default so legacy stdio servers keep verbatim JSON-RPC forwarding; operators
can enable it for specification-compliant servers paired with older callers
that omit the notification.

### `jsonrpc` commands

For `command_type: "jsonrpc"`, `jsonrpc` is the raw JSON-RPC request or
notification to send to the MCP server. Preserve JSON-RPC IDs and do not
reinterpret the payload as a tunnel-protocol object, except for the private
shared-stdio retired-ID alias described above. That alias is restored before
the response returns to the control plane.

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
| `resp_headers` | no | Multi-valued upstream MCP response headers after the protocol allowlist and `Connection` nominations are applied. |
| `resp_code` | yes | HTTP-style status code from the MCP interaction. |
| `resp_type` | no | Payload discriminator; defaults to `jsonrpc_response`. |

Supported `resp_type` values:

| Value | Final? | Use |
| --- | --- | --- |
| `jsonrpc_response` | yes | Terminal JSON-RPC result or error with `resp_json`. |
| `jsonrpc_notify` | no | Intermediate JSON-RPC notification with `resp_json`. |
| `notify_ack` | yes | Terminal acknowledgment for a JSON-RPC notification that has no result. |
| `session_termination_response` | yes | Terminal acknowledgment after closing an MCP session. |

### Structured MCP errors and synthesized tunnel failures

When the target returns a valid JSON-RPC error, preserve its request `id` and
exact `error.code`, `error.message`, and `error.data` values in `resp_json`.
Preserve the actual target HTTP status in `resp_code` and the existing
protocol-relevant response-header allowlist in `resp_headers`: `Content-Type`,
`Mcp-Session-Id`, `Mcp-Protocol-Version`, `Last-Event-ID`,
`Access-Control-Expose-Headers`, and `WWW-Authenticate`. The client removes
`Connection`, every response field it names, empty values, and unrelated
upstream headers before posting the payload. This includes MCP capability error
`-32003` and version error `-32004`; do not replace or decorate either with
`-32603`.

At the connector boundary, tunnel-service maps every informational `1xx` status
to `502` because an informational response cannot terminate the request. Among
final-response statuses, it preserves values recognized by its HTTP runtime,
maps an unrecognized `2xx` to `200`, and maps any other unrecognized status
through `599` to `502`. For synthesized target failures, `upstream_status`
retains the original error status.

Only when no valid MCP error can be recovered may a client synthesize JSON-RPC
`-32603`. A synthesized failure may carry bounded provenance at
`error.data.tunnel_failure`:

```json
{
  "request_id": "req_123",
  "channel": "main",
  "resp_json": {
    "jsonrpc": "2.0",
    "id": "rpc_123",
    "error": {
      "code": -32603,
      "message": "Bad Gateway",
      "data": {
        "tunnel_failure": {
          "version": 1,
          "source": "transport_closed",
          "transport_error_kind": "closed_pipe",
          "upstream_response_received": false
        }
      }
    }
  },
  "resp_headers": {
    "Content-Type": ["application/json"]
  },
  "resp_code": 502,
  "resp_type": "jsonrpc_response"
}
```

The machine-readable schema is published as `x-tunnel-failure-schema` on the
`resp_json` OpenAPI property. Its current known fields are:

| Field | Required | Contract |
| --- | --- | --- |
| `version` | yes | Positive integer. Version `1` is current; readers must tolerate future positive versions. |
| `source` | yes | Bounded string. Known values are listed below; readers must tolerate unknown values. |
| `transport_error_kind` | no | Optional fixed, low-cardinality diagnostic label emitted by newer clients; legacy clients omit it and readers must tolerate unknown future values. |
| `upstream_response_received` | yes | Whether an actual target HTTP response was received. |
| `upstream_status` | no | Target HTTP error status from `400` through `599`; valid only for `source: target_http` with `upstream_response_received: true`. |

Known version-1 `source` values are `target_http`, `dns`, `tls`, `connect`,
`transport_closed`, `timeout`, `protocol`, and `client_internal`.
`target_http` means an actual target HTTP error response was received and
requires `upstream_status`. `transport_closed` means the target connection or
pipe was already closed or became unusable before a target response, so
`upstream_response_received` must be `false` and `upstream_status` must be
omitted. A synthesized outer `resp_code` such as `502` is the tunnel response
status; it is not evidence that the target returned that status.
The OpenAPI schema lists the current fixed `transport_error_kind` values;
readers must tolerate omitted or unknown future values.
`canceled` remains a local-log-only diagnostic: canceled commands do not post
a synthesized response, and the envelope builder omits it defensively.

The provenance object is optional and additive. Existing clients may omit it,
existing services continue accepting the nested value inside opaque
`resp_json`, and contract-aware readers use only recognized safe fields.
Unknown versions, sources, and fields must fall back to generic tunnel-failure
behavior and must not be copied into logs or metrics. Never put raw target
URLs, response bodies, arbitrary exception text, credentials, or tokens in
provenance. This contract does not use capability negotiation or an additional
MCP handshake.

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
- For polls, retry transient network failures, `429`, and `5xx` with
  bounded exponential backoff and jitter.
- For terminal response POSTs, retry transient network failures, `408`, `429`,
  `502`, `503`, and `504` with bounded exponential backoff and jitter.
  Preserve the exact body, headers, correlation values, and caller deadline
  across attempts; do not retry other `4xx` responses.
- For JSON-RPC notification POSTs, retry `429` and transport failures that occur
  before the request is written. Notifications are non-terminal, so the request
  remains pending after one is accepted; do not replay an ambiguous failure that
  could enqueue the same notification twice. A notification delivery failure is
  best-effort: skip later notifications for that command, keep draining the MCP
  stream, and still attempt delivery of the terminal response.
- When the rules above allow a retry, `429` and `503` responses may include the standard
  `Retry-After` header in either delta-seconds form (for example, `5`) or
  HTTP-date form. Treat a valid value as a minimum delay in addition to local
  backoff and jitter. Ignore malformed, negative, and expired values, cap
  excessive values, and never wait past cancellation or the command deadline.
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

process(command, deadline):
  if deadline is not none:
    if deadline has passed:
      return
    apply deadline to MCP work and response delivery

  # Dispatch yields zero or more notifications, then a terminal response.
  for response in dispatch_by_command_type(command):
    POST /v1/tunnels/{tunnel_id}/response
      header X-Tunnel-Shard-Token = command.shard_token
      body.request_id = command.request_id
      body.channel = command.channel
      body.resp_* = response

loop:
  poll = GET /v1/tunnels/{tunnel_id}/poll?limit=25&timeout_ms=15000
  poll_received_at = monotonic_now()  # immediately when response headers arrive

  if poll.status == 204:
    continue
  if poll.status != 200:
    handle_control_plane_error(poll)
    continue

  for command in poll.body.commands:
    deadline = none
    timeout = parse_optional_response_timeout(command.response_timeout)
    if timeout is valid:
      deadline = poll_received_at + timeout

    workers.submit(process, command, deadline)
```

## Implementation checklist

- Generate or hand-write models from [`openapi.json`](openapi.json).
- Send bearer auth and stable client name/version headers.
- Send the optional MCP server information declaration alongside the stable
  client instance ID on control-plane requests.
- Use only the canonical plural endpoints.
- Handle `200` and `204` poll responses.
- Record one monotonic receipt time before decoding a successful poll response.
- Parse the optional relative `response_timeout`, fail open when it is missing
  or invalid, and treat `0s` as immediately expired.
- Bound MCP work and response delivery by receipt time plus the timeout without
  adding another skew, and drop expired commands without a response.
- Support both documented `command_type` values.
- Preserve raw JSON-RPC payloads and multi-valued headers.
- Echo `request_id`, `channel`, and `shard_token` in the correct locations.
- Use bounded command concurrency and preserve correlation when responses
  complete out of order.
- Forward non-final `jsonrpc_notify` payloads before the terminal response.
- Cover each response discriminator with fixtures.
- Ignore unknown JSON fields for forward compatibility.
- Validate fixtures against the OpenAPI document in CI.
