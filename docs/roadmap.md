# Roadmap / Design Notes

This document captures forward-looking ideas. It is **not** a guarantee of
behavior or a commitment to a timeline.

## Potential enhancements

- **Progress / notification forwarding**
  - Relay MCP JSON-RPC notifications (no ID) back through the control plane so
    long-running requests can surface progress.
- **Richer readiness**
  - Make readiness reflect meaningful control-plane/MCP connectivity signals
    instead of basic "process up" status.
- **mTLS certificate automation**
  - Add operator tooling for cert issuance and rotation workflows now that
    tunnel-client supports per-channel mTLS client certificates.
- **Control-plane allowlisting**
  - Support IP-based controls or tighter network restrictions where applicable.
- **Tracing**
  - Add end-to-end trace context propagation and OpenTelemetry spans across
    poll, MCP, and response.
