<script lang="ts">
  import { onDestroy } from "svelte";
  import { fetchJSON } from "../lib/api";
  import { formatBytes } from "../lib/format";
  import type {
    BadgeKind,
    HarpoonCall,
    HarpoonCallsResponse,
    HarpoonStatusResponse,
    HarpoonTarget,
    HarpoonTargetsResponse,
    ProxyRouteSummary,
  } from "../lib/types";

  export let active = false;

  let status: HarpoonStatusResponse | null = null;
  let targets: HarpoonTarget[] = [];
  let calls: HarpoonCall[] = [];
  let errorMessage = "";
  let refreshTimer: number | undefined;
  let captureEnabled = false;
  let labelFilter = "";

  let openCalls = new Set<string>();
  let responseView = new Map<string, boolean>();

  $: proxyRoutes = status?.proxy_routes ?? [];
  $: proxiedRoutes = proxyRoutes.filter((route) => routeMode(route) === "proxy").length;
  $: directRoutes = proxyRoutes.filter((route) => routeMode(route) === "direct").length;

  $: if (active) {
    startPolling();
  } else {
    stopPolling();
  }

  function callKey(call: HarpoonCall): string {
    return [
      call.timestamp || "",
      call.label || "",
      call.method || "",
      call.url || "",
      call.status || "",
    ].join("|");
  }

  function toggleCall(call: HarpoonCall): void {
    const key = callKey(call);
    const next = new Set(openCalls);
    if (next.has(key)) {
      next.delete(key);
    } else {
      next.add(key);
    }
    openCalls = next;
  }

  function toggleResponseView(call: HarpoonCall): void {
    const key = callKey(call);
    const next = new Map(responseView);
    const current = next.get(key) !== false;
    next.set(key, !current);
    responseView = next;
  }

  function tryPrettyJSON(text?: string): string {
    if (!text) return "";
    try {
      const parsed = JSON.parse(text);
      return JSON.stringify(parsed, null, 2);
    } catch {
      return text;
    }
  }

  function formatPayload(body?: string, isBase64?: boolean): string {
    if (!body) return "—";
    if (isBase64) {
      return `Base64 payload:\n${body}`;
    }
    return tryPrettyJSON(body);
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

  async function refreshStatus(): Promise<boolean> {
    status = await fetchJSON<HarpoonStatusResponse>("/api/harpoon/status");
    captureEnabled = !!status.capture_payloads;
    return !!status.enabled;
  }

  async function refreshTargets(): Promise<void> {
    const data = await fetchJSON<HarpoonTargetsResponse>("/api/harpoon/targets");
    targets = data.targets ?? [];
  }

  async function refreshCalls(): Promise<void> {
    const query = labelFilter ? `?label=${encodeURIComponent(labelFilter)}` : "";
    const data = await fetchJSON<HarpoonCallsResponse>(`/api/harpoon/calls${query}`);
    calls = data.calls ?? [];
  }

  async function refreshAll(): Promise<void> {
    errorMessage = "";
    try {
      const enabled = await refreshStatus();
      if (!enabled) return;
      await refreshTargets();
      await refreshCalls();
    } catch (err) {
      errorMessage = `error: ${String(err)}`;
    }
  }

  function startPolling(): void {
    if (refreshTimer) return;
    refreshAll();
    refreshTimer = window.setInterval(() => {
      if (active) refreshAll();
    }, 5000);
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

<div class="row harpoon-toolbar">
  <div class="harpoon-summary">
    <div class="muted small">Harpoon</div>
    <div class="row small" style="margin-top: 6px">
      <span class={`badge ${status?.enabled ? "ok" : "warn"}`}>
        Harpoon: {status?.enabled ? "enabled" : "disabled"}
      </span>
      <span
        class={`badge ${captureEnabled ? "warn" : "ok"}`}
        title={
          captureEnabled
            ? "Request/response bodies are stored in call history (debug only)."
            : "Request/response bodies are not stored. Enable with --harpoon.capture-payloads."
        }
      >
        Capture req/resp payloads: {captureEnabled ? "on" : "off"}
      </span>
      <span class="badge warn">
        Proxy routes: {proxyRoutes.length} ({proxiedRoutes} proxy / {directRoutes} direct)
      </span>
    </div>
    <div class="small muted" style="margin-top: 6px">
      Harpoon is the embedded MCP server that makes outbound HTTP requests by label (not raw
      URLs). It routes through the tunnel-client channel and keeps target URLs hidden.
    </div>
  </div>
  <span class="harpoon-toolbar-spacer"></span>
  <div class="row harpoon-controls">
    <label class="small" style="display:flex; align-items:center; gap:8px">
      Label
      <select bind:value={labelFilter} on:change={refreshCalls}>
        <option value="">all labels</option>
        {#each targets as target}
          {#if target.label}
            <option value={target.label}>{target.label}</option>
          {/if}
        {/each}
      </select>
    </label>
    <button type="button" on:click={refreshAll}>Refresh</button>
    <span class="muted small">{errorMessage}</span>
  </div>
</div>

{#if !status?.enabled}
  <div class="harpoon-disabled muted">
    {status?.reason || "Harpoon disabled"}
  </div>
{:else}
  <div class="grid" style="margin-top: 12px">
    <div class="card span-12">
      <div class="muted small">Harpoon policy</div>
      <div class="kv" style="margin-top: 8px">
        <div class="muted">allow_plaintext_http</div>
        <div class="mono">{String(!!status?.allow_plaintext_http)}</div>
        <div class="muted">max_response_bytes</div>
        <div class="mono">{formatBytes(status?.max_response_bytes)}</div>
        <div class="muted">max_redirects</div>
        <div class="mono">{status?.max_redirects ?? "—"}</div>
      </div>
    </div>
    <div class="card span-12">
      <div class="muted small">Harpoon proxy routes</div>
      <div class="harpoon-table-wrap">
        <table class="harpoon-table harpoon-proxy-routes-table" style="margin-top: 12px">
          <thead>
            <tr>
              <th>Target</th>
              <th>Mode</th>
              <th>Proxy ID</th>
              <th>Proxy URL</th>
              <th>Proxy source</th>
            </tr>
          </thead>
          <tbody>
            {#if proxyRoutes.length === 0}
              <tr>
                <td colspan="5" class="muted">No harpoon proxy routes.</td>
              </tr>
            {:else}
              {#each proxyRoutes as route}
                {@const mode = routeMode(route)}
                <tr>
                  <td class="mono">{route.name || route.target || "—"}</td>
                  <td>
                    <span class={`badge ${routeModeBadge(mode)}`}>{mode}</span>
                  </td>
                  <td class="mono">{route.proxy_id || "—"}</td>
                  <td class="mono">{routeProxyURL(route)}</td>
                  <td class="mono">{route.proxy_source || "—"}</td>
                </tr>
              {/each}
            {/if}
          </tbody>
        </table>
      </div>
    </div>
    <div class="card span-6">
      <div class="muted small">Targets</div>
      <div class="harpoon-table-wrap">
        <table class="harpoon-table harpoon-targets-table" style="margin-top: 12px">
          <thead>
            <tr>
              <th>Label</th>
              <th>Description</th>
              <th>URL</th>
            </tr>
          </thead>
          <tbody>
            {#if targets.length === 0}
              <tr>
                <td colspan="3" class="muted">No targets configured.</td>
              </tr>
            {:else}
              {#each targets as target}
                <tr>
                  <td class="mono">{target.label || "—"}</td>
                  <td>
                    {target.description || "—"}
                    {#if target.category || target.source}
                      <div class="muted small">
                        category/source: {target.category || target.source || "—"}
                        {#if target.source && target.category && target.source !== target.category}
                          · source: {target.source}
                        {/if}
                      </div>
                    {/if}
                    {#if target.tags && target.tags.length > 0}
                      <div class="muted small">tags: {target.tags.join(", ")}</div>
                    {/if}
                    {#if target.inclusion_reason}
                      <div class="muted small">
                        inclusion_reason: {target.inclusion_reason}
                      </div>
                    {/if}
                  </td>
                  <td class="mono">{target.url || "—"}</td>
                </tr>
              {/each}
            {/if}
          </tbody>
        </table>
      </div>
    </div>
    <div class="card span-6">
      <div class="muted small">Recent calls (last 100)</div>
      <div class="harpoon-calls" style="margin-top: 12px">
        {#if calls.length === 0}
          <div class="muted small">No recent calls.</div>
        {:else}
          {#each calls as call}
            {@const key = callKey(call)}
            {@const isOpen = openCalls.has(key)}
            {@const hasTransformed = !!call.response_body_transformed}
            {@const showTransformed = hasTransformed ? responseView.get(key) !== false : false}
            <div class="harpoon-call">
              <div class="harpoon-call-header">
                <button class="harpoon-toggle" type="button" on:click={() => toggleCall(call)}>
                  {isOpen ? "–" : "+"}
                </button>
                <div class="mono small">
                  {call.timestamp ? new Date(call.timestamp).toISOString() : "—"}
                </div>
                <div class="mono small">{call.label || "—"}</div>
                <div class="mono small">{call.method || "—"}</div>
                <div class="mono small">{call.status ?? "—"}</div>
                <div class="mono small">
                  {call.latency_ms != null ? `${call.latency_ms}ms` : "—"}
                </div>
                <div class="mono small">
                  {formatBytes(call.req_bytes)} / {formatBytes(call.resp_bytes)}
                </div>
                <div class="small">{call.error || "—"}</div>
              </div>
              {#if isOpen}
                <div class="harpoon-call-details">
                  {#if !captureEnabled}
                    <div class="muted small">Payload capture is disabled.</div>
                  {:else}
                    <div class="harpoon-payload">
                      <div class="muted small">Request body</div>
                      <pre class="pre mono">
{formatPayload(call.request_body, false)}</pre
                      >
                    </div>
                    <div class="harpoon-payload">
                      <div class="muted small harpoon-response-title">
                        Response body
                        <button
                          class="harpoon-toggle-view"
                          type="button"
                          disabled={!hasTransformed}
                          on:click={() => toggleResponseView(call)}
                        >
                          {#if hasTransformed}
                            {showTransformed ? "Show original" : "Show transformed"}
                          {:else}
                            Original
                          {/if}
                        </button>
                      </div>
                      <pre class="pre mono">
{formatPayload(
  hasTransformed && showTransformed
    ? call.response_body_transformed
    : call.response_body,
  hasTransformed && showTransformed ? false : call.body_is_base64,
)}</pre
                      >
                    </div>
                  {/if}
                </div>
              {/if}
            </div>
          {/each}
        {/if}
      </div>
    </div>
  </div>
{/if}
