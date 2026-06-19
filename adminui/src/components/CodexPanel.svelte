<script lang="ts">
  import { onDestroy } from "svelte";
  import { fetchJSON, postJSON } from "../lib/api";
  import {
    codexAuthKind,
    codexAuthText,
    codexStatusKind,
    codexStatusText,
    deriveCodexState,
  } from "../lib/codex";
  import { fmtTimestamp } from "../lib/format";
  import type {
    CodexDeviceCodeLoginResponse,
    CodexEvent,
    CodexEventsResponse,
    CodexStatusResponse,
    CodexThreadStartResponse,
    CodexTurnStartResponse,
  } from "../lib/types";

  export let active = false;

  type TranscriptTurn = {
    id: string;
    threadID: string;
    userText: string;
    assistantText: string;
    commandText: string;
    status: string;
  };

  const assistantApprovalPolicy = "never";
  const assistantSandboxType = "workspace-write";

  let status: CodexStatusResponse | null = null;
  let events: CodexEvent[] = [];
  let transcriptTurns: TranscriptTurn[] = [];
  let diagnosticEvents: CodexEvent[] = [];
  let assistantState = "not_ready";
  let canCreateChat = false;
  let canSendTurn = false;
  let activeThreadID = "";
  let workspaceLabel = "-";
  let chatLabel = "No active chat";
  let errorMessage = "";
  let refreshTimer: number | undefined;
  let stream: EventSource | undefined;

  let threadCwd = "";
  let threadModel = "";
  let threadDeveloperInstructions = "";
  let threadInjectContext = true;
  let threadBusy = false;

  let turnPrompt = "";
  let turnInjectContext = true;
  let turnBusy = false;
  let loginBusy = false;
  let cancelBusy = false;

  let assistantOutput = "";
  let commandOutput = "";

  $: assistantState = deriveCodexState(status);
  $: canCreateChat = Boolean(status?.account?.type) && Boolean(status?.ready);
  $: canSendTurn = Boolean(status?.thread?.id) && Boolean(status?.account?.type) && !turnBusy;
  $: activeThreadID = status?.thread?.id || latestThreadID(events);
  $: workspaceLabel = status?.thread?.cwd || status?.command_cwd || threadCwd || "-";
  $: chatLabel = activeThreadID || "No active chat";
  $: {
    events;
    activeThreadID;
    rebuildOutputs();
  }

  $: if (active) {
    start();
  } else {
    stop();
  }

  async function loadStatus(): Promise<void> {
    errorMessage = "";
    try {
      status = await fetchJSON<CodexStatusResponse>("/api/codex/status");
      if (!threadCwd && status?.command_cwd) {
        threadCwd = status.command_cwd;
      }
    } catch (err) {
      errorMessage = `error: ${String(err)}`;
    }
  }

  async function loadEvents(): Promise<void> {
    try {
      const response = await fetchJSON<CodexEventsResponse>("/api/codex/events?limit=200");
      events = response.events ?? [];
      rebuildOutputs();
    } catch (err) {
      errorMessage = `error: ${String(err)}`;
    }
  }

  function rebuildOutputs(): void {
    const turns = new Map<string, TranscriptTurn>();
    const order: string[] = [];
    const transcriptThreadID = activeThreadID || latestThreadID(events);

    function ensureTurn(turnID: string, threadID = ""): TranscriptTurn | null {
      const normalizedTurnID = turnID.trim();
      if (!normalizedTurnID) {
        return null;
      }

      let turn = turns.get(normalizedTurnID);
      if (!turn) {
        turn = {
          id: normalizedTurnID,
          threadID,
          userText: "",
          assistantText: "",
          commandText: "",
          status: "in_progress",
        };
        turns.set(normalizedTurnID, turn);
        order.push(normalizedTurnID);
      }
      if (threadID && !turn.threadID) {
        turn.threadID = threadID;
      }
      return turn;
    }

    for (const event of events) {
      const turnID = eventTurnID(event);
      const threadID = eventThreadID(event);
      if (transcriptThreadID && threadID && threadID !== transcriptThreadID) {
        continue;
      }

      if (event.method === "turn/started") {
        const turn = ensureTurn(turnID, threadID);
        if (turn) {
          turn.status = "in_progress";
        }
        continue;
      }

      if (event.method === "turn/completed") {
        const turn = ensureTurn(turnID, threadID);
        if (turn) {
          turn.status = "completed";
        }
        continue;
      }

      if (event.method === "item/agentMessage/delta" && event.delta) {
        const turn = ensureTurn(turnID || "active-turn", threadID);
        if (turn) {
          turn.assistantText += event.delta;
        }
        continue;
      }

      if (event.method === "item/commandExecution/outputDelta" && event.delta) {
        const turn = ensureTurn(turnID || "active-turn", threadID);
        if (turn) {
          turn.commandText += event.delta;
        }
        continue;
      }

      if (event.method !== "item/started" && event.method !== "item/completed") {
        continue;
      }

      const item = eventItem(event);
      const turn = ensureTurn(turnID, threadID);
      if (!item || !turn) {
        continue;
      }

      if (item.type === "userMessage") {
        const text = eventItemText(item);
        if (text) {
          turn.userText = text;
        }
      }

      if (item.type === "agentMessage") {
        const text = eventItemText(item);
        if (text) {
          turn.assistantText = text;
        }
      }
    }

    transcriptTurns = order
      .map((turnID) => turns.get(turnID))
      .filter((turn): turn is TranscriptTurn => Boolean(turn))
      .filter((turn) => turn.userText || turn.assistantText || turn.commandText || turn.status);

    diagnosticEvents = [...events]
      .reverse()
      .filter((event) => event.method !== "item/agentMessage/delta")
      .slice(0, 8);

    const latestTurn = transcriptTurns[transcriptTurns.length - 1];
    assistantOutput = latestTurn?.assistantText || "";
    commandOutput = latestTurn?.commandText || "";
  }

  function applyEvent(event: CodexEvent, append = true): void {
    if (append) {
      events = [...events.slice(-199), event];
    }
    rebuildOutputs();
    if (event.method === "account/login/completed" || event.method === "account/updated") {
      void loadStatus();
    }
    if (event.method === "thread/started" || event.method === "turn/completed") {
      void loadStatus();
    }
  }

  function connectStream(): void {
    if (stream) return;
    stream = new EventSource("/api/codex/events/stream");
    stream.addEventListener("codex", (raw) => {
      const event = raw as MessageEvent<string>;
      try {
        applyEvent(JSON.parse(event.data) as CodexEvent);
      } catch {
        // ignore malformed events
      }
    });
    stream.onerror = () => {
      if (active) {
        errorMessage = errorMessage || "stream reconnecting";
      }
    };
  }

  function disconnectStream(): void {
    stream?.close();
    stream = undefined;
  }

  function start(): void {
    if (refreshTimer) return;
    void Promise.all([loadStatus(), loadEvents()]);
    connectStream();
    refreshTimer = window.setInterval(() => {
      if (active) {
        void loadStatus();
      }
    }, 7000);
  }

  function stop(): void {
    if (refreshTimer) {
      window.clearInterval(refreshTimer);
      refreshTimer = undefined;
    }
    disconnectStream();
  }

  async function startDeviceCodeLogin(): Promise<void> {
    loginBusy = true;
    errorMessage = "";
    try {
      const response = await postJSON<CodexDeviceCodeLoginResponse>("/api/codex/login/device", {});
      await loadStatus();
      if (response.verification_url) {
        window.open(response.verification_url, "_blank", "noopener,noreferrer");
      }
    } catch (err) {
      errorMessage = `error: ${String(err)}`;
    } finally {
      loginBusy = false;
    }
  }

  async function cancelDeviceCodeLogin(): Promise<void> {
    cancelBusy = true;
    errorMessage = "";
    try {
      await postJSON("/api/codex/login/cancel", { login_id: status?.login?.login_id || "" });
      await loadStatus();
    } catch (err) {
      errorMessage = `error: ${String(err)}`;
    } finally {
      cancelBusy = false;
    }
  }

  async function startThread(): Promise<void> {
    threadBusy = true;
    errorMessage = "";
    try {
      await postJSON<CodexThreadStartResponse>("/api/codex/thread/start", {
        cwd: threadCwd,
        model: threadModel,
        approval_policy: assistantApprovalPolicy,
        sandbox_type: assistantSandboxType,
        developer_instructions: threadDeveloperInstructions,
        inject_context: threadInjectContext,
      });
      await loadStatus();
    } catch (err) {
      errorMessage = `error: ${String(err)}`;
    } finally {
      threadBusy = false;
    }
  }

  async function startTurn(): Promise<void> {
    if (!turnPrompt.trim()) {
      errorMessage = "error: prompt is required";
      return;
    }
    turnBusy = true;
    errorMessage = "";
    try {
      const response = await postJSON<CodexTurnStartResponse>("/api/codex/turn/start", {
        thread_id: status?.thread?.id || "",
        prompt: turnPrompt,
        approval_policy: assistantApprovalPolicy,
        sandbox_type: assistantSandboxType,
        inject_context: turnInjectContext,
      });
      if (response.turn_id) {
        turnPrompt = "";
      }
      await loadStatus();
    } catch (err) {
      errorMessage = `error: ${String(err)}`;
    } finally {
      turnBusy = false;
    }
  }

  function latestThreadID(eventList: CodexEvent[]): string {
    for (let i = eventList.length - 1; i >= 0; i -= 1) {
      const threadID = eventThreadID(eventList[i]);
      if (threadID) {
        return threadID;
      }
    }
    return "";
  }

  function eventThreadID(event: CodexEvent): string {
    const payload = event.payload as { params?: { threadId?: string } } | undefined;
    return event.thread_id || payload?.params?.threadId || "";
  }

  function eventTurnID(event: CodexEvent): string {
    const payload = event.payload as {
      params?: {
        turnId?: string;
        turn?: { id?: string };
      };
    } | undefined;
    return event.turn_id || payload?.params?.turnId || payload?.params?.turn?.id || "";
  }

  function eventItem(event: CodexEvent): { type?: string; text?: string; content?: Array<{ text?: string }> } | null {
    const payload = event.payload as {
      params?: {
        item?: {
          type?: string;
          text?: string;
          content?: Array<{ text?: string }>;
        };
      };
    } | undefined;
    return payload?.params?.item || null;
  }

  function eventItemText(item: {
    text?: string;
    content?: Array<{ text?: string }>;
  }): string {
    if (typeof item.text === "string" && item.text.trim() !== "") {
      return item.text;
    }
    if (!Array.isArray(item.content)) {
      return "";
    }
    return item.content
      .map((part) => part?.text || "")
      .filter((text) => text.trim() !== "")
      .join("\n\n");
  }

  onDestroy(() => {
    stop();
  });
