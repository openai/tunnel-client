# Harpoon target templates

A target template gives a caller access to one configured HTTP operation with
bounded identifier values. The operator fixes the HTTPS origin, GET method,
path structure, query names, and authentication headers. Harpoon validates the
arguments and constructs the request locally.

Templates require `config_version: 2` and `template.version: 1`. Existing
exact-URL targets can share the same file. A target must have either `url` or
`template`; templates cannot use `unix_socket`.

## Configure operations

The following complete configuration exposes three operations on an example
case-management API. Replace the tunnel ID and HTTPS origins with your own
values. The configured upstream must be reachable from the client and its TLS
certificate must be trusted; configure `ca_bundle` for a private CA when needed.

Provide `CONTROL_PLANE_API_KEY` and `CASE_API_AUTHORIZATION` through your process
environment or secret manager. `CASE_API_AUTHORIZATION` contains the complete
header value, such as `Bearer ...`. Keep the credential out of command-line
arguments and source control.

Save this as `templates.yaml`:

```yaml
config_version: 2
control_plane:
  tunnel_id: tunnel_0123456789abcdef0123456789abcdef
  api_key: env:CONTROL_PLANE_API_KEY
  poll_channels: [harpoon]
harpoon:
  targets:
    - label: get_profile
      description: Get a profile by session identifier
      template:
        version: 1
        origin: https://cases.example
        method: GET
        path_template: /profiles
        query:
          session-id: "{session_id}"
        parameters:
          session_id:
            type: string
            required: true
            pattern: '^[A-Za-z0-9_-]+$'
            min_length: 1
            max_length: 64
        headers:
          Authorization: env:CASE_API_AUTHORIZATION
        allowed_headers: [Accept, X-Request-Tag]
        follow_redirects: false

    - label: get_case_orders
      description: Get orders for one case
      template:
        version: 1
        origin: https://cases.example
        method: GET
        path_template: /cases/orders
        query:
          CaseId: "{case_id}"
        parameters:
          case_id:
            type: string
            required: true
            pattern: '^[A-Za-z0-9_-]+$'
            max_length: 64
        headers:
          Authorization: env:CASE_API_AUTHORIZATION
        follow_redirects: false

    - label: get_case_status
      description: Get the status of one case including elevations and complaints
      template:
        version: 1
        origin: https://cases.example
        method: GET
        path_template: /casestatus/{case_id}
        query:
          elevations: "true"
          complaints: "true"
        parameters:
          case_id:
            type: string
            required: true
            pattern: '^[A-Za-z0-9_-]+$'
            max_length: 64
            reserved_values: [all, search, admin]
        headers:
          Authorization: env:CASE_API_AUTHORIZATION
        follow_redirects: false
health:
  listen_addr: 127.0.0.1:8080
```

Start the client with the secrets already present in its environment:

```sh
tunnel-client run --config templates.yaml
```

This example subscribes only to the Harpoon channel. To serve your existing MCP
server as well, add its `mcp.server_urls` or `mcp.commands` binding and set
`control_plane.poll_channels: [main, harpoon]`.

For a mounted secret, replace the fixed header value with a file reference:

```yaml
headers:
  Authorization: file:/run/secrets/case-api-authorization
```

The file contains the complete header value. One final line ending is removed;
embedded line breaks and invalid HTTP header values are rejected. References
resolve at startup. Restart the process after changing configuration or rotating
the referenced credential; templates do not reload automatically.

## Call and discover targets

Use MCP `tools/list` to check that `call_target_template` is available. Use
`list_targets` to discover template labels, descriptions, and their public
parameter constraints. A template entry includes `template_version: 1`,
`allowed_methods: ["GET"]`, and a JSON Schema object in `parameters_schema`.
Template discovery excludes private origins, path and
query templates, fixed query values, and authentication headers. Descriptions,
parameter names, enum values, and reserved identifiers are public metadata;
choose their contents accordingly.

Send the following MCP tool call on the Harpoon channel:

```json
{
  "jsonrpc": "2.0",
  "id": 1,
  "method": "tools/call",
  "params": {
    "name": "call_target_template",
    "arguments": {
      "label": "get_case_orders",
      "parameters": {"case_id": "CASE-123"}
    }
  }
}
```

Harpoon sends `GET https://cases.example/cases/orders?CaseId=CASE-123` with the
operator's fixed authorization header. These additional examples show the
arguments object and resulting operation:

| Arguments | Operation |
| --- | --- |
| `{"label":"get_profile","parameters":{"session_id":"session-123"}}` | `GET /profiles?session-id=session-123` |
| `{"label":"get_case_status","parameters":{"case_id":"CASE-123"}}` | `GET /casestatus/CASE-123?complaints=true&elevations=true` |

Query key order is canonicalized during encoding and has no semantic meaning.

