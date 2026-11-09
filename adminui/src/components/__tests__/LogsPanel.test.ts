import { fireEvent, render, waitFor } from "@testing-library/svelte";
import { tick } from "svelte";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";

import LogsPanel from "../LogsPanel.svelte";
import { setAdminUIRoot } from "../../test/adminRoot";
import { jsonResponse, mockFetchRequest } from "../../test/mockFetch";

class MockEventSource {
  onopen: ((event: Event) => void) | null = null;
  onerror: ((event: Event) => void) | null = null;

  constructor(_url: string) {
    queueMicrotask(() => {
      this.onopen?.(new Event("open"));
    });
  }

  addEventListener(_type: string, _listener: EventListenerOrEventListenerObject): void {}

  close(): void {}
}

function observeInitialConnection() {
  let resolveConnected!: () => void;
  const connected = new Promise<void>((resolve) => {
    resolveConnected = resolve;
  });
  return {
    connected,
    onConnectionChange: (value: boolean) => {
      if (value) resolveConnected();
    },
  };
}

describe("LogsPanel", () => {
  beforeEach(() => {
    vi.stubGlobal("EventSource", MockEventSource);
  });

  afterEach(() => {
    vi.unstubAllGlobals();
    vi.restoreAllMocks();
  });

  it("updates the runtime log level through the admin API", async () => {
    let currentLevel = "warn";
    let lastPutBody = "";

    mockFetchRequest(async ({ url, init }) => {
      if (url.includes("/api/logs?limit=500")) {
        return jsonResponse({ events: [] });
      }
      if (url.endsWith("/api/log-level")) {
        const method = (init?.method || "GET").toUpperCase();
        if (method === "PUT") {
          lastPutBody = String(init?.body || "");
          currentLevel = JSON.parse(lastPutBody).level;
        }
        return jsonResponse({
          level: currentLevel,
          supported_levels: ["debug", "info", "warn"],
        });
      }
      if (url.endsWith("/api/status")) {
        return jsonResponse({ raw_http_logging_enabled: false });
      }
      return new Response("not found", { status: 404, statusText: "Not Found" });
    });

    const navigateTo = vi.fn();
    const confirmLogExport = vi.fn(() => true);
    const initialConnection = observeInitialConnection();
    const { getByRole, findByText } = render(LogsPanel, {
      onConnectionChange: initialConnection.onConnectionChange,
      navigateTo,
      confirmLogExport,
    });

    // The stream connects after the initial requests have settled.
    await initialConnection.connected;
    await tick();
    const runtimeLevelSelect = getByRole("combobox", { name: "Runtime log level" }) as HTMLSelectElement;
    expect(runtimeLevelSelect.value).toBe("warn");
    expect(runtimeLevelSelect.disabled).toBe(false);

    await fireEvent.change(runtimeLevelSelect, { target: { value: "debug" } });
    await fireEvent.click(getByRole("button", { name: "Apply runtime level" }));

    await findByText("runtime log level: debug");
    expect(lastPutBody).toBe(JSON.stringify({ level: "debug" }));
    expect(navigateTo).not.toHaveBeenCalled();
    expect(confirmLogExport).not.toHaveBeenCalled();
  });

  it.each([
    { documentPath: "/", appRoot: "./", expectedRoot: "/" },
    { documentPath: "/gateway/client/", appRoot: "./", expectedRoot: "/gateway/client/" },
    { documentPath: "/gateway/client/ui/", appRoot: "../", expectedRoot: "/gateway/client/" },
  ])("uses the application root for log exports at $documentPath", async ({
    documentPath,
    appRoot,
    expectedRoot,
  }) => {
    setAdminUIRoot(`${documentPath}?view=compact#logs`, appRoot);
    mockFetchRequest(async ({ url }) => {
      if (url.endsWith("/api/logs?limit=500")) return jsonResponse({ events: [] });
      if (url.endsWith("/api/log-level")) return jsonResponse({ level: "info" });
      if (url.endsWith("/api/status")) return jsonResponse({ raw_http_logging_enabled: false });
      return new Response("not found", { status: 404 });
    });

    const navigateTo = vi.fn();
    const { findByText, getByRole } = render(LogsPanel, {
      onConnectionChange: () => {},
      navigateTo,
    });
    const exportPath = `${expectedRoot}api/logs/export?minutes=30`;
    const exportURL = `${window.location.origin}${exportPath}`;

    expect(await findByText(`curl -fsSJO "${exportURL}"`)).toBeTruthy();
    await fireEvent.click(getByRole("button", { name: "Download recent logs" }));
    await waitFor(() => expect(navigateTo).toHaveBeenCalledWith(exportPath));
  });

  it("warns before downloading logs when debug + raw unsafe logging are enabled", async () => {
    let currentLevel = "warn";
    let statusRequestCount = 0;

    mockFetchRequest(async ({ url, init }) => {
      if (url.includes("/api/logs?limit=500")) {
        return jsonResponse({ events: [] });
      }
      if (url.endsWith("/api/log-level")) {
        const method = (init?.method || "GET").toUpperCase();
        if (method === "PUT") {
          currentLevel = JSON.parse(String(init?.body || "")).level;
        }
        return jsonResponse({
          level: currentLevel,
          supported_levels: ["debug", "info", "warn"],
        });
      }
      if (url.endsWith("/api/status")) {
        statusRequestCount += 1;
        return jsonResponse({ raw_http_logging_enabled: true });
      }
      return new Response("not found", { status: 404, statusText: "Not Found" });
    });

    const confirmSpy = vi.fn(() => false);
    const navigateTo = vi.fn();
    const initialConnection = observeInitialConnection();

    const { getByRole, findByText } = render(LogsPanel, {
      onConnectionChange: initialConnection.onConnectionChange,
      navigateTo,
      confirmLogExport: confirmSpy,
    });

    await initialConnection.connected;
    await tick();
    const runtimeLevelSelect = getByRole("combobox", { name: "Runtime log level" }) as HTMLSelectElement;
    expect(runtimeLevelSelect.value).toBe("warn");
    expect(runtimeLevelSelect.disabled).toBe(false);

    await fireEvent.change(runtimeLevelSelect, { target: { value: "debug" } });
    await fireEvent.click(getByRole("button", { name: "Apply runtime level" }));
    await findByText("runtime log level: debug");

    await fireEvent.click(getByRole("button", { name: "Download recent logs" }));

    expect(statusRequestCount).toBeGreaterThan(0);
    await waitFor(() => {
      expect(confirmSpy).toHaveBeenCalledWith(
        "Logs may include sensitive data because debug logging and --log.http-raw-unsafe are enabled. Download logs anyway?",
      );
    });
    expect(navigateTo).not.toHaveBeenCalled();
  });
});
