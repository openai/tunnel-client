# Local health and component details

Use `/healthz` for tunnel-client process liveness and `/readyz` for the existing
startup readiness decision. Use `/health?details=true` or `/health/mcp` to understand
what the runtime has observed. Reading health never initializes MCP, lists
tools, starts another child, or probes a dependency. Observations are historical
evidence at their timestamps, not a continuous reachability guarantee.

## Routes and compatibility

| Request | Response |
| --- | --- |
| `GET /healthz` | Existing `200 live` liveness response. |
| `GET /readyz` | Existing readiness response: 200 when ready, 503 while gated. |
| `GET /metrics` | Existing Prometheus metrics. |
| `GET /health` | Compact JSON by default; details when configured below. |
| `GET /health?details=true` | JSON with every registered component. |
| `GET /health?details=false` | Compact JSON, overriding the configured default. |
| `GET /health/mcp` | MCP component details, independent of the default. |
| `GET /health/{component}` | One registered component's details. |

The new `/health` family is accessible only over loopback TCP or the configured
Unix socket, even when the listener binds all interfaces. `--allow-remote-ui`
does not widen its access. Existing probes retain their access rules. The
detail setting chooses representation; it does not disable collection or grant
access. Keep container and Kubernetes probes on `/healthz` and `/readyz`.
Requests from other peers return HTTP 403 with a plain-text denial before
method or query validation.

A successful snapshot returns HTTP 200, including when a component is degraded.
Check the JSON `ready` field or `/readyz` for the readiness decision. Over an
allowed local connection, the new routes return JSON with
`Content-Type: application/json` and
`Cache-Control: no-store`. They accept GET only; other methods return 405 with
`Allow: GET`. The aggregate accepts exactly one literal `details=true` or
`details=false`; empty, repeated, malformed, and unknown query parameters return
400. Component routes accept no query parameters. Unknown or unavailable
components return 404. A registered disabled component returns 200 with
`status: "disabled"`. An internal snapshot encoding failure returns 500.

Older runtimes may return 404 for the new routes. Existing probe and CLI health
contracts remain valid and do not require an upgrade of the remote service.
Consumers should check `schema_version` and ignore unknown JSON fields and
component names within a supported schema version. This permits additive
diagnostics without requiring every reader to upgrade together.

**Automation:** Use the `/health` family when you need the documented schema and
compatibility rules above. The full client's `/api/status` and `/api/oauth`
endpoints are admin UI and support snapshots; they do not follow this health
schema contract.

### Stable JSON ordering

For the same snapshot content, the JSON output has deterministic ordering.
Fields in each schema object retain a fixed order, component names are sorted
lexicographically, and capability names and tool names are sorted. Proxy routes
retain a deterministic order with numbered labels. Fx registration order and
map insertion order do not affect the output. When the aggregate exceeds its
size budget, details are removed in
component-name order.

For exact-byte assertions, normalize changing values such as `snapshot_at`,
`runtime.instance_id`, and `runtime.uptime_seconds` while preserving the original
JSON key order. Counters and observation timestamps can also change as work
progresses. Readers should continue accepting new optional fields and components
within the supported schema version.

## Select the default detail level

The default is false in `tunnel-client`, `tunnel-client-runtime`, and
`tunnel-client-runtime-cloudflared`. Configure it through any shared source:

```sh
tunnel-client run --health.show-details=false
tunnel-client run --health.show-details=true
HEALTH_SHOW_DETAILS=true tunnel-client run
```

These examples assume your normal tunnel credentials and MCP configuration are
already supplied. The equivalent profile setting is:

```yaml
health:
  show_details: true
```

Precedence is explicit CLI flag, environment, YAML/profile, then default.
`--health.show-details=false` overrides `HEALTH_SHOW_DETAILS=true` and a true
profile value. `HEALTH_SHOW_DETAILS=false` overrides a true profile value.
Invalid boolean settings fail configuration validation. The query parameter
overrides that resolved default for one aggregate request.

## Read a snapshot

```sh
curl -i 'http://127.0.0.1:8080/health'
curl -i 'http://127.0.0.1:8080/health?details=true'
curl -i 'http://127.0.0.1:8080/health?details=false'
curl -i 'http://127.0.0.1:8080/health/mcp'
curl -i 'http://127.0.0.1:8080/health/control-plane'
curl -i 'http://127.0.0.1:8080/readyz'
```

For `--health.unix-socket /run/tunnel-client/health.sock`, use your configured
socket path:

```sh
curl --unix-socket /run/tunnel-client/health.sock 'http://localhost/health?details=true'
curl --unix-socket /run/tunnel-client/health.sock 'http://localhost/health/mcp'
```

With an ephemeral TCP port, use the base URL written by `--health.url-file`.
If you already have a URL ending in `/healthz` or `/readyz`, replace that path
with `/health?details=true`; do not append another path to the probe URL.
Managed runtime connect/status JSON includes `health_details_url` and
`mcp_health_url` when the runtime confirms support. These fields are omitted for
older runtimes. Existing `health_url`, `healthy`, and `ready` keep their meanings.
The narrow runtime binaries expose the HTTP routes without adding the full
client's `health` CLI command or admin UI.

A compact response looks like this (identifiers and times are examples):

```json
{
  "schema_version": 1,
  "live": true,
  "ready": true,
  "snapshot_at": "2026-09-10T20:55:46Z",
  "runtime": {
    "instance_id": "0123456789abcdef0123456789abcdef",
    "version": "0.0.0-dev",
    "flavor": "full",
    "started_at": "2026-09-10T20:55:40Z",
    "uptime_seconds": 6,
    "lifecycle": "running"
  }
}
```

Detailed responses add `components`, keyed by the names below. Each value has
`status`, `state`, optional `reason_code` and `observed_at`, and typed `details`.
Status is `ok`, `degraded`, `unknown`, or `disabled`. Timestamps are UTC RFC 3339.
The root timestamp is the read time; individual observations may be older and
are not one atomic snapshot across components. Runtime lifecycle is `starting`,
`running`, or `draining`.

After the ordinary forwarding path initializes the main stdio child and lists
its tools, `/health/mcp` can return:

```json
{
  "schema_version": 1,
  "snapshot_at": "2026-09-10T20:55:46Z",
  "component": "mcp",
  "status": "ok",
  "state": "discovered",
  "limited": false,
  "observed_at": "2026-09-10T20:55:45Z",
  "details": {
    "channel": "main",
    "transport": "stdio",
    "child_state": "running",
    "child_generation": "0123456789abcdef0123456789abcdef",
    "initialize_epoch": 1,
    "evidence": "same_child",
    "initialize": {
      "ok": true,
      "identity_complete": true,
      "limited": false,
      "observed_at": "2026-09-10T20:55:44Z",
      "protocol_version": "2025-11-25",
      "server_name": "receipt-fixture",
      "server_version": "1.2.3",
      "capability_names": ["tools"]
    },
    "tools_list": {
      "ok": true,
      "observed_at": "2026-09-10T20:55:45Z",
      "tool_names": ["echo"],
      "retained_count": 1,
      "complete": true,
      "partial": false,
      "limited": false
    }
  }
}
```

The detailed aggregate contains that component value at `components.mcp`,
without the component route's `schema_version`, `snapshot_at`, and `component`
wrapper fields.

For the self-contained `2026-07-28` protocol, a successful same-child
`server/discover` response instead moves the component to `server_discovered`
and adds a bounded `server_discover` receipt plus
`server_discover_epoch`. The legacy `initialize.ok` remains false and
`initialize_epoch` remains zero. A subsequent contiguous `tools/list`
traversal moves the component to `discovered` and populates the same bounded
`tools_list` projection. The modern receipt retains only supported protocol
versions and recognized capability names; it does not retain instructions,
unknown metadata, or raw response fields.

## Interpret components

