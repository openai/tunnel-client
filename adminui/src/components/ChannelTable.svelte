<script lang="ts">
  import type { BadgeKind, ChannelDetail, ChannelStatus, ProxyRouteSummary } from "../lib/types";

  export let channels: ChannelStatus[] = [];
  export let mcpRoutes: ProxyRouteSummary[] = [];

  let openRows = new Set<number>();
  let routeByName = new Map<string, ProxyRouteSummary>();

  $: routeByName = new Map(
    (mcpRoutes || [])
      .map((route) => [route?.name || "", route] as const)
      .filter(([name]) => name !== ""),
  );

  function toggleRow(index: number): void {
    const next = new Set(openRows);
    if (next.has(index)) {
      next.delete(index);
    } else {
      next.add(index);
    }
    openRows = next;
  }

  function channelDetails(channel: ChannelStatus): ChannelDetail[] {
    const details = Array.isArray(channel.details) ? channel.details : [];
    const allowed = new Set(["http-streamable", "stdio"]);
    if (!allowed.has(channel.transport_kind || "")) {
      return [];
    }
    return details.filter((detail) => detail?.key || detail?.value);
  }

  function routeForChannel(channel: ChannelStatus): ProxyRouteSummary | undefined {
    if (!channel?.name) return undefined;
    return routeByName.get(channel.name);
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

  function mergedDetails(channel: ChannelStatus, route?: ProxyRouteSummary): ChannelDetail[] {
    const details = channelDetails(channel);
    if (!route) {
      return details;
    }
    const proxyDetails: ChannelDetail[] = [
      { key: "proxy_mode", value: routeMode(route) },
      { key: "proxy_id", value: route.proxy_id || "—" },
      { key: "proxy_source", value: route.proxy_source || "—" },
      { key: "proxy_url", value: routeProxyURL(route) },
      { key: "proxy_target", value: route.target || "—" },
    ];
    return [...proxyDetails, ...details];
  }
</script>

<table class="oauth-table" style="margin-top: 12px">
  <thead>
    <tr>
      <th class="channel-expand-cell"></th>
      <th>Name</th>
      <th>Status</th>
      <th>Server</th>
      <th>Transport</th>
      <th>Proxy</th>
      <th>Reason</th>
    </tr>
  </thead>
  <tbody>
    {#if !channels || channels.length === 0}
      <tr>
        <td class="muted" colspan="7">No channel data</td>
      </tr>
    {:else}
      {#each channels as channel, idx}
        {@const route = routeForChannel(channel)}
        {@const mode = routeMode(route)}
        {@const details = mergedDetails(channel, route)}
        <tr>
          <td class="channel-expand-cell">
            {#if details.length > 0}
              <button class="channel-expand small" type="button" on:click={() => toggleRow(idx)}>
                {openRows.has(idx) ? "− Details" : "+ Details"}
              </button>
            {/if}
          </td>
          <td class="mono">{channel.name || "—"}</td>
          <td>
            <span class={`badge ${channel.enabled ? "ok" : "warn"}`}>
              {channel.enabled ? "enabled" : "disabled"}
            </span>
          </td>
          <td class="mono">{channel.server_kind || "—"}</td>
          <td class="mono">{channel.transport_kind || "—"}</td>
          <td>
            <span class={`badge ${routeModeBadge(mode)}`}>{mode}</span>
            <div class="mono small" style="margin-top: 4px">{route?.proxy_id || "—"}</div>
          </td>
          <td class="small">{channel.reason || "—"}</td>
        </tr>
        {#if details.length > 0}
          <tr class="channel-detail-row" hidden={!openRows.has(idx)}>
            <td class="channel-expand-cell"></td>
            <td colspan="6">
              <div class="channel-detail-wrap">
                <div class="muted small">Details</div>
                <div class="channel-detail-kv">
                  {#each details as detail}
                    <div class="muted mono">{detail.key || "—"}</div>
                    <div class="mono">{detail.value || "—"}</div>
                  {/each}
                </div>
              </div>
            </td>
          </tr>
        {/if}
      {/each}
    {/if}
  </tbody>
</table>
