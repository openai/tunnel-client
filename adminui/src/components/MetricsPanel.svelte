<script lang="ts">
  import { onMount } from "svelte";
  import { fetchText } from "../lib/api";
  import { fmtTimestampSeconds } from "../lib/format";
  import {
    extractInterestingMetricKeys,
    maxMetric,
    parseMetrics,
    sumMetric,
  } from "../lib/metrics";

  let rawMetrics = "";
  let errorMessage = "";

  let lastPoll = "—";
  let pollCycles = "—";
  let queueState = "—";
  let workerState = "—";
  let pollErrors = "—";

  async function refreshMetrics(): Promise<void> {
    errorMessage = "";
    try {
      rawMetrics = await fetchText("/metrics");
      const parsed = parseMetrics(rawMetrics);

      const lastPollValue = maxMetric(parsed, [
        "commands_poll_last_successful_timestamp_seconds",
      ]);
      const pollCyclesValue = sumMetric(parsed, [
        "commands_poll_cycles_total",
        "commands_poll_cycles",
      ]);
      const queueLength = maxMetric(parsed, [
        "commands_queue_length",
        "commands_queue_length_total",
        "queue_length",
      ]);
      const queueCapacity = maxMetric(parsed, [
        "commands_queue_capacity",
        "commands_queue_capacity_total",
        "queue_capacity",
      ]);
      const workerOcc = maxMetric(parsed, [
        "dispatcher_worker_pool_occupancy",
        "dispatcher_worker_pool_occupancy_total",
        "worker_pool_occupancy",
      ]);
      const workerCap = maxMetric(parsed, [
        "dispatcher_worker_pool_capacity",
        "dispatcher_worker_pool_capacity_total",
        "worker_pool_capacity",
      ]);
      const pollErrorsValue = sumMetric(parsed, [
        "commands_poll_errors",
        "commands_poll_errors_total",
        "poll_errors",
        "poll_errors_total",
      ]);

      lastPoll = fmtTimestampSeconds(lastPollValue ?? undefined);
      pollCycles = pollCyclesValue == null ? "—" : pollCyclesValue.toString();
      queueState =
        queueLength == null || queueCapacity == null
          ? "—"
          : `${queueLength} / ${queueCapacity}`;
      workerState =
        workerOcc == null || workerCap == null
          ? "—"
          : `${workerOcc} / ${workerCap}`;
      pollErrors = pollErrorsValue == null ? "—" : pollErrorsValue.toString();

      if (
        queueLength == null &&
        queueCapacity == null &&
        workerOcc == null &&
        workerCap == null &&
        pollErrorsValue == null
      ) {
        const interesting = extractInterestingMetricKeys(parsed);
        errorMessage =
          interesting.length > 0
            ? `could not match expected tunnel-client metrics; found: ${interesting.join(", ")}`
            :
              "could not find tunnel-client metrics in /metrics (open /metrics to inspect)";
      }
    } catch (err) {
      errorMessage = `error: ${String(err)}`;
    }
  }

  onMount(() => {
    refreshMetrics();
  });
</script>

<div class="grid">
  <div class="card span-6">
    <div class="row">
      <div>
        <div class="muted small">Key metrics</div>
        <div class="small muted">Extracted from <span class="mono">/metrics</span></div>
      </div>
      <span style="flex:1"></span>
      <button type="button" on:click={refreshMetrics}>Refresh</button>
      <span class="muted small">{errorMessage}</span>
    </div>
    <div class="kv" style="margin-top: 12px">
      <div class="muted">Last poll success</div>
      <div class="mono">{lastPoll}</div>
      <div class="muted">Poll cycles</div>
      <div class="mono">{pollCycles}</div>
      <div class="muted">Queue</div>
      <div class="mono">{queueState}</div>
      <div class="muted">Worker pool</div>
      <div class="mono">{workerState}</div>
      <div class="muted">Poll errors</div>
      <div class="mono">{pollErrors}</div>
    </div>
  </div>

  <div class="card span-6">
    <div class="muted small">About these metrics</div>
    <div class="small" style="margin-top: 8px">
      This is a light parse of Prometheus text format and may not show every series. For full
      detail, open <a href="/metrics" target="_blank" rel="noreferrer">/metrics</a>.
    </div>
    <details style="margin-top: 12px">
      <summary class="muted">Raw metrics</summary>
      <pre class="pre mono" style="margin-top: 10px">{rawMetrics || "—"}</pre>
    </details>
  </div>
</div>