| Component | Evidence and limitations | Availability |
| --- | --- | --- |
| `mcp` | Main-channel transport, physical child generation, legacy initialize or modern server-discover epoch, and bounded protocol, capability, identity, and tool metadata. Same-child discovery applies to stdio. Other transports explicitly report that limitation and may show their separate startup probe. | All three binaries. |
| `control-plane` | Poll attempts, successful empty or nonempty polls, current wait, retry/backoff, and bounded failure category. A long poll or local backpressure is not an outage. | All three. |
| `response-delivery` | Upload attempts, retries, active uploads, actual 200 acceptance and logical completion. The compatible benign 404 outcome is distinct from acceptance. | All three. |
| `queue` | Local polled-command channel depth, capacity, utilization, enqueue/dequeue counts and backpressure. This says nothing about the remote queue. | All three. |
| `dispatcher` | Actual active operations, pool limit, oldest active age, completions, failures and timeouts. Idle worker goroutines are not active operations. Age alone does not prove a stall. | All three. |
| `oauth` | Overall startup discovery outcome; see [Supervising startup](#supervising-startup). This does not validate a user's credentials or token. | All three. |
| `harpoon` | Configured target count and startup catalog pending/settled/failed. A settled catalog does not prove target reachability. | When its runtime module is present. |
| `cloudflared` | Existing companion enabled/readiness state and transition time. | Full and cloudflared runtime; absent in the narrow runtime without cloudflared. |
| `proxy` | Bounded route observations from the existing proxy checker. No proxy URL, credentials, or history. | Full client when its checker is registered. |

For stdio, `/readyz` can return 200 before any protocol exchange because its
startup probe skips stdio. In that case MCP state is `not_observed` with unknown
status. Legacy forwarded discovery moves it to `initialized` and then
`discovered`; modern discovery moves it to `server_discovered` and then
`discovered`. Any reported identity is the child's self-report. A replacement
child, reinitialize, rediscovery attempt, or physical shutdown invalidates the
affected earlier proof; an ordinary non-discovery request timeout that preserves
the child does not erase its previous evidence.

`tools_list.complete` means a contiguous first-to-last traversal was observed
without omissions or limits. `partial` or `limited` means retained names can
prove presence, but an omitted name does not prove absence. A successful empty
list is distinguishable from an unobserved list. Pagination is never fetched
by health reads. A list-changed notification invalidates catalog completeness.
Protocol evidence does not prove that its response upload succeeded; inspect
`response-delivery` separately.

## Supervising startup

Use each process's own exit and liveness policy for restarts. The MCP server's
health contract governs the MCP process; tunnel-client's `/healthz` governs
tunnel-client liveness. A tunnel-client readiness failure or an individual MCP
HTTP error does not establish that the MCP process needs to restart.

Read `/health?details=true` for the component evidence behind startup. The
snapshot's `ready` field and `/readyz` report tunnel-client readiness. They are
not MCP process liveness or a universal flag for polling eligibility.

### Choose the signal for the decision

| Signal | Meaning and recommended use |
| --- | --- |
| MCP process exit or its own liveness check | Apply the MCP server's documented process restart policy. Tunnel-client health does not replace this check. |
| `/healthz` | Tunnel-client is alive when it returns 200. Use it for tunnel-client liveness, not MCP or OAuth health. |
| `/readyz` or the aggregate `ready` field | The current tunnel-client readiness decision, including applicable runtime dependencies, OAuth discovery, and the MCP startup probe. Inspect components to diagnose a non-ready result. Determine polling eligibility separately below. |
| `/health/oauth` | The overall startup OAuth discovery outcome. This does not validate a user's credentials or token. |
| `/health/mcp` | Main-channel transport and observed MCP protocol evidence, including the separate startup initialization probe where applicable. For stdio, readiness can precede any observed protocol exchange. |
| `/health/harpoon` (when registered) | Startup target catalog registration. A settled catalog does not prove that a target is reachable. |
| `/health/control-plane` | Observed poll attempts, successes, waits, and backoff. Use it to distinguish an active or paused poller from repeated poll failures. |
| An individual MCP HTTP response | A result for one request. Retain the evidence and identify the request's role before interpreting its effect on startup. A 415 or 405 alone does not determine the final OAuth outcome or MCP process health. |

While startup is pending, keep a healthy MCP process available. Diagnose final
integration failures and alert the operator; restart a process when its own
health policy or a configuration change requires it.

### Read the OAuth startup outcome

Use the local component endpoint:

```sh
curl -fsS 'http://127.0.0.1:8080/health/oauth'
```

An HTTP 200 means the snapshot was read successfully, including when discovery
failed. Read the JSON `state` and `status` fields for the OAuth outcome.

The current schema version 1 OAuth component uses these values:

| `state` | `status` | `reason_code` | `details.discovery_complete` | Meaning |
| --- | --- | --- | --- | --- |
| `pending` | `unknown` | omitted | `false` | No final startup discovery outcome yet. |
| `complete` | `ok` | omitted | `true` | Startup OAuth discovery completed successfully. |
| `not_advertised` | `ok` | `metadata_not_advertised` | `true` | Optional absence of protected-resource metadata; this allowed outcome supports plain MCP servers. |
| `failed` | `degraded` | `discovery_failed` | `true` | Required startup OAuth discovery failed. |
| `disabled` | `disabled` | omitted | `false` | Startup OAuth discovery is not enabled for this configuration. |

For enabled discovery, `discovery_complete` means it reached a final outcome,
including a failure; it is not a success flag. Component `status` describes that
component's evidence independently of readiness. The `complete`,
`not_advertised`, and `failed` states include `observed_at`, the time discovery
settled. The `pending` and `disabled` states omit it. Treat unrecognized state
or reason values as unknown.

For example, a pending response has this shape (timestamps are illustrative):

```json
{
  "schema_version": 1,
  "snapshot_at": "2026-01-02T03:04:05Z",
  "component": "oauth",
  "status": "unknown",
  "state": "pending",
  "limited": false,
  "details": {"discovery_complete": false}
}
```

A final failure still has `discovery_complete: true`:

```json
{
  "schema_version": 1,
  "snapshot_at": "2026-01-02T03:04:10Z",
  "component": "oauth",
  "status": "degraded",
  "state": "failed",
  "reason_code": "discovery_failed",
  "observed_at": "2026-01-02T03:04:09Z",
  "limited": false,
  "details": {"discovery_complete": true}
}
```

These fields describe the overall startup discovery outcome. Use the optional
probe observations below to inspect the HTTP attempts that led to it.

### Inspect OAuth startup probes

For HTTP startup OAuth discovery, `details.active_probes` contains up to 16
outstanding observations and `details.recent_probes` contains the last 16
completed observations. They are available while the overall OAuth state is
still `pending`. Active observations are ordered by registration; completed
observations are ordered by completion, oldest retained first. Empty arrays are
omitted.

One observation represents one invocation of the configured HTTP transport.
Each redirect or explicit retry that invokes the transport produces a separate
observation. Request construction failures and rejected redirect destinations do
not create synthetic observations. An attempt can fail before reaching the
server, and a transport can perform internal retries that are not separately
observable here. A missing response status means that no valid response status
was observed; it does not prove that the server received or rejected the request.

For example, this `details` value shows a rejected challenge followed by a
metadata response whose body or disposition is still being processed. IDs and
timestamps are illustrative:

```json
{
  "discovery_complete": false,
  "active_probes": [
    {
      "probe_id": "probe-example-2",
      "origin": "startup",
      "probe_type": "protected_resource_metadata",
      "method": "GET",
      "started_at": "2026-01-02T03:04:06Z",
      "response_at": "2026-01-02T03:04:07Z",
      "status_code": 200,
      "disposition": "pending",
      "response_request_id": "probe-prmd-200"
    }
  ],
  "recent_probes": [
    {
      "probe_id": "probe-example-1",
      "origin": "startup",
      "probe_type": "www_authenticate",
      "method": "POST",
      "started_at": "2026-01-02T03:04:05Z",
      "response_at": "2026-01-02T03:04:05Z",
      "status_code": 415,
      "completed_at": "2026-01-02T03:04:06Z",
      "disposition": "fallback",
      "response_request_id": "probe-post-415"
    }
  ]
}
```

| Field | Meaning |
| --- | --- |
| `probe_id` | Opaque identifier for this observation within one tunnel-client process. It is not sent to the server. |
| `origin` | `startup`; queued OAuth commands and the separate MCP initialization probe do not enter this history. |
| `probe_type` | `www_authenticate`, `protected_resource_metadata`, or `authorization_server_metadata`. |
| `method` | The transport request method, projected to `GET`, `POST`, or `OTHER`. |
| `started_at` | Time the transport attempt began. |
| `response_at` | Time response headers became available; omitted when no response was observed. |
| `status_code` | Observed three-digit HTTP status; omitted when absent or invalid. |
| `completed_at` | Time the discovery decision was recorded; omitted while pending. |
| `disposition` | The decision described below. It is based on control flow, not an HTTP status lookup. |
| `response_request_id` | Optional validated `X-Request-Id` from this exact response. |

| Disposition | Meaning |
| --- | --- |
| `pending` | The request or its decision is still in progress. A response status may already be available. |
| `accepted` | This discovery step accepted or retained the response. Later discovery, bundle, or catalog work can still fail. |
| `fallback` | The client chose another method or candidate, a retained candidate, or continuation without optional enrichment. This describes the immediate action, not whether the response is used later. |
| `retry` | The client scheduled another request, pass, or startup cycle. Shutdown can prevent that later attempt from running. |
| `redirect` | The existing redirect policy allowed continuation. This does not prove that the next destination was reached. |
| `not_advertised` | The complete discovery result settled as optional metadata absence. A single 404 does not establish this outcome. |
| `terminal_failure` | The complete discovery result settled as a required failure at this observation. The decision can depend on earlier attempts; failures outside HTTP can settle discovery without this disposition. |
| `cancelled` | The startup operation was cancelled by its owner before another decision was recorded. Request deadlines follow the normal fallback, retry, or failure decision instead. |

The component's final OAuth state remains authoritative. A completed observation
is not rewritten when later work fails. Probe observations are diagnostic
snapshots, not a synchronization barrier for a supervisor that acts immediately
on server logs. Continue using the supervision policy above.

`probe_id` cannot be used directly to search server logs. `response_request_id`
helps only when the server records the same value; its presence and uniqueness
are not guaranteed. The client retains exactly one `X-Request-Id` value only
when it contains 1–128 ASCII letters, digits, `.`, `_`, `:`, or `-`, and the
response's `Connection` header does not name it. Invalid, ambiguous, or oversized values
are omitted rather than shortened. Treat retained IDs as untrusted diagnostic
data provided by the server.

ID inspection omits the value when the parsed response exceeds 128 header
entries, 32 `Connection` values, or 1,024 total `Connection` bytes. These limits
bound diagnostic work even for a custom HTTP transport.

The component sets `limited` when inspection or retention limits reduce the
available evidence. That indication remains set for the startup lifetime.
Older runtimes may omit both arrays. Treat missing arrays as unavailable evidence.

### Determine polling separately from readiness

In the standard runtime, polling has startup gates in addition to its normal
retry and queue behavior:

| Condition | Polling effect |
| --- | --- |
| A positive `mcp.startup_wait_timeout` is active for an HTTP `main` binding | The first poll and OAuth discovery wait for the MCP initialization probe to settle or the configured wait to expire. |
| The listener wait expires | The listener gate releases. `/readyz` remains non-ready for the startup wait failure, and the catalog gate below still applies. |
| Polling is unscoped or its scope includes `harpoon` | Startup OAuth discovery and target registration must complete before commands are consumed. A pending or failed startup catalog keeps this gate closed. |
| Polling explicitly excludes `harpoon` | It does not wait for the Harpoon startup catalog. Other applicable conditions remain. |
| Startup gates are open | The poller may proceed. Queue pressure or retry backoff can still pause polling; inspect `/health/control-plane` for observed behavior. |

An OAuth `complete` state does not establish that Harpoon registration has
settled. A `/readyz` 200 does not prove that a poll is currently successful,
and a 503 does not universally mean polling is blocked.
Use the configured polling scope and the applicable gates to interpret
eligibility. See [poll channels](configuration.md#control-plane) and
[startup listener wait](configuration.md#mcp-server) for configuration details.

## Bounds and privacy

Health state does not retain tool schemas, descriptions, arguments, outputs,
raw errors, credentials, raw OAuth bodies/headers, pagination cursors, or target
inventories. The following diagnostic limits do not change forwarded traffic:

| Item | Limit |
| --- | --- |
| Registered components | 16, with unique fixed ASCII names up to 64 bytes. |
| Encoded component / aggregate response | 24 KiB / 64 KiB. |
| MCP response result / request metadata examined | 1 MiB / 8 KiB. |
| Retained server-discover protocol versions | Up to 32 exact date-version strings, sorted and deduplicated. |
| Pagination cursor / traversal | 4 KiB per cursor / 32 pages. |
| Retained tool names | Up to 256 exact names, 128 UTF-8 bytes each, sorted and deduplicated; 16 KiB encoded name-array budget. |
| Protocol and server identity fields | 256 UTF-8 bytes each; oversized values are omitted, not shortened into a different identity. |
| Runtime version / flavor | 256 / 32 UTF-8 bytes. |
| Component state / reason code | 32 / 64 bytes of fixed identifiers. |
| OAuth startup probe observations | Up to 16 active and 16 completed observations; response request IDs are at most 128 ASCII bytes. |
| Proxy route summaries | 16. |
| Dispatcher start timestamps | 1,024 active timestamp slots; operation counts remain exact when age tracking is limited. |

Limited component evidence is marked `limited`; incomplete server identity is
marked `identity_complete: false`. Runtime metadata omissions set runtime
`limited`. Aggregate overflow removes optional details in deterministic component
name order, retains status envelopes, and sets `truncated: true`. JSON is never
cut mid-value. Missing or omitted observations must not be interpreted as a
successful check.
