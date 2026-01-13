(function () {
  const $ = (id) => document.getElementById(id);
  const panels = ["overview", "metrics", "logs"];
  const tabs = Array.from(document.querySelectorAll(".tab"));

  function selectTab(name) {
    tabs.forEach((t) =>
      t.setAttribute("aria-selected", t.dataset.tab === name ? "true" : "false")
    );
    panels.forEach((p) =>
      $("panel-" + p).setAttribute("aria-hidden", p === name ? "false" : "true")
    );
  }

  tabs.forEach((t) =>
    t.addEventListener("click", () => selectTab(t.dataset.tab))
  );

  function setBadge(id, kind, text) {
    const el = $(id);
    el.className = "badge " + kind;
    el.textContent = text;
  }

  async function fetchText(path) {
    const res = await fetch(path, { cache: "no-store" });
    if (!res.ok) throw new Error(res.status + " " + res.statusText);
    return await res.text();
  }

  async function fetchJSON(path) {
    const res = await fetch(path, { cache: "no-store" });
    if (!res.ok) throw new Error(res.status + " " + res.statusText);
    return await res.json();
  }

  function fmtUptime(seconds) {
    seconds = Math.max(0, Math.floor(seconds || 0));
    const d = Math.floor(seconds / 86400);
    seconds -= d * 86400;
    const h = Math.floor(seconds / 3600);
    seconds -= h * 3600;
    const m = Math.floor(seconds / 60);
    seconds -= m * 60;
    const parts = [];
    if (d) parts.push(d + "d");
    if (h) parts.push(h + "h");
    if (m) parts.push(m + "m");
    parts.push(seconds + "s");
    return parts.join(" ");
  }

  function copy(text) {
    if (!text) return;
    if (navigator && navigator.clipboard && navigator.clipboard.writeText) {
      navigator.clipboard.writeText(text);
    }
  }

  async function refreshHealth() {
    try {
      const h = (await fetchText("/healthz")).trim();
      setBadge("badgeHealth", "ok", "Health: " + h);
    } catch (e) {
      setBadge("badgeHealth", "bad", "Health: error");
    }

    try {
      const r = (await fetchText("/readyz")).trim();
      setBadge("badgeReady", "ok", "Ready: " + r);
    } catch (e) {
      setBadge("badgeReady", "bad", "Ready: error");
    }
  }

  function renderWarnings(warnings) {
    const card = $("warningsCard");
    const list = $("warnings");
    list.textContent = "";
    if (!warnings || warnings.length === 0) {
      card.style.display = "none";
      return;
    }
    card.style.display = "block";
    warnings.forEach((w) => {
      const div = document.createElement("div");
      div.textContent = "• " + w;
      list.appendChild(div);
    });
  }

  async function refreshStatus() {
    try {
      const s = await fetchJSON("/api/status");
      $("statusJSON").textContent = JSON.stringify(s, null, 2);

      $("vVersion").textContent = s.version || "—";
      $("vUptime").textContent = fmtUptime(s.uptime_seconds || 0);
      $("vHealthAddr").textContent = s.health_listen_addr || "—";
      $("vMcpUrl").textContent = s.mcp_server_url || "—";
      $("vCpBase").textContent = s.control_plane_base_url || "—";
      $("vTunnelId").textContent = s.control_plane_tunnel_id || "—";
      $("vPollTimeout").textContent = s.control_plane_poll_timeout || "—";
      $("vMaxInflight").textContent = (
        s.control_plane_max_inflight || "—"
      ).toString();

      renderWarnings(s.warnings || []);
    } catch (e) {
      $("statusJSON").textContent = "error: " + e;
    }
  }

  $("copyTunnelId").addEventListener("click", () =>
    copy($("vTunnelId").textContent)
  );

  // ---- Metrics ----
  function parseMetrics(text) {
    const out = new Map();
    const lines = (text || "").split("\n");
    for (const line of lines) {
      if (!line || line.startsWith("#")) continue;
      const m = line.match(
        /^([a-zA-Z_:][a-zA-Z0-9_:]*)(\{[^}]*\})?\s+([-+]?(?:\d+\.?\d*|\d*\.?\d+)(?:[eE][-+]?\d+)?)$/
      );
      if (!m) continue;
      const name = m[1];
      const value = Number(m[3]);
      if (!Number.isFinite(value)) continue;
      if (!out.has(name)) out.set(name, []);
      out.get(name).push({ labels: m[2] || "", value });
    }
    return out;
  }

  function pickFirst(map, names) {
    for (const n of names) {
      const xs = map.get(n);
      if (xs && xs.length) return xs[0].value;
    }
    return null;
  }

  function fmtTimestampSeconds(ts) {
    if (!ts || !Number.isFinite(ts) || ts <= 0) return "—";
    const d = new Date(ts * 1000);
    const ageSec = Math.floor((Date.now() - d.getTime()) / 1000);
    return d.toISOString() + " (" + fmtUptime(ageSec) + " ago)";
  }

  async function refreshMetrics() {
    $("metricsErr").textContent = "";
    try {
      const t = await fetchText("/metrics");
      $("metricsRaw").textContent = t;
      const m = parseMetrics(t);

      const lastPoll = pickFirst(m, [
        "commands_poll_last_successful_timestamp_seconds",
      ]);
      const qLen = pickFirst(m, ["commands_queue_length"]);
      const qCap = pickFirst(m, ["commands_queue_capacity"]);
      const wOcc = pickFirst(m, ["dispatcher_worker_pool_occupancy"]);
      const wCap = pickFirst(m, ["dispatcher_worker_pool_capacity"]);
      const pollErrs = pickFirst(m, [
        "commands_poll_errors",
        "commands_poll_errors_total",
      ]);

      $("mLastPoll").textContent = fmtTimestampSeconds(lastPoll);
      $("mQueue").textContent =
        qLen == null || qCap == null ? "—" : qLen + " / " + qCap;
      $("mWorkers").textContent =
        wOcc == null || wCap == null ? "—" : wOcc + " / " + wCap;
      $("mPollErrors").textContent =
        pollErrs == null ? "—" : pollErrs.toString();
    } catch (e) {
      $("metricsErr").textContent = "error: " + e;
    }
  }

  $("refreshMetrics").addEventListener("click", refreshMetrics);

  // ---- Logs ----
  const logsEl = $("logs");
  const filterEl = $("filter");
  const levelEl = $("level");
  const autoscrollEl = $("autoscroll");
  const showAttrsEl = $("showAttrs");
  const pauseBtn = $("pause");

  let logEvents = [];
  let paused = false;
  let streamConnected = false;

  function levelOrder(lvl) {
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
  }

  function formatEvent(ev, includeAttrs) {
    const t = ev.time ? new Date(ev.time).toISOString() : "";
    const lvl = (ev.level || "").toUpperCase();
    const attrs = ev.attrs || {};
    const comp = attrs.component ? "[" + attrs.component + "] " : "";
    const msg = ev.message || "";
    const hint = [];
    if (attrs.request_id) hint.push("req=" + attrs.request_id);
    if (attrs.tunnel_request_id) hint.push("ts=" + attrs.tunnel_request_id);
    if (attrs.session_id) hint.push("sess=" + attrs.session_id);
    let base = [t, lvl].filter(Boolean).join(" ");
    if (base) base += " ";
    base += comp + msg;
    if (hint.length) base += " " + hint.join(" ");
    if (
      includeAttrs &&
      includeAttrs !== false &&
      showAttrsEl.checked &&
      ev.attrs
    ) {
      base += " " + JSON.stringify(ev.attrs);
    }
    return base;
  }

  function passesFilters(ev) {
    const filter = (filterEl.value || "").trim().toLowerCase();
    const minLevel = levelEl.value;
    const lvl = (ev.level || "").toLowerCase();
    if (minLevel !== "all") {
      if (levelOrder(lvl) < levelOrder(minLevel)) return false;
      if (minLevel === "error" && lvl !== "error") return false;
    }
    if (!filter) return true;
    const line = formatEvent(ev, true).toLowerCase();
    return line.includes(filter);
  }

  function appendLog(ev) {
    if (!passesFilters(ev)) return;
    const line = document.createElement("div");
    line.className = "log-line level-" + (ev.level || "info").toLowerCase();
    line.textContent = formatEvent(ev, false);
    logsEl.appendChild(line);
    if (autoscrollEl.checked) {
      logsEl.scrollTop = logsEl.scrollHeight;
    }
  }

  function rerenderLogs() {
    logsEl.textContent = "";
    for (const ev of logEvents) appendLog(ev);
  }

  filterEl.addEventListener("input", rerenderLogs);
  levelEl.addEventListener("change", rerenderLogs);
  showAttrsEl.addEventListener("change", rerenderLogs);

  $("clear").addEventListener("click", () => {
    logsEl.textContent = "";
  });

  pauseBtn.addEventListener("click", () => {
    paused = !paused;
    pauseBtn.textContent = paused ? "Resume" : "Pause";
    if (!paused) rerenderLogs();
  });

  function updateStreamBadge() {
    const kind = streamConnected ? "ok" : "warn";
    const txt = streamConnected ? "Logs: connected" : "Logs: connecting";
    setBadge("badgeStream", kind, txt);
  }

  async function startLogs() {
    $("logErr").textContent = "";
    updateStreamBadge();
    try {
      const initial = await fetchJSON("/api/logs?limit=500");
      logEvents = initial.events || [];
      rerenderLogs();
    } catch (e) {
      $("logErr").textContent = "error loading initial logs: " + e;
    }

    try {
      const es = new EventSource("/api/logs/stream");
      es.onopen = () => {
        streamConnected = true;
        updateStreamBadge();
      };
      es.addEventListener("log", (evt) => {
        try {
          const ev = JSON.parse(evt.data);
          logEvents.push(ev);
          if (logEvents.length > 5000) logEvents.shift();
          if (!paused) appendLog(ev);
        } catch (e) {}
      });
      es.onerror = () => {
        streamConnected = false;
        updateStreamBadge();
      };
    } catch (e) {
      $("logErr").textContent = "error starting log stream: " + e;
    }
  }

  (async function main() {
    await refreshHealth();
    await refreshStatus();
    await refreshMetrics();
    await startLogs();

    setInterval(refreshHealth, 5000);
    setInterval(refreshStatus, 10000);
  })();
})();