The caller can supply `headers` only for names explicitly listed in that
target's `allowed_headers`. For example, `get_profile` accepts:

```json
{
  "label": "get_profile",
  "parameters": {"session_id": "session-123"},
  "headers": {"Accept": "application/json", "X-Request-Tag": "support-check"},
  "timeout_ms": 10000,
  "max_response_bytes": 32768
}
```

`timeout_ms` and `max_response_bytes` are optional bounded execution limits.
Timeout defaults to 30 seconds and an explicit value must be from 100 through
120,000 milliseconds. The response limit defaults to the configured Harpoon
limit, which cannot exceed 102,400 bytes; an explicit per-call value must be
positive and cannot exceed that configured limit. Omit these fields to use
defaults. Results contain the upstream `status_code`, `headers`, `body_base64`,
and `body_size_bytes`, using the same response structure as exact-target calls.

Caller input cannot override the method, origin, URL, path, query, template,
fixed headers, or redirect policy. GET bodies, duplicate JSON keys, unknown
fields, missing or additional parameters, and non-string identifiers are
rejected. The legacy `call_target` tool cannot execute a template target, and
`call_target_template` cannot execute an exact-URL target.

## Multiple parameters and enumerations

One template can contain multiple path arguments and multiple query parameters.
Each argument has its own required schema. This example combines two path
arguments, two dynamic query values, and one fixed query value:

```yaml
- label: get_tenant_case
  description: Get one tenant case with an approved view
  template:
    version: 1
    origin: https://cases.example
    method: GET
    path_template: /tenants/{tenant_id}/cases/{case_id}
    query:
      view: "{view}"
      requestId: "{request_id}"
      archive: "false"
    parameters:
      tenant_id:
        type: string
        required: true
        pattern: '^[A-Za-z0-9_-]+$'
        max_length: 64
      case_id:
        type: string
        required: true
        pattern: '^[A-Za-z0-9_-]+$'
        max_length: 64
      view:
        type: string
        required: true
        enum: [summary, details]
        max_length: 7
      request_id:
        type: string
        required: true
        pattern: '^[A-Za-z0-9_-]+$'
        max_length: 64
    headers:
      Authorization: env:CASE_API_AUTHORIZATION
    follow_redirects: false
```

Call arguments:

```json
{
  "label": "get_tenant_case",
  "parameters": {
    "tenant_id": "tenant-1",
    "case_id": "CASE-123",
    "view": "summary",
    "request_id": "req-456"
  }
}
```

The result is
`GET /tenants/tenant-1/cases/CASE-123?archive=false&requestId=req-456&view=summary`.
All four arguments must be present and valid. Missing or invalid values in any
position reject the entire call before an outbound request is sent.

## Grammar and limits

The client compiles every selected template before serving requests. Unknown or
duplicate YAML fields are rejected. `config_version: 2` also rejects multiple YAML
documents, including a trailing `---`; version 1 and files without a version retain
their existing behavior of reading only the first document. Invalid policy fails
startup.

Profile add, edit, and init validate the same template policy before saving. They
check secret-reference syntax without reading environment variables or files.
Startup resolves those references and validates the actual header values and their
combined size; a profile can be saved before its credentials are available.

| Field or input | Version 1 rule |
| --- | --- |
| `origin` | Literal `https://host` with an optional port from 1 to 65,535 and optional trailing `/`. No credentials, variable host, query, fragment, or other path. |
| `method` | Explicitly `GET`. |
| `path_template` | Absolute path. A placeholder occupies one complete segment, such as `/cases/{case_id}`. Empty segments, traversal segments, and partial placeholders such as `case-{id}` are rejected. |
| `query` | At most 32 fixed, unique keys. Each key is 1–64 ASCII unreserved characters; its value is a fixed string or exactly one `{parameter}`. Fixed strings are valid UTF-8, contain no control characters, and are at most 256 bytes. Quote YAML booleans and numbers when using them as string literals. |
| Parameter names | 1–64 characters matching `[A-Za-z][A-Za-z0-9_]*`. Declare 1–16 parameters; every declared parameter must appear in the path or query. A parameter may appear more than once. |
| Parameter types | `type: string` and `required: true`. Optional parameters, arrays, objects, and repeated query keys are unsupported. |
| Identifier characters | ASCII letters, digits, `_`, `-`, `.`, and `~`; the complete values `.` and `..` are rejected. Percent signs, slash, backslash, whitespace, controls, query delimiters, fragments, and encoded separators are rejected before URL construction. |
| Identifier length | `max_length` is required, from 1 through 256 bytes. `min_length` defaults to 1 and cannot exceed the maximum. |
| `pattern` and `enum` | At least one is required. Patterns match the entire identifier, contain at most 512 ASCII bytes, and use the portable regular-expression subset described below. An enum contains at most 64 unique values. When both are present, every value must satisfy both. |
| `reserved_values` | At most 64 identifiers rejected case-insensitively, including enum values. Duplicate reserved values are rejected case-insensitively. |
| URL length | At most 4,096 bytes after encoding. Policies whose longest permitted identifiers could exceed that limit are rejected at startup. |
| `headers` and `allowed_headers` | At most 32 configured names in total. Names are at most 128 bytes and case-insensitive. Caller names cannot overlap fixed names. Total outbound header names and values, including the managed `User-Agent`, cannot exceed 8,192 bytes. Values must be valid UTF-8 without control characters. |
| `follow_redirects` | Omit or set to `false`. A redirect response is returned without following it, even for the same origin or an independently configured target. |

