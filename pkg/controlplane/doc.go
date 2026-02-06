// Package controlplane owns the HTTP client surface used to talk to the
// tunnel control plane.
//
// In short, the tunnel-client long-polls `GET /v1/tunnel/{tunnel_id}/poll`
// to retrieve queued MCP commands and posts execution results back through
// `POST /v1/tunnel/{tunnel_id}/response`. These endpoints live in the control plane
// service at `https://api.openai.com`.
//
// This package focuses on:
//   - Building an http.Client that applies tunnel-specific headers, API keys,
//     and TLS overrides sourced from config.ControlPlaneConfig and the shared
//     TLS bundle configuration.
//   - Owning the control plane poll/watch loop so transport swaps
//     (long-polling today, WebSockets soon) stay invisible to downstream
//     packages. The loop publishes work into the bounded channel supplied by
//     pkg/dispatcher, honoring config.ControlPlaneConfig.MaxInFlightRequests as
//     the backpressure limit.
//   - Resiliency behavior derived from README.md, and the
//     control-plane contract:
//   - Long-polling honors the control plane LONG_POLL_TIMEOUT default (30s)
//     and reconnects immediately on 204/timeout so the single-process
//     tunnel-client stays hot.
//   - Transport errors, 5xx responses, and auth retries use exponential
//     backoff with jitter and bounded ceilings to self-heal without causing
//     thundering-herd load on tunnel-service.
//   - Response posting treats 404 "already fulfilled" responses as
//     idempotent soft-failures while retrying transient failures so queued
//     MCP calls are durably acknowledged.
//   - Because tunnel-service holds drained items in an awaiting-response
//     state, a client crash after poll currently relies on connector-side
//     timeouts to release the request. Future iterations may add a
//     lease/heartbeat so in-flight work can be re-delivered if the client
//     disappears.
//   - Structured logging that avoids dumping bodies by default; raw payload
//     logs are only enabled when LOG_HTTP_RAW_UNSAFE or the corresponding flag
//     is true.
//   - Metric and trace hooks so the rest of the client can emit poll/response
//     counters, latency histograms, and OpenTelemetry spans aligned with the
//     control-plane contract.
//
// Downstream packages (dispatcher, mcpclient, etc.) depend on controlplane to
// hide the REST details while they focus on lifecycle orchestration and MCP
// invocation.
package controlplane
