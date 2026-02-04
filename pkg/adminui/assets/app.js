(function () {
  const $ = (id) => document.getElementById(id);
  const panels = ["overview", "metrics", "oauth", "harpoon", "logs"];
  const tabs = Array.from(document.querySelectorAll(".tab"));
  const tabListeners = [];

  function selectTab(name) {
    tabs.forEach((t) =>
      t.setAttribute("aria-selected", t.dataset.tab === name ? "true" : "false")
    );
    panels.forEach((p) => {
      const panel = $("panel-" + p);
      if (!panel) return;
      panel.setAttribute("aria-hidden", p === name ? "false" : "true");
    });
    tabListeners.forEach((fn) => {
      try {
        fn(name);
      } catch (e) {}
    });
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

  const adminUI = (window.adminUI = window.adminUI || {});
  adminUI.$ = $;
  adminUI.fetchJSON = fetchJSON;
  adminUI.fmtUptime = fmtUptime;
  adminUI.onTabChange = (fn) => {
    if (typeof fn === "function") tabListeners.push(fn);
  };

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

  function renderChannels(channels) {
    const rows = $("channelsRows");
    if (!rows) return;
    rows.textContent = "";
    if (!channels || channels.length === 0) {
      const row = document.createElement("tr");
      const cell = document.createElement("td");
      cell.colSpan = 6;
      cell.className = "muted";
      cell.textContent = "No channel data";
      row.appendChild(cell);
      rows.appendChild(row);
      return;
    }

    function renderChannelDetails(details) {
      const wrap = document.createElement("div");
      wrap.className = "channel-detail-wrap";

      const heading = document.createElement("div");
      heading.className = "muted small";
      heading.textContent = "Details";
      wrap.appendChild(heading);

      const kv = document.createElement("div");
      kv.className = "channel-detail-kv";
      details.forEach((detail) => {
        const keyCell = document.createElement("div");
        keyCell.className = "muted mono";
        keyCell.textContent = detail.key || "—";
        kv.appendChild(keyCell);

        const valueCell = document.createElement("div");
        valueCell.className = "mono";
        valueCell.textContent = detail.value || "—";
        kv.appendChild(valueCell);
      });
      wrap.appendChild(kv);
      return wrap;
    }

    channels.forEach((ch) => {
      const row = document.createElement("tr");
      const nameCell = document.createElement("td");
      nameCell.className = "mono";
      nameCell.textContent = ch.name || "—";

      const statusCell = document.createElement("td");
      const statusBadge = document.createElement("span");
      statusBadge.className = "badge " + (ch.enabled ? "ok" : "warn");
      statusBadge.textContent = ch.enabled ? "enabled" : "disabled";
      statusCell.appendChild(statusBadge);

      const serverCell = document.createElement("td");
      serverCell.className = "mono";
      serverCell.textContent = ch.server_kind || "—";

      const transportCell = document.createElement("td");
      transportCell.className = "mono";
      transportCell.textContent = ch.transport_kind || "—";

      const reasonCell = document.createElement("td");
      reasonCell.className = "small";
      reasonCell.textContent = ch.reason || "—";

      const details =
        Array.isArray(ch.details) &&
        (ch.transport_kind === "http-streamable" || ch.transport_kind === "stdio")
          ? ch.details.filter(
              (d) => d && (typeof d.key === "string" || typeof d.value === "string")
            )
          : [];
      const hasDetails = details.length > 0;

      const expandCell = document.createElement("td");
      expandCell.className = "channel-expand-cell";
      if (hasDetails) {
        const toggle = document.createElement("button");
        toggle.className = "channel-expand small";
        toggle.textContent = "+ Details";
        toggle.type = "button";
        expandCell.appendChild(toggle);

        const detailRow = document.createElement("tr");
        detailRow.className = "channel-detail-row";
        detailRow.hidden = true;

        const detailPadCell = document.createElement("td");
        detailPadCell.className = "channel-expand-cell";

        const detailCell = document.createElement("td");
        detailCell.colSpan = 5;
        detailCell.appendChild(renderChannelDetails(details));

        detailRow.appendChild(detailPadCell);
        detailRow.appendChild(detailCell);

        toggle.addEventListener("click", () => {
          const isOpen = !detailRow.hidden;
          detailRow.hidden = isOpen;
          toggle.textContent = isOpen ? "+ Details" : "− Details";
        });

        row.appendChild(expandCell);
        row.appendChild(nameCell);
        row.appendChild(statusCell);
        row.appendChild(serverCell);
        row.appendChild(transportCell);
        row.appendChild(reasonCell);
        rows.appendChild(row);
        rows.appendChild(detailRow);
        return;
      }
      row.appendChild(expandCell);

      row.appendChild(nameCell);
      row.appendChild(statusCell);
      row.appendChild(serverCell);
      row.appendChild(transportCell);
      row.appendChild(reasonCell);
      rows.appendChild(row);
    });
  }


  async function refreshStatus() {
    try {
      const s = await fetchJSON("/api/status");
      $("statusJSON").textContent = JSON.stringify(s, null, 2);

      $("vVersion").textContent = s.version || "—";
      $("vUptime").textContent = fmtUptime(s.uptime_seconds || 0);
      $("vHealthAddr").textContent = s.health_listen_addr || "—";
      $("vCpBase").textContent = s.control_plane_base_url || "—";
      $("vTunnelId").textContent = s.control_plane_tunnel_id || "—";
      const meta = s.tunnel_metadata || {};
      $("vTunnelName").textContent = meta.name || "—";
      $("vTunnelDescription").textContent = meta.description || "—";
      $("vPollTimeout").textContent = s.control_plane_poll_timeout || "—";
      $("vMaxInflight").textContent = (
        s.control_plane_max_inflight || "—"
      ).toString();

      renderWarnings(s.warnings || []);
      renderChannels(s.channels || []);
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
    for (const raw of lines) {
      const line = (raw || "").trim();
      if (!line || line.startsWith("#")) continue;
      // Prometheus text format is: name{labels} value [timestamp]
      // We parse a minimal subset (including optional timestamps) and ignore
      // NaN/Inf values.
      const m = line.match(
        /^([a-zA-Z_:][a-zA-Z0-9_:]*)(\{[^}]*\})?\s+((?:[-+]?(?:\d+\.?\d*|\d*\.?\d+)(?:[eE][-+]?\d+)?)|NaN|\+Inf|-Inf|Inf)(?:\s+\d+)?$/
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

  function firstMetricSeries(map, names) {
    for (const n of names) {
      const xs = map.get(n);
      if (xs && xs.length) return xs;
    }
    return null;
  }

  function maxMetric(map, names) {
    const xs = firstMetricSeries(map, names);
    if (!xs) return null;
    let max = null;
    for (const x of xs) {
      if (max == null || x.value > max) max = x.value;
    }
    return max;
  }

  function sumMetric(map, names) {
    const xs = firstMetricSeries(map, names);
    if (!xs) return null;
    let sum = 0;
    for (const x of xs) sum += x.value;
    return sum;
  }

  function findMetricSeries(map, names) {
    // Exact match first.
    for (const n of names) {
      const xs = map.get(n);
      if (xs && xs.length) return xs;
    }

    const keys = Array.from(map.keys());

    // Suffix match (handles exporters that namespace metric names).
    for (const n of names) {
      const matches = keys.filter((k) => k === n || k.endsWith(n));
      if (matches.length) {
        matches.sort((a, b) => a.length - b.length);
        const xs = map.get(matches[0]);
        if (xs && xs.length) return xs;
      }
    }

    // Substring match (last resort).
    for (const n of names) {
      const matches = keys.filter((k) => k.includes(n));
      if (matches.length) {
        matches.sort((a, b) => a.length - b.length);
        const xs = map.get(matches[0]);
        if (xs && xs.length) return xs;
      }
    }

    return null;
  }

  function maxMetric2(map, names) {
    const xs = findMetricSeries(map, names);
    if (!xs) return null;
    let max = null;
    for (const x of xs) {
      if (max == null || x.value > max) max = x.value;
    }
    return max;
  }

  function sumMetric2(map, names) {
    const xs = findMetricSeries(map, names);
    if (!xs) return null;
    let sum = 0;
    for (const x of xs) sum += x.value;
    return sum;
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

      const lastPoll = maxMetric2(m, [
        "commands_poll_last_successful_timestamp_seconds",
      ]);
      const pollCycles = sumMetric2(m, [
        "commands_poll_cycles_total",
        "commands_poll_cycles",
      ]);
      const qLen = maxMetric2(m, [
        "commands_queue_length",
        "commands_queue_length_total",
        "queue_length",
      ]);
      const qCap = maxMetric2(m, [
        "commands_queue_capacity",
        "commands_queue_capacity_total",
        "queue_capacity",
      ]);
      const wOcc = maxMetric2(m, [
        "dispatcher_worker_pool_occupancy",
        "dispatcher_worker_pool_occupancy_total",
        "worker_pool_occupancy",
      ]);
      const wCap = maxMetric2(m, [
        "dispatcher_worker_pool_capacity",
        "dispatcher_worker_pool_capacity_total",
        "worker_pool_capacity",
      ]);
      const pollErrs = sumMetric2(m, [
        "commands_poll_errors",
        "commands_poll_errors_total",
        "poll_errors",
        "poll_errors_total",
      ]);

      $("mLastPoll").textContent = fmtTimestampSeconds(lastPoll);
      $("mPollCycles").textContent =
        pollCycles == null ? "—" : pollCycles.toString();
      $("mQueue").textContent =
        qLen == null || qCap == null ? "—" : qLen + " / " + qCap;
      $("mWorkers").textContent =
        wOcc == null || wCap == null ? "—" : wOcc + " / " + wCap;
      $("mPollErrors").textContent =
        pollErrs == null ? "—" : pollErrs.toString();

      if (
        qLen == null &&
        qCap == null &&
        wOcc == null &&
        wCap == null &&
        pollErrs == null
      ) {
        const keys = Array.from(m.keys());
        const interesting = keys
          .filter((k) => k.includes("commands_") || k.includes("dispatcher_"))
          .slice(0, 12);
        $("metricsErr").textContent =
          interesting.length > 0
            ? "could not match expected tunnel-client metrics; found: " +
              interesting.join(", ")
            : "could not find tunnel-client metrics in /metrics (open /metrics to inspect)";
      }
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

  function formatEventForSearch(ev) {
    const t = ev.time ? new Date(ev.time).toISOString() : "";
    const lvl = (ev.level || "").toUpperCase();
    const attrs = ev.attrs || {};
    const comp = attrs.component ? "[" + attrs.component + "] " : "";
    const msg = ev.message || "";
    let base = [t, lvl].filter(Boolean).join(" ");
    if (base) base += " ";
    base += comp + msg;
    try {
      if (attrs && Object.keys(attrs).length > 0) {
        base += " " + JSON.stringify(attrs);
      }
    } catch (e) {
      // ignore
    }
    return base;
  }

  function formatEvent(ev) {
    const t = ev.time ? new Date(ev.time).toISOString() : "";
    const lvl = (ev.level || "").toUpperCase();
    const attrs = ev.attrs || {};
    const comp = attrs.component ? "[" + attrs.component + "] " : "";
    const msg = ev.message || "";
    const hint = [];

    // Common IDs
    if (attrs.request_id) hint.push("req=" + attrs.request_id);
    if (attrs.tunnel_request_id) hint.push("ts=" + attrs.tunnel_request_id);
    if (attrs.session_id) hint.push("sess=" + attrs.session_id);

    // Common diagnostics (so errors show up like the terminal output).
    if (attrs.error) hint.push("error=" + JSON.stringify(attrs.error));
    if (attrs.retry_in_ms != null)
      hint.push("retry_in_ms=" + attrs.retry_in_ms);
    if (attrs.poll_timeout_ms != null)
      hint.push("poll_timeout_ms=" + attrs.poll_timeout_ms);

    let base = [t, lvl].filter(Boolean).join(" ");
    if (base) base += " ";
    base += comp + msg;
    if (hint.length) base += " " + hint.join(" ");

    // Optional full attrs view.
    if (showAttrsEl.checked && ev.attrs) {
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
    const line = formatEventForSearch(ev).toLowerCase();
    return line.includes(filter);
  }

  function appendLog(ev) {
    if (!passesFilters(ev)) return;
    const line = document.createElement("div");
    line.className = "log-line level-" + (ev.level || "info").toLowerCase();
    line.textContent = formatEvent(ev);
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

  async function main() {
    await refreshHealth();
    await refreshStatus();
    await refreshMetrics();
    if (window.adminUI && window.adminUI.oauth && window.adminUI.oauth.refresh) {
      await window.adminUI.oauth.refresh();
    }
    await startLogs();

    setInterval(refreshHealth, 5000);
    setInterval(refreshStatus, 10000);
    setInterval(() => {
      if (window.adminUI && window.adminUI.oauth && window.adminUI.oauth.refresh) {
        window.adminUI.oauth.refresh();
      }
    }, 15000);
  }

  if (document.readyState === "loading") {
    window.addEventListener("load", () => {
      main();
    });
  } else {
    main();
  }
})();
