# Local health and component details

Use `/healthz` for process liveness and `/readyz` for the existing startup
readiness decision. Use `/health?details=true` or `/health/mcp` to understand
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

## Interpret components

| Component | Evidence and limitations | Availability |
| --- | --- | --- |
| `mcp` | Main-channel transport, physical child generation, initialize epoch, exact retained identity and tool names. Same-child discovery applies to stdio. Other transports explicitly report that limitation and may show their separate startup probe. | All three binaries. |
| `control-plane` | Poll attempts, successful empty or nonempty polls, current wait, retry/backoff, and bounded failure category. A long poll or local backpressure is not an outage. | All three. |
| `response-delivery` | Upload attempts, retries, active uploads, actual 200 acceptance and logical completion. The compatible benign 404 outcome is distinct from acceptance. | All three. |
| `queue` | Local polled-command channel depth, capacity, utilization, enqueue/dequeue counts and backpressure. This says nothing about the remote queue. | All three. |
| `dispatcher` | Actual active operations, pool limit, oldest active age, completions, failures and timeouts. Idle worker goroutines are not active operations. Age alone does not prove a stall. | All three. |
| `oauth` | Startup discovery pending, succeeded, optional failure, failure, or disabled. This does not validate a user's credentials or token. | All three. |
| `harpoon` | Configured target count and startup catalog pending/settled/failed. A settled catalog does not prove target reachability. | When its runtime module is present. |
| `cloudflared` | Existing companion enabled/readiness state and transition time. | Full and cloudflared runtime; absent in the narrow runtime without cloudflared. |
| `proxy` | Bounded route observations from the existing proxy checker. No proxy URL, credentials, or history. | Full client when its checker is registered. |

For stdio, `/readyz` can return 200 before any protocol exchange because its
startup probe skips stdio. In that case MCP state is `not_observed` with unknown
status. Ordinary forwarded discovery moves it to `initialized` and then
`discovered`. The identity is the child's self-report. A replacement child,
reinitialize, or physical shutdown invalidates earlier proof; an ordinary
request timeout that preserves the child does not erase its previous evidence.

`tools_list.complete` means a contiguous first-to-last traversal was observed
without omissions or limits. `partial` or `limited` means retained names can
prove presence, but an omitted name does not prove absence. A successful empty
list is distinguishable from an unobserved list. Pagination is never fetched
by health reads. A list-changed notification invalidates catalog completeness.
Protocol evidence does not prove that its response upload succeeded; inspect
`response-delivery` separately.

## Bounds and privacy

Health state does not retain tool schemas, descriptions, arguments, outputs,
raw errors, credentials, OAuth bodies/headers, pagination cursors, or target
inventories. The following diagnostic limits do not change forwarded traffic:

| Item | Limit |
| --- | --- |
| Registered components | 16, with unique fixed ASCII names up to 64 bytes. |
| Encoded component / aggregate response | 24 KiB / 64 KiB. |
| MCP response result / request metadata examined | 1 MiB / 8 KiB. |
| Pagination cursor / traversal | 4 KiB per cursor / 32 pages. |
| Retained tool names | Up to 256 exact names, 128 UTF-8 bytes each, sorted and deduplicated; 16 KiB encoded name-array budget. |
| Protocol and server identity fields | 256 UTF-8 bytes each; oversized values are omitted, not shortened into a different identity. |
| Runtime version / flavor | 256 / 32 UTF-8 bytes. |
| Component state / reason code | 32 / 64 bytes of fixed identifiers. |
| Proxy route summaries | 16. |
| Dispatcher start timestamps | 1,024 active timestamp slots; operation counts remain exact when age tracking is limited. |

Limited component evidence is marked `limited`; incomplete server identity is
marked `identity_complete: false`. Runtime metadata omissions set runtime
`limited`. Aggregate overflow removes optional details in deterministic component
name order, retains status envelopes, and sets `truncated: true`. JSON is never
cut mid-value. Missing or omitted observations must not be interpreted as a
successful check.
