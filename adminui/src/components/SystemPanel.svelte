<script lang="ts">
  import { onDestroy } from "svelte";
  import { fetchJSON } from "../lib/api";
  import { fmtTimestamp } from "../lib/format";
  import type {
    BadgeKind,
    ProxyCheckRecord,
    ProxyRouteHealthSummary,
    ProxyRouteSummary,
    SystemResponse,
  } from "../lib/types";

  export let active = false;

  let system: SystemResponse | null = null;
  let errorMessage = "";
  let refreshTimer: number | undefined;
  let openRoutes = new Set<string>();

  $: if (active) {
    startPolling();
  } else {
    stopPolling();
  }

  function routeKey(summary: ProxyRouteHealthSummary): string {
    const route = summary.route;
    return [route?.kind || "", route?.name || "", route?.target || ""].join("|");
  }

  function toggleRoute(key: string): void {
    const next = new Set(openRoutes);
    if (next.has(key)) {
      next.delete(key);
    } else {
      next.add(key);
    }
    openRoutes = next;
  }

  function routeMode(route?: ProxyRouteSummary): "direct" | "proxy" | "unknown" {
    if (!route?.route_mode) return "unknown";
    if (route.route_mode === "direct") return "direct";
    if (route.route_mode === "proxy") return "proxy";
    return "unknown";
  }

  function routeModeBadge(kind: "direct" | "proxy" | "unknown"): BadgeKind {
    if (kind === "proxy") return "ok";
    return "warn";
  }

  function routeProxyURL(route?: ProxyRouteSummary): string {
    const mode = routeMode(route);
    if (mode === "direct") return "direct";
    return route?.proxy_url || "—";
  }

  function reachability(summary: ProxyRouteHealthSummary): "reachable" | "unreachable" | "direct" | "unknown" {
    const state = (summary.health_state || "").toLowerCase();
    if (state === "healthy") return "reachable";
    if (state === "unhealthy") return "unreachable";
    if (state === "direct") return "direct";
    const mode = routeMode(summary.route);
    if (mode === "direct") return "direct";
    return "unknown";
  }

  function reachabilityBadge(status: "reachable" | "unreachable" | "direct" | "unknown"): BadgeKind {
    if (status === "reachable") return "ok";
    if (status === "unreachable") return "bad";
    return "warn";
  }

  function latestFailure(summary: ProxyRouteHealthSummary): ProxyCheckRecord | undefined {
    const history = summary.history || [];
    for (let idx = history.length - 1; idx >= 0; idx -= 1) {
      if (!history[idx]?.success) {
        return history[idx];
      }
    }
    return undefined;
  }

  function latestError(summary: ProxyRouteHealthSummary): string {
    const failure = latestFailure(summary);
    if (!failure) return "—";
    const parts = [failure.error_phase, failure.error_reason, failure.http_status_category].filter(Boolean);
    if (parts.length === 0) {
      return "unreachable";
    }
    return parts.join(" / ");
  }

  function checkStatus(record: ProxyCheckRecord): "reachable" | "unreachable" {
    return record.success ? "reachable" : "unreachable";
  }

  function checkBadge(status: "reachable" | "unreachable"): BadgeKind {
    return status === "reachable" ? "ok" : "bad";
  }

  async function refreshSystem(): Promise<void> {
    errorMessage = "";
    try {
      system = await fetchJSON<SystemResponse>("/api/system");
    } catch (err) {
      errorMessage = `error: ${String(err)}`;
    }
  }

  function startPolling(): void {
    if (refreshTimer) return;
    refreshSystem();
    refreshTimer = window.setInterval(() => {
      if (active) {
        refreshSystem();
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
  <div class="card span-12">
    <div class="row">
      <div>
        <div class="muted small">System observability</div>
        <div class="small muted">TLS trust, proxy identity map, and route reachability</div>
      </div>
      <span style="flex: 1"></span>
      <button type="button" on:click={refreshSystem}>Refresh</button>
      <span class="muted small">{errorMessage}</span>
    </div>
  </div>

  <div class="card span-6">
    <div class="muted small">System trust</div>
    <div class="kv" style="margin-top: 8px">
      <div class="muted">Enabled</div>
      <div>
        <span class={`badge ${system?.tls?.system_trust?.enabled ? "ok" : "warn"}`}>
          {system?.tls?.system_trust?.enabled ? "yes" : "no"}
        </span>
      </div>
      <div class="muted">Source</div>
      <div class="mono">{system?.tls?.system_trust?.source || "—"}</div>
      <div class="muted">Source paths</div>
      <div class="mono">
        {#if system?.tls?.system_trust?.source_paths?.length}
          {system.tls.system_trust.source_paths.join(", ")}
        {:else}
          —
        {/if}
      </div>
      <div class="muted">Fallback note</div>
      <div>{system?.tls?.system_trust?.fallback_note || "—"}</div>
    </div>
  </div>

  <div class="card span-6">
    <div class="muted small">Extra bundle (--ca-bundle)</div>
    {#if system?.tls?.extra_bundle}
      <div class="kv" style="margin-top: 8px">
        <div class="muted">Path</div>
        <div class="mono">{system.tls.extra_bundle.path || "—"}</div>
        <div class="muted">Cert count</div>
        <div class="mono">{system.tls.extra_bundle.cert_count ?? 0}</div>
        <div class="muted">Parse errors</div>
        <div>
          <span class={`badge ${(system.tls.extra_bundle.parse_errors || 0) > 0 ? "bad" : "ok"}`}>
            {system.tls.extra_bundle.parse_errors ?? 0}
          </span>
        </div>
      </div>
    {:else}
      <div class="muted small" style="margin-top: 8px">No extra CA bundle configured.</div>
    {/if}
  </div>

  {#if system?.tls?.extra_bundle?.certificates?.length}
    <div class="card span-12">
      <div class="muted small">Extra bundle certificates</div>
      <div class="table-scroll" style="margin-top: 12px">
        <table class="oauth-table system-certs-table">
          <thead>
            <tr>
              <th>Name</th>
              <th>Subject</th>
              <th>Issuer</th>
              <th>Not after</th>
              <th>Parse status</th>
              <th>Cert ID</th>
            </tr>
          </thead>
          <tbody>
            {#each system.tls.extra_bundle.certificates as cert}
              <tr>
                <td>{cert.name || cert.description || "—"}</td>
                <td class="mono">{cert.subject_cn || "—"}</td>
                <td class="mono">{cert.issuer_cn || "—"}</td>
                <td class="mono">{fmtTimestamp(cert.not_after)}</td>
                <td>
                  <span class={`badge ${cert.parse_status === "ok" ? "ok" : "bad"}`}>
                    {cert.parse_status || "unknown"}
                  </span>
                </td>
                <td class="mono">{cert.cert_id || "—"}</td>
              </tr>
            {/each}
          </tbody>
        </table>
      </div>
    </div>
  {/if}

  <div class="card span-12">
    <div class="muted small">Proxy identity map</div>
    <div class="table-scroll" style="margin-top: 12px">
      <table class="oauth-table system-proxy-map-table">
        <thead>
          <tr>
            <th>Proxy ID</th>
            <th>Proxy URL</th>
            <th>Source</th>
          </tr>
        </thead>
        <tbody>
          {#if !system?.proxy_identity_map?.length}
            <tr>
              <td class="muted" colspan="3">No proxied routes.</td>
            </tr>
          {:else}
            {#each system.proxy_identity_map as record}
              <tr>
                <td class="mono">{record.proxy_id || "—"}</td>
                <td class="mono">{record.proxy_url || "—"}</td>
                <td class="mono">{record.proxy_source || "—"}</td>
              </tr>
            {/each}
          {/if}
        </tbody>
      </table>
    </div>
  </div>

  <div class="card span-12">
    <div class="muted small">Route reachability</div>
    <div class="table-scroll" style="margin-top: 12px">
      <table class="oauth-table system-route-table">
        <thead>
          <tr>
            <th class="system-expand-cell"></th>
            <th>Route</th>
            <th>Target</th>
            <th>Mode</th>
            <th>Reachability</th>
            <th>Proxy ID</th>
            <th>Proxy URL</th>
            <th>Proxy source</th>
            <th>Last check</th>
            <th>Last error</th>
          </tr>
        </thead>
        <tbody>
          {#if !system?.proxy_health?.length}
            <tr>
              <td class="muted" colspan="10">No route health data.</td>
            </tr>
          {:else}
            {#each system.proxy_health as summary}
              {@const rowKey = routeKey(summary)}
              {@const status = reachability(summary)}
              {@const mode = routeMode(summary.route)}
              {@const history = summary.history || []}
              <tr>
                <td class="system-expand-cell">
                  {#if history.length > 0}
                    <button class="system-expand small" type="button" on:click={() => toggleRoute(rowKey)}>
                      {openRoutes.has(rowKey) ? "− History" : "+ History"}
                    </button>
                  {/if}
                </td>
                <td class="mono">
                  {summary.route?.kind || "—"}
                  <div class="small muted">{summary.route?.name || "—"}</div>
                </td>
                <td class="mono">{summary.route?.target || "—"}</td>
                <td>
                  <span class={`badge ${routeModeBadge(mode)}`}>{mode}</span>
                </td>
                <td>
                  <span class={`badge ${reachabilityBadge(status)}`}>{status}</span>
                </td>
                <td class="mono">{summary.route?.proxy_id || "—"}</td>
                <td class="mono">{routeProxyURL(summary.route)}</td>
                <td class="mono">{summary.route?.proxy_source || "—"}</td>
                <td class="mono">{fmtTimestamp(summary.last_check)}</td>
                <td class="small">{latestError(summary)}</td>
              </tr>
              {#if history.length > 0}
                <tr class="system-history-row" hidden={!openRoutes.has(rowKey)}>
                  <td class="system-expand-cell"></td>
                  <td colspan="9">
                    <div class="system-history-wrap">
                      <div class="system-history-head small">
                        <span class="muted">last_check</span>
                        <span class="mono">{fmtTimestamp(summary.last_check)}</span>
                        <span class="muted">last_success</span>
                        <span class="mono">{fmtTimestamp(summary.last_success)}</span>
                      </div>
                      <div class="table-scroll">
                        <table class="system-history-table">
                          <thead>
                            <tr>
                              <th>Timestamp</th>
                              <th>Result</th>
                              <th>TCP ms</th>
                              <th>CONNECT ms</th>
                              <th>HTTP category</th>
                              <th>Error phase</th>
                              <th>Error reason</th>
                            </tr>
                          </thead>
                          <tbody>
                            {#each history as check}
                              {@const checkResult = checkStatus(check)}
                              <tr>
                                <td class="mono">{fmtTimestamp(check.timestamp)}</td>
                                <td>
                                  <span class={`badge ${checkBadge(checkResult)}`}>{checkResult}</span>
                                </td>
                                <td class="mono">{check.tcp_duration_ms ?? "—"}</td>
                                <td class="mono">{check.connect_duration_ms ?? "—"}</td>
                                <td class="mono">{check.http_status_category || "—"}</td>
                                <td class="mono">{check.error_phase || "—"}</td>
                                <td class="mono">{check.error_reason || "—"}</td>
                              </tr>
                            {/each}
                          </tbody>
                        </table>
                      </div>
                    </div>
                  </td>
                </tr>
              {/if}
            {/each}
          {/if}
        </tbody>
      </table>
    </div>
  </div>

  <div class="card span-12">
    <details>
      <summary class="muted">Raw system JSON</summary>
      <pre class="pre mono" style="margin-top: 10px">
{system ? JSON.stringify(system, null, 2) : errorMessage || "—"}</pre
      >
    </details>
  </div>
</div>