Literal path segments and query keys use the same ASCII unreserved character
set as identifiers, and cannot be `.` or `..`. Query values are encoded as query
values, while path identifiers are encoded as individual segments; the caller
does not provide pre-encoded input.

Patterns support literal ASCII characters, character classes, ordinary and
noncapturing groups (`(...)` and `(?:...)`), alternation, anchors, and
quantifiers. The supported escapes are `\d`, `\D`, `\s`, `\S`, `\w`, `\W`,
and escaped regular-expression punctuation. Inline flags, lookarounds,
backreferences, octal escapes, Unicode classes such as `\p`, Go-specific
anchors such as `\A` or `\z`, and POSIX classes are rejected. This common
subset keeps public JSON Schema validation consistent with runtime validation.
The separate identifier character and length restrictions apply to every
pattern, including permissive patterns such as `.*`.

Authentication headers belong in the operator's fixed `headers` map. Caller
allowlists cannot include names containing an `authorization`, `cookie`, `key`,
`secret`, `token`, or `password` component, or recognizable joined names such as
`ApiKey`, `X-AuthToken`, and `ClientSecret`. Transport, forwarding, trusted
identity, and method-override headers are prohibited even when explicitly
listed, including `Host`, `Cookie`, `User-Agent`, `Forwarded`, `X-Forwarded-*`,
`X-Envoy-*`, and `X-HTTP-Method-Override`.

## Proxy connectivity checks

When a template uses an HTTP proxy, the full client's proxy health checker tests
CONNECT to the template's fixed origin host and port. It never supplies parameter
values, sends target authentication headers, or invokes the HTTP operation.
Successful CONNECT confirms proxy connectivity; it does not establish that a
specific resource exists or that the target will authorize a request. A rejected
CONNECT marks proxy health as degraded even when only template targets use that
proxy. Direct routes do not initiate a proxy check.

## Authorization and privacy

Identifier validation limits request structure; the upstream API remains
responsible for deciding whether a caller may access a particular object.
Harpoon preserves upstream authorization failures such as HTTP 403.

A fixed service credential gives all callers of that target the authority of
that credential. Scope it to the intended operations and objects, and use a
separate upstream authorization layer when permissions must differ by end user.
Templates do not infer end-user identity from a case ID or propagate caller
identity headers. A syntactically valid ID alone is not evidence of permission
to read that object.

Template calls are excluded from payload observers and raw HTTP debug capture.
Routine call logs omit parameter values, rendered URLs, fixed headers, and
response bodies. Upstream response bodies still return to the authorized tool
caller and may contain sensitive information. The local admin UI and support
export are operator diagnostics; template discovery is the public metadata
surface. Fixed header values and environment variables referenced by those
headers are redacted in support exports, regardless of the header or variable's
name.

## Compatibility and upgrades

- Existing exact-URL configurations with no `config_version` or version 1 keep
  their existing behavior. Version 2 can contain exact targets alongside
  templates.
- Template configuration is an explicit opt-in. Older clients reject version 2
  or the unknown template fields; they cannot silently reinterpret the template
  as an exact URL.
- `call_target_template` is a separate tool. Older clients do not implement it,
  so callers must check discovery and require template support. Do not retry a
  template operation through `call_target` or by passing a rendered URL.
- The client uses the existing MCP JSON-RPC and tunnel poll/response envelopes.
  This client feature does not establish service-side routing guarantees for a
  mixture of old and new executors. Keep templates disabled for such a group
  until every eligible executor supports them or the service enforces compatible
  routing.
- Explicit `--harpoon.target` flags replace the **entire** YAML target list;
  otherwise an explicitly set `HARPOON_TARGETS` replaces that entire list.
  These legacy inputs accept exact targets only. They do not merge with YAML
  templates. An explicitly empty value clears the list. Remove stale overrides
  when activating templates from YAML.
- Changing templates or rotating a referenced credential requires a process
  restart. Preserve a version 1 exact-target configuration if you need to roll
  back to an older client.
