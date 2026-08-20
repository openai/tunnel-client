<script lang="ts">
  import { afterUpdate, onDestroy, onMount } from "svelte";
  import { fetchJSON, fetchJSONWithInit } from "../lib/api";
  import type { LogEvent, LogLevelResponse, LogsResponse, StatusResponse } from "../lib/types";

  export let onConnectionChange: (connected: boolean) => void;
  export let navigateTo: (url: string) => void = (url: string) => {
    window.location.assign(url);
  };
  export let confirmLogExport: (message: string) => boolean = (message: string) => {
    return window.confirm(message);
  };

  let logEvents: LogEvent[] = [];
  let pausedSnapshot: LogEvent[] = [];
  let filterText = "";
  let visibleLevel = "all";
  let runtimeLevel = "info";
  let supportedRuntimeLevels = ["debug", "info", "warn"];
  let autoscroll = true;
  let showAttrs = false;
  let paused = false;
  let errorMessage = "";
  let runtimeLevelMessage = "";
  let runtimeLevelLoading = false;
  let runtimeLevelUpdating = false;
  let activeRuntimeLevel = "info";
  let rawHTTPLoggingEnabled = false;
  let streamConnected = false;
  let curlExportURL = "/api/logs/export?minutes=30";
  let logsContainer: HTMLDivElement | null = null;
  let eventSource: EventSource | null = null;

  $: curlExportCommand = `curl -fsSJO "${curlExportURL}"`;

  const levelOrder = (lvl: string): number => {
    switch ((lvl || "").toLowerCase()) {
      case "debug":
        return 10;
      case "info":
        return 20;
      case "warn":
        return 30;
      case "error":
        return 40;
      default:
        return 0;
    }
  };

  const formatEventForSearch = (ev: LogEvent): string => {
    const t = ev.time ? new Date(ev.time).toISOString() : "";
    const lvl = (ev.level || "").toUpperCase();
    const attrs = ev.attrs || {};
    const comp = (attrs as Record<string, unknown>).component
      ? `[${(attrs as Record<string, unknown>).component}] `
      : "";
    const msg = ev.message || "";
    let base = [t, lvl].filter(Boolean).join(" ");
    if (base) base += " ";
    base += comp + msg;
    try {
      if (attrs && Object.keys(attrs).length > 0) {
        base += " " + JSON.stringify(attrs);
      }
    } catch {
      // ignore
    }
    return base;
  };

  const formatEvent = (ev: LogEvent): string => {
    const t = ev.time ? new Date(ev.time).toISOString() : "";
    const lvl = (ev.level || "").toUpperCase();
    const attrs = ev.attrs || {};
    const comp = (attrs as Record<string, unknown>).component
      ? `[${(attrs as Record<string, unknown>).component}] `
      : "";
    const msg = ev.message || "";
    const hint: string[] = [];

    const attrsRecord = attrs as Record<string, unknown>;
    if (attrsRecord.request_id) hint.push(`req=${attrsRecord.request_id}`);
    if (attrsRecord.tunnel_request_id) hint.push(`ts=${attrsRecord.tunnel_request_id}`);
    if (attrsRecord.session_id) hint.push(`sess=${attrsRecord.session_id}`);
    if (attrsRecord.error) hint.push(`error=${JSON.stringify(attrsRecord.error)}`);
    if (attrsRecord.retry_in_ms != null) hint.push(`retry_in_ms=${attrsRecord.retry_in_ms}`);
    if (attrsRecord.poll_timeout_ms != null) {
      hint.push(`poll_timeout_ms=${attrsRecord.poll_timeout_ms}`);
    }

    let base = [t, lvl].filter(Boolean).join(" ");
    if (base) base += " ";
    base += comp + msg;
    if (hint.length) base += " " + hint.join(" ");

    if (showAttrs && ev.attrs) {
      base += " " + JSON.stringify(ev.attrs);
    }
    return base;
  };

  const passesFilters = (ev: LogEvent): boolean => {
    const filter = filterText.trim().toLowerCase();
    const minLevel = visibleLevel;
    const lvl = (ev.level || "").toLowerCase();
    if (minLevel !== "all") {
      if (levelOrder(lvl) < levelOrder(minLevel)) return false;
      if (minLevel === "error" && lvl !== "error") return false;
    }
    if (!filter) return true;
    const line = formatEventForSearch(ev).toLowerCase();
    return line.includes(filter);
  };

  $: filteredEvents = logEvents.filter(passesFilters);
  $: displayEvents = paused ? pausedSnapshot : filteredEvents;

  function setPaused(next: boolean): void {
    paused = next;
    if (paused) {
      pausedSnapshot = filteredEvents;
    } else {
      pausedSnapshot = [];
    }
  }

  function clearLogs(): void {
    logEvents = [];
    pausedSnapshot = [];
  }

  function shouldWarnBeforeLogExport(rawUnsafeEnabled: boolean): boolean {
    return rawUnsafeEnabled && activeRuntimeLevel.toLowerCase() === "debug";
  }

  async function downloadRecentLogs(): Promise<void> {
    let rawUnsafeEnabled = rawHTTPLoggingEnabled;
    try {
      const status = await fetchJSON<StatusResponse>("/api/status");
      rawUnsafeEnabled = status.raw_http_logging_enabled === true;
      rawHTTPLoggingEnabled = rawUnsafeEnabled;
    } catch {
      // ignore status refresh failures and fall back to the latest known value
    }

    if (shouldWarnBeforeLogExport(rawUnsafeEnabled)) {
      const proceed = confirmLogExport(
        "Logs may include sensitive data because debug logging and --log.http-raw-unsafe are enabled. Download logs anyway?",
      );
      if (!proceed) return;
    }
    navigateTo("/api/logs/export?minutes=30");
  }

  function updateCurlExportURL(): void {
    curlExportURL = new URL("/api/logs/export?minutes=30", window.location.href).toString();
  }

  function updateConnection(next: boolean): void {
    if (streamConnected === next) return;
    streamConnected = next;
    onConnectionChange?.(next);
  }

  async function loadInitialLogs(): Promise<void> {
    try {
      const initial = await fetchJSON<LogsResponse>("/api/logs?limit=500");
      logEvents = initial.events ?? [];
    } catch (err) {
      errorMessage = `error loading initial logs: ${String(err)}`;
    }
  }

  async function loadRuntimeLevel(): Promise<void> {
    runtimeLevelLoading = true;
    try {
      const response = await fetchJSON<LogLevelResponse>("/api/log-level");
      runtimeLevel = response.level || runtimeLevel;
      activeRuntimeLevel = response.level || activeRuntimeLevel;
      supportedRuntimeLevels = response.supported_levels?.length
        ? response.supported_levels
        : supportedRuntimeLevels;
    } catch (err) {
      errorMessage = `error loading runtime log level: ${String(err)}`;
    } finally {
      runtimeLevelLoading = false;
    }
  }

  async function applyRuntimeLevel(): Promise<void> {
    runtimeLevelUpdating = true;
    runtimeLevelMessage = "";
    try {
      const response = await fetchJSONWithInit<LogLevelResponse>("/api/log-level", {
        method: "PUT",
        headers: {
          "content-type": "application/json",
        },
        body: JSON.stringify({ level: runtimeLevel }),
      });
      runtimeLevel = response.level || runtimeLevel;
      activeRuntimeLevel = response.level || activeRuntimeLevel;
      supportedRuntimeLevels = response.supported_levels?.length
        ? response.supported_levels
        : supportedRuntimeLevels;
      runtimeLevelMessage = `runtime log level: ${runtimeLevel}`;
    } catch (err) {
      errorMessage = `error updating runtime log level: ${String(err)}`;
    } finally {
      runtimeLevelUpdating = false;
    }
  }

  async function loadStatus(): Promise<void> {
    try {
      const status = await fetchJSON<StatusResponse>("/api/status");
      rawHTTPLoggingEnabled = status.raw_http_logging_enabled === true;
    } catch (err) {
      errorMessage = `error loading status: ${String(err)}`;
    }
  }

  function startStream(): void {
    try {
      eventSource = new EventSource("/api/logs/stream");
      eventSource.onopen = () => updateConnection(true);
      eventSource.onerror = () => updateConnection(false);
      eventSource.addEventListener("log", (evt) => {
        try {
          const ev = JSON.parse((evt as MessageEvent).data) as LogEvent;
          logEvents = [...logEvents, ev].slice(-5000);
        } catch {
          // ignore
        }
      });
    } catch (err) {
      errorMessage = `error starting log stream: ${String(err)}`;
    }
  }

  onMount(async () => {
    updateConnection(false);
    updateCurlExportURL();
    await Promise.all([loadInitialLogs(), loadRuntimeLevel(), loadStatus()]);
    startStream();
  });

  onDestroy(() => {
    eventSource?.close();
  });

  afterUpdate(() => {
    if (!autoscroll || paused || !logsContainer) return;
    logsContainer.scrollTop = logsContainer.scrollHeight;
  });
