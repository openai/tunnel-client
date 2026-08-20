<script lang="ts">
  import { onDestroy, onMount } from "svelte";
  import { fetchTextResponse } from "./lib/api";
  import { healthBadgeFromResponse, readyBadgeFromResponse } from "./lib/badges";
  import type { BadgeKind } from "./lib/types";
  import OverviewPanel from "./components/OverviewPanel.svelte";
  import MetricsPanel from "./components/MetricsPanel.svelte";
  import OAuthPanel from "./components/OAuthPanel.svelte";
  import HarpoonPanel from "./components/HarpoonPanel.svelte";
  import SystemPanel from "./components/SystemPanel.svelte";
  import LogsPanel from "./components/LogsPanel.svelte";
  import CodexPanel from "./components/CodexPanel.svelte";

  const tabs = [
    { id: "overview", label: "Overview" },
    { id: "metrics", label: "Metrics" },
    { id: "oauth", label: "OAuth" },
    { id: "harpoon", label: "Harpoon" },
    { id: "system", label: "System" },
    { id: "codex", label: "Assistant" },
    { id: "logs", label: "Logs" },
  ];

  let activeTab = "overview";
  let healthBadge = { kind: "warn" as BadgeKind, text: "Health: …" };
  let readyBadge = { kind: "warn" as BadgeKind, text: "Ready: …" };
  let logsBadge = { kind: "warn" as BadgeKind, text: "Logs: connecting" };

  let healthInterval: number | undefined;
  let readyInterval: number | undefined;

  function normalizeTab(tabID: string): string {
    return tabs.some((tab) => tab.id === tabID) ? tabID : "overview";
  }

  function updateTabFromHash(): void {
    activeTab = normalizeTab(window.location.hash.replace(/^#/, ""));
  }

  function selectTab(tabID: string): void {
    activeTab = normalizeTab(tabID);
    if (window.location.hash !== `#${activeTab}`) {
      window.history.replaceState(null, "", `#${activeTab}`);
    }
  }

  async function refreshHealth(): Promise<void> {
    try {
      healthBadge = healthBadgeFromResponse(await fetchTextResponse("/healthz"));
    } catch {
      healthBadge = { kind: "bad", text: "Health: error" };
    }
  }

  async function refreshReady(): Promise<void> {
    try {
      readyBadge = readyBadgeFromResponse(await fetchTextResponse("/readyz"));
    } catch {
      readyBadge = { kind: "bad", text: "Ready: error" };
    }
  }

  function handleLogsConnection(connected: boolean): void {
    logsBadge = {
      kind: connected ? "ok" : "warn",
      text: connected ? "Logs: connected" : "Logs: connecting",
    };
  }

  onMount(() => {
    updateTabFromHash();
    refreshHealth();
    refreshReady();
    healthInterval = window.setInterval(refreshHealth, 5000);
    readyInterval = window.setInterval(refreshReady, 5000);
    window.addEventListener("hashchange", updateTabFromHash);

    return () => {
      if (healthInterval) window.clearInterval(healthInterval);
      if (readyInterval) window.clearInterval(readyInterval);
      window.removeEventListener("hashchange", updateTabFromHash);
    };
  });

  onDestroy(() => {
    if (healthInterval) window.clearInterval(healthInterval);
    if (readyInterval) window.clearInterval(readyInterval);
  });
</script>

<div class="topbar">
  <div>
    <h1>tunnel-client</h1>
    <div class="muted small">
      Local admin UI • <span class="mono">/</span> and <span class="mono">/ui</span>
    </div>
  </div>
  <div class="row small">
    <span class={`badge ${healthBadge.kind}`}>{healthBadge.text}</span>
    <span class={`badge ${readyBadge.kind}`}>{readyBadge.text}</span>
    <span class={`badge ${logsBadge.kind}`}>{logsBadge.text}</span>
  </div>
</div>

<div class="tabs" role="tablist">
  {#each tabs as tab}
    <button
      class="tab"
      data-tab={tab.id}
      role="tab"
      aria-selected={activeTab === tab.id}
      on:click={() => selectTab(tab.id)}
    >
      {tab.label}
    </button>
  {/each}
  <span style="flex: 1"></span>
  <a class="small muted" href="/metrics" target="_blank" rel="noreferrer">
    open /metrics
  </a>
</div>

<section class="panel" id="panel-overview" aria-hidden={activeTab !== "overview"}>
  <OverviewPanel active={activeTab === "overview"} />
</section>

<section class="panel" id="panel-metrics" aria-hidden={activeTab !== "metrics"}>
  <MetricsPanel />
</section>

<section class="panel" id="panel-oauth" aria-hidden={activeTab !== "oauth"}>
  <OAuthPanel />
</section>

<section class="panel" id="panel-harpoon" aria-hidden={activeTab !== "harpoon"}>
  <HarpoonPanel active={activeTab === "harpoon"} />
</section>

<section class="panel" id="panel-system" aria-hidden={activeTab !== "system"}>
  <SystemPanel active={activeTab === "system"} />
</section>

<section class="panel" id="panel-codex" aria-hidden={activeTab !== "codex"}>
  <CodexPanel active={activeTab === "codex"} />
</section>

<section class="panel" id="panel-logs" aria-hidden={activeTab !== "logs"}>
  <LogsPanel onConnectionChange={handleLogsConnection} />
</section>
