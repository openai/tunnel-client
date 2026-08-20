<script lang="ts">
  import { onDestroy, onMount } from "svelte";
  import { fetchJSON } from "../lib/api";
  import { fmtTimestamp } from "../lib/format";
  import { buildOAuthRows, hasDetails } from "../lib/oauth";
  import type { OAuthRow, OAuthStatusResponse } from "../lib/types";

  let rows: OAuthRow[] = [];
  let errorMessage = "";
  let refreshTimer: number | undefined;
  let expandedRows = new Set<string>();

  function toggleRow(key: string): void {
    const next = new Set(expandedRows);
    if (next.has(key)) {
      next.delete(key);
    } else {
      next.add(key);
    }
    expandedRows = next;
  }

  async function refreshOAuth(): Promise<void> {
    errorMessage = "";
    try {
      const payload = await fetchJSON<OAuthStatusResponse>("/api/oauth");
      rows = buildOAuthRows(payload);
      if (payload.error) {
        errorMessage = payload.error;
      }
    } catch (err) {
      errorMessage = `error: ${String(err)}`;
      rows = buildOAuthRows(null);
    }
  }

  onMount(() => {
    refreshOAuth();
    refreshTimer = window.setInterval(refreshOAuth, 15000);
    return () => {
      if (refreshTimer) window.clearInterval(refreshTimer);
    };
  });

  onDestroy(() => {
    if (refreshTimer) window.clearInterval(refreshTimer);
  });
</script>

<div class="grid">
  <div class="card span-12">
    <div class="row">
      <div>
        <div class="muted small">OAuth discovery</div>
        <div class="small muted">PRMD + auth server metadata discovery timeline</div>
      </div>
      <span style="flex:1"></span>
      <button type="button" on:click={refreshOAuth}>Refresh</button>
      <span class="muted small">{errorMessage}</span>
    </div>
    <table class="oauth-table" style="margin-top: 12px">
      <thead>
        <tr>
          <th></th>
          <th>Priority</th>
          <th>Step</th>
          <th>URL</th>
          <th>Status</th>
        </tr>
      </thead>
      <tbody>
        {#if rows.length === 0}
          <tr>
            <td colspan="5" class="muted">No discovery rows.</td>
          </tr>
        {:else}
          {#each rows as row}
            <tr>
              <td class="oauth-expand-cell">
                {#if hasDetails(row.details)}
                  <button
                    type="button"
                    class="oauth-expand"
                    aria-label="Toggle details"
                    on:click={() => toggleRow(row.key)}
                  >
                    {expandedRows.has(row.key) ? "−" : "+"}
                  </button>
                {/if}
              </td>
              <td>{row.priority}</td>
              <td>{row.step}</td>
              <td class="mono oauth-url">{row.url}</td>
              <td>
                <span class={`oauth-status oauth-status-${row.status}`}>{row.status}</span>
              </td>
            </tr>
            {#if hasDetails(row.details)}
              <tr class="oauth-detail-row" style:display={expandedRows.has(row.key) ? "table-row" : "none"}>
                <td colspan="5">
                  <div class="oauth-detail-wrap">
                    <div class="oauth-detail-title">OAuth metadata</div>
                    <div class="oauth-detail-kv">
                      <div class="muted">Status</div>
                      <div class="mono">{row.details.status || "—"}</div>
                      <div class="muted">Fetched at</div>
                      <div class="mono">{fmtTimestamp(row.details.fetchedAt)}</div>
                      <div class="muted">Source URL</div>
                      <div class="mono">{row.details.sourceURL || "—"}</div>
                      <div class="muted">Metadata source</div>
                      <div class="mono">{row.details.source || "—"}</div>
                      <div class="muted">HTTP status</div>
                      <div class="mono">
                        {row.details.statusCode ? `HTTP ${row.details.statusCode}` : "—"}
                      </div>
                      <div class="muted">Error</div>
                      <div class="mono">{row.details.error || "—"}</div>
                    </div>
                    <details class="oauth-detail-collapse">
                      <summary class="oauth-detail-summary">Headers</summary>
                      {#if row.details.headers && Object.keys(row.details.headers).length}
                        <pre class="pre mono oauth-detail-pre">
{JSON.stringify(row.details.headers, null, 2)}</pre
                        >
                      {:else}
                        <div class="oauth-detail-empty"></div>
                      {/if}
                    </details>
                    <details class="oauth-detail-collapse">
                      <summary class="oauth-detail-summary">Body</summary>
                      {#if row.details.body}
                        <pre class="pre mono oauth-detail-pre">
{JSON.stringify(row.details.body, null, 2)}</pre
                        >
                      {:else if row.details.bodyText}
                        <pre class="pre mono oauth-detail-pre">{row.details.bodyText}</pre>
                      {:else}
                        <div class="oauth-detail-empty"></div>
                      {/if}
                    </details>
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