</script>

<div class="grid">
  <div class="card span-12">
    <div class="row">
      <div>
        <div class="muted small">Logs</div>
        <div class="small muted">Streaming from <span class="mono">/api/logs/stream</span> (SSE)</div>
      </div>
      <span style="flex:1"></span>
      <div class="checkbox-stack small">
        <label><input type="checkbox" bind:checked={autoscroll} /> autoscroll</label>
        <label><input type="checkbox" bind:checked={showAttrs} /> show attrs</label>
      </div>
      <button type="button" on:click={() => setPaused(!paused)}>{paused ? "Resume" : "Pause"}</button>
      <button type="button" on:click={downloadRecentLogs}>Download recent logs</button>
      <button type="button" on:click={clearLogs}>Clear</button>
    </div>
    <div class="small muted" style="margin-top: 8px">
      CLI export: <code class="mono">{curlExportCommand}</code>
    </div>
    <div class="row" style="margin-top: 10px">
      <input aria-label="Filter logs" bind:value={filterText} placeholder="filter (substring)…" />
      <select aria-label="Visible log level filter" bind:value={visibleLevel}>
        <option value="all">level: all</option>
        <option value="debug">level: debug+</option>
        <option value="info">level: info+</option>
        <option value="warn">level: warn+</option>
        <option value="error">level: error</option>
      </select>
      <span class="muted small">runtime</span>
      <select
        id="runtime-log-level"
        aria-label="Runtime log level"
        bind:value={runtimeLevel}
        disabled={runtimeLevelLoading || runtimeLevelUpdating}
      >
        {#each supportedRuntimeLevels as option}
          <option value={option}>{option}</option>
        {/each}
      </select>
      <button
        type="button"
        on:click={applyRuntimeLevel}
        disabled={runtimeLevelLoading || runtimeLevelUpdating}
      >
        {runtimeLevelUpdating ? "Applying..." : "Apply runtime level"}
      </button>
      <span class="muted small">{runtimeLevelMessage || errorMessage}</span>
    </div>
    <div class="logs mono" bind:this={logsContainer} style="margin-top: 12px">
      {#each displayEvents as ev}
        <div class={`log-line level-${(ev.level || "info").toLowerCase()}`}>
          {formatEvent(ev)}
        </div>
      {/each}
    </div>
  </div>
</div>
