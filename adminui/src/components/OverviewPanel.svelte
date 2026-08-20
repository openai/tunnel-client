<script lang="ts">
  import { onDestroy } from "svelte";
  import { fetchJSON } from "../lib/api";
  import { fmtUptime } from "../lib/format";
  import type { BadgeKind, ProxyRouteSummary, StatusResponse } from "../lib/types";
  import ChannelTable from "./ChannelTable.svelte";

  export let active = false;

  let status: StatusResponse | null = null;
  let errorMessage = "";
  let refreshTimer: number | undefined;

  const warningHeading = "Warnings";

  $: if (active) {
    startPolling();
  } else {
    stopPolling();
  }

  function routeMode(route?: ProxyRouteSummary): "proxy" | "direct" | "unknown" {
    if (!route?.route_mode) return "unknown";
    if (route.route_mode === "proxy") return "proxy";
    if (route.route_mode === "direct") return "direct";
    return "unknown";
  }

  function routeModeBadge(mode: "proxy" | "direct" | "unknown"): BadgeKind {
    if (mode === "proxy") return "ok";
    return "warn";
  }

  function routeProxyURL(route?: ProxyRouteSummary): string {
    const mode = routeMode(route);
    if (mode === "direct") {
      return "direct";
    }
    return route?.proxy_url || "—";
  }

  async function refreshStatus(): Promise<void> {
    errorMessage = "";
    try {
      status = await fetchJSON<StatusResponse>("/api/status");
    } catch (err) {
      errorMessage = `error: ${String(err)}`;
    }
  }

  function copyTunnelId(): void {
    const id = status?.control_plane_tunnel_id;
    if (!id) return;
    if (navigator?.clipboard?.writeText) {
      navigator.clipboard.writeText(id);
    }
  }

  function startPolling(): void {
    if (refreshTimer) return;
    refreshStatus();
    refreshTimer = window.setInterval(() => {
      if (active) {
        refreshStatus();
      }
    }, 10000);
  }

  function stopPolling(): void {
    if (refreshTimer) {
      window.clearInterval(refreshTimer);
      refreshTimer = undefined;
    }
  }

  onDestroy(() => {
    stopPolling();
  });
</script>

<div class="grid">
  <div class="card span-6">
    <div class="muted small">Client</div>
    <div class="kv" style="margin-top: 8px">
      <div class="muted">Version</div>
      <div class="mono">{status?.version || "—"}</div>
      <div class="muted">Instance ID</div>
      <div class="mono">{status?.client_instance_id || "—"}</div>
      <div class="muted">Uptime</div>
      <div class="mono">{fmtUptime(status?.uptime_seconds || 0)}</div>
      <div class="muted">Health addr</div>
      <div class="mono">{status?.health_listen_addr || "—"}</div>
    </div>
  </div>

  <div class="card span-6">
    <div class="muted small">Control plane</div>
    <div class="kv" style="margin-top: 8px">
      <div class="muted">Base URL</div>
      <div class="mono">{status?.control_plane_base_url || "—"}</div>
      <div class="muted">Tunnel ID</div>
      <div class="row" style="align-items: baseline">
        <span class="mono">{status?.control_plane_tunnel_id || "—"}</span>
        <button class="small" type="button" on:click={copyTunnelId}>Copy</button>
      </div>
      <div class="muted">Tunnel name</div>
      <div class="mono">{status?.tunnel_metadata?.name || "—"}</div>
      <div class="muted">Description</div>
      <div class="mono">{status?.tunnel_metadata?.description || "—"}</div>
      <div class="muted">Poll timeout</div>
      <div class="mono">{status?.control_plane_poll_timeout || "—"}</div>
      <div class="muted">Max inflight</div>
      <div class="mono">
        {status?.control_plane_max_inflight ?? "—"}
      </div>
      <div class="muted">Proxy mode</div>
      <div>
        <span
          class={`badge ${routeModeBadge(routeMode(status?.control_plane_route))}`}
        >
          {routeMode(status?.control_plane_route)}
        </span>
      </div>
      <div class="muted">Proxy ID</div>
      <div class="mono">{status?.control_plane_route?.proxy_id || "—"}</div>
      <div class="muted">Proxy URL</div>
      <div class="mono">{routeProxyURL(status?.control_plane_route)}</div>
      <div class="muted">Proxy source</div>
      <div class="mono">{status?.control_plane_route?.proxy_source || "—"}</div>
    </div>
  </div>

  {#if status?.warnings?.length}
    <div class="card span-12">
      <div class="muted small">{warningHeading}</div>
      <div style="margin-top: 6px">
        {#each status.warnings as warning}
          <div>• {warning}</div>
        {/each}
      </div>
    </div>
  {/if}

  <div class="card span-12">
    <div class="muted small">Channels</div>
    <ChannelTable channels={status?.channels ?? []} mcpRoutes={status?.mcp_routes ?? []} />
  </div>

  <div class="card span-12">
    <details>
      <summary class="muted">Raw status JSON</summary>
      <pre class="pre mono" style="margin-top: 10px">
{status ? JSON.stringify(status, null, 2) : errorMessage || "—"}</pre
      >
    </details>
  </div>
</div>