</script>

<div class="assistant-shell">
  <div class="assistant-frame">
    <div class="assistant-header">
      <div class="assistant-copy">
        <div class="muted small">Local assistant</div>
        <div class="assistant-title-row">
          <h2>Assistant</h2>
          <span class={`badge ${codexStatusKind(status)}`}>{codexStatusText(status)}</span>
          <span class={`badge ${codexAuthKind(status)}`}>{codexAuthText(status)}</span>
        </div>
        <div class="assistant-subtitle mono">{workspaceLabel}</div>
        <div class="assistant-chat-label mono">{chatLabel}</div>
      </div>

      <div class="assistant-actions">
        <button type="button" on:click={startThread} disabled={!canCreateChat || threadBusy}>
          {threadBusy ? "Starting..." : status?.thread?.id ? "New chat" : "Start chat"}
        </button>
        {#if assistantState === "logged_out" && !status?.login?.pending}
          <button type="button" on:click={startDeviceCodeLogin} disabled={loginBusy}>
            {loginBusy ? "Starting..." : "Sign in"}
          </button>
        {/if}
      </div>
    </div>

    {#if status?.login?.pending}
      <div class="assistant-callout">
        <div>
          <div class="muted small">Device-code login</div>
          <div class="assistant-callout-title mono">{status.login.user_code || "-"}</div>
          <div class="muted small mono">{status.login.verification_url || "-"}</div>
        </div>
        <div class="assistant-callout-actions">
          {#if status?.login?.verification_url}
            <a href={status.login.verification_url} target="_blank" rel="noreferrer">Open verification page</a>
          {/if}
          <button
            type="button"
            on:click={cancelDeviceCodeLogin}
            disabled={cancelBusy || !status?.login?.pending}
          >
            {cancelBusy ? "Canceling..." : "Cancel login"}
          </button>
        </div>
      </div>
    {/if}

    {#if status?.last_error || errorMessage}
      <div class="assistant-alert mono">{status?.last_error || errorMessage}</div>
    {/if}

    <div class="assistant-transcript">
      {#if transcriptTurns.length === 0}
        <div class="assistant-empty">
          <div class="assistant-empty-title">
            {status?.thread?.id ? "No turns yet." : "Start a chat to talk to the assistant."}
          </div>
          <div class="muted">
            {#if status?.thread?.id}
              Use the prompt box below to send the first turn on this thread.
            {:else}
              The assistant uses the current tunnel-client workspace, model, and developer instructions.
            {/if}
          </div>
        </div>
      {:else}
        {#each transcriptTurns as turn}
          <div class="assistant-turn">
            {#if turn.userText}
              <article class="assistant-message user">
                <div class="assistant-message-label">You</div>
                <div class="assistant-message-body">{turn.userText}</div>
              </article>
            {/if}

            <article class="assistant-message assistant">
              <div class="assistant-message-label">Assistant</div>
              <div class="assistant-message-body">
                {turn.assistantText || (turn.status === "completed" ? "No assistant output." : "Thinking...")}
              </div>
            </article>
          </div>
        {/each}
      {/if}
    </div>

    <div class="assistant-composer">
      <label class="muted small" for="assistant-prompt">Prompt</label>
      <textarea
        id="assistant-prompt"
        bind:value={turnPrompt}
        rows="6"
        placeholder={status?.thread?.id ? "Ask the assistant..." : "Start a new chat first."}
        disabled={!status?.thread?.id || turnBusy}
      />
      <div class="assistant-composer-row">
        <label class="assistant-check">
          <input type="checkbox" bind:checked={turnInjectContext} />
          <span>Inject latest tunnel context</span>
        </label>

        <div class="assistant-composer-actions">
          {#if status?.turn?.status}
            <span class="muted small mono">{status.turn.status}</span>
          {/if}
          <button type="button" on:click={startTurn} disabled={!canSendTurn}>
            {turnBusy ? "Sending..." : "Send"}
          </button>
        </div>
      </div>
    </div>

    {#if commandOutput}
      <details class="assistant-drawer">
        <summary>Command output</summary>
        <pre class="pre mono assistant-pre">{commandOutput}</pre>
      </details>
    {/if}

    <details class="assistant-drawer">
      <summary>Session settings</summary>
      <div class="assistant-settings-grid">
        <div class="muted">CWD</div>
        <div><input bind:value={threadCwd} style="width: 100%" /></div>

        <div class="muted">Model</div>
        <div><input bind:value={threadModel} placeholder="default" style="width: 100%" /></div>

        <div class="muted">New chat context</div>
        <div>
          <label class="assistant-check">
            <input type="checkbox" bind:checked={threadInjectContext} />
            <span>Inject tunnel context when starting a new chat</span>
          </label>
        </div>

        <div class="muted">Developer instructions</div>
        <div>
          <textarea bind:value={threadDeveloperInstructions} rows="5" style="width: 100%" />
        </div>
      </div>

      <div class="assistant-drawer-actions">
        <button type="button" on:click={startThread} disabled={!canCreateChat || threadBusy}>
          {threadBusy ? "Starting..." : status?.thread?.id ? "Start fresh chat" : "Start chat"}
        </button>
      </div>
    </details>

    <details class="assistant-drawer">
      <summary>Diagnostics</summary>
      <div class="assistant-settings-grid">
        <div class="muted">Process</div>
        <div><span class={`badge ${codexStatusKind(status)}`}>{codexStatusText(status)}</span></div>

        <div class="muted">PID</div>
        <div class="mono">{status?.pid ?? "-"}</div>

        <div class="muted">Command</div>
        <div class="mono">{status?.command || "-"}</div>

        <div class="muted">Platform</div>
        <div class="mono">
          {status?.initialize_info?.platform_os || "-"} / {status?.initialize_info?.platform_family || "-"}
        </div>

        <div class="muted">Requires OpenAI auth</div>
        <div class="mono">{status?.requires_openai_auth ? "true" : "false"}</div>

        <div class="muted">Last error</div>
        <div class="mono">{status?.last_error || errorMessage || "-"}</div>
      </div>

      <div class="assistant-events">
        <div class="muted small">Recent significant bridge events</div>
        {#if diagnosticEvents.length === 0}
          <div class="muted small">No events yet.</div>
        {:else}
          {#each diagnosticEvents as event}
            <div class="assistant-event">
              <div class="assistant-event-time mono">{fmtTimestamp(event.time)}</div>
              <div class="assistant-event-copy">
                <div class="assistant-event-method mono">{event.method || "-"}</div>
                <div class="muted small mono">{event.summary || event.delta || "-"}</div>
              </div>
            </div>
          {/each}
        {/if}
      </div>
    </details>
  </div>
</div>

<style>
  h2 {
    margin: 0;
    font-size: 22px;
    line-height: 1.1;
  }

  textarea {
    width: 100%;
    min-height: 120px;
    padding: 12px 14px;
    border-radius: 16px;
    border: 1px solid var(--border);
    background: color-mix(in srgb, var(--bg) 70%, transparent);
    color: inherit;
    font: inherit;
    resize: vertical;
    box-sizing: border-box;
  }

  .assistant-shell {
    max-width: 960px;
    margin: 0 auto;
  }

  .assistant-frame {
    display: grid;
    gap: 14px;
    border: 1px solid var(--border);
    border-radius: 24px;
    padding: 16px;
    background:
      linear-gradient(180deg, rgba(127, 127, 127, 0.08), rgba(127, 127, 127, 0.03)),
      rgba(127, 127, 127, 0.04);
  }

  .assistant-header {
    display: flex;
    justify-content: space-between;
    align-items: flex-start;
    gap: 12px;
    flex-wrap: wrap;
  }

  .assistant-copy {
    display: grid;
    gap: 8px;
    min-width: min(560px, 100%);
  }

  .assistant-title-row {
    display: flex;
    align-items: center;
    gap: 8px;
    flex-wrap: wrap;
  }

  .assistant-subtitle {
    font-size: 12px;
  }

  .assistant-chat-label {
    font-size: 12px;
    color: var(--muted);
  }

  .assistant-actions {
    display: flex;
    gap: 10px;
    flex-wrap: wrap;
    justify-content: flex-end;
  }

  .assistant-callout {
    display: flex;
    justify-content: space-between;
    align-items: flex-start;
    gap: 12px;
    flex-wrap: wrap;
    border: 1px dashed var(--border);
    border-radius: 18px;
    padding: 12px 14px;
    background: color-mix(in srgb, var(--bg) 88%, transparent);
  }

  .assistant-callout-title {
    margin-top: 4px;
    font-size: 18px;
  }

  .assistant-callout-actions {
    display: flex;
    gap: 10px;
    align-items: center;
    flex-wrap: wrap;
  }

  .assistant-alert {
    padding: 10px 12px;
    border-radius: 14px;
    border: 1px solid rgba(221, 51, 51, 0.35);
    background: rgba(221, 51, 51, 0.08);
  }

  .assistant-transcript {
    display: grid;
    gap: 14px;
    min-height: 320px;
    padding: 16px;
    border: 1px solid var(--border);
    border-radius: 22px;
    background:
      linear-gradient(180deg, rgba(127, 127, 127, 0.03), rgba(127, 127, 127, 0.08)),
      color-mix(in srgb, var(--bg) 90%, transparent);
  }

  .assistant-empty {
    display: grid;
    gap: 6px;
    align-self: center;
    justify-items: start;
    padding: 6px 2px;
  }

  .assistant-empty-title {
    font-size: 18px;
    font-weight: 600;
  }

  .assistant-turn {
    display: grid;
    gap: 12px;
  }

  .assistant-message {
    display: grid;
    gap: 6px;
    max-width: min(720px, 100%);
    padding: 12px 14px;
    border-radius: 20px;
    border: 1px solid var(--border);
    background: color-mix(in srgb, var(--bg) 92%, transparent);
  }

  .assistant-message.user {
    justify-self: end;
    background: color-mix(in srgb, rgba(127, 127, 127, 0.16) 70%, transparent);
  }

  .assistant-message.assistant {
    justify-self: start;
  }

  .assistant-message-label {
    font-size: 11px;
    font-weight: 600;
    letter-spacing: 0.06em;
    text-transform: uppercase;
    color: var(--muted);
  }

  .assistant-message-body {
    white-space: pre-wrap;
    overflow-wrap: anywhere;
    line-height: 1.5;
  }

  .assistant-composer {
    display: grid;
    gap: 10px;
  }

  .assistant-composer-row {
    display: flex;
    justify-content: space-between;
    align-items: center;
    gap: 10px;
    flex-wrap: wrap;
  }

  .assistant-composer-actions {
    display: flex;
    align-items: center;
    gap: 10px;
    flex-wrap: wrap;
    margin-left: auto;
  }

  .assistant-check {
    display: inline-flex;
    align-items: center;
    gap: 8px;
    min-height: 24px;
  }

  .assistant-check input {
    min-width: 0;
    width: 16px;
    height: 16px;
    padding: 0;
    margin: 0;
  }

  .assistant-drawer {
    border: 1px solid var(--border);
    border-radius: 16px;
    padding: 12px 14px;
    background: color-mix(in srgb, var(--bg) 88%, transparent);
  }

  .assistant-drawer[open] {
    display: grid;
    gap: 12px;
  }

  .assistant-drawer summary {
    cursor: pointer;
    font-weight: 600;
  }

  .assistant-drawer-actions {
    display: flex;
    justify-content: flex-end;
    gap: 10px;
    flex-wrap: wrap;
  }

  .assistant-settings-grid {
    display: grid;
    grid-template-columns: 140px 1fr;
    gap: 10px 12px;
  }

  .assistant-events {
    display: grid;
    gap: 10px;
  }

  .assistant-event {
    display: grid;
    grid-template-columns: 140px 1fr;
    gap: 10px;
    padding-top: 10px;
    border-top: 1px solid var(--border);
  }

  .assistant-event:first-of-type {
    border-top: 0;
    padding-top: 0;
  }

  .assistant-event-time {
    font-size: 12px;
  }

  .assistant-event-copy {
    display: grid;
    gap: 4px;
  }

  .assistant-event-method {
    font-size: 12px;
  }

  .assistant-pre {
    margin: 0;
  }

  @media (max-width: 820px) {
    .assistant-settings-grid,
    .assistant-event {
      grid-template-columns: 1fr;
    }

    .assistant-copy {
      min-width: 0;
    }

    .assistant-composer-actions {
      margin-left: 0;
    }
  }
</style>
