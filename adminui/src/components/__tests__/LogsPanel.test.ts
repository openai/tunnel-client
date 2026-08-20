import { fireEvent, render, waitFor } from "@testing-library/svelte";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";

import LogsPanel from "../LogsPanel.svelte";
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

describe("LogsPanel", () => {
  beforeEach(() => {
    vi.stubGlobal("EventSource", MockEventSource);
  });

  afterEach(() => {
    vi.unstubAllGlobals();
    vi.restoreAllMocks();
  });

  it("updates the runtime log level through the admin API", async () => {
    let currentLevel = "info";
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
    const { getByRole, findByText } = render(LogsPanel, {
      onConnectionChange: () => {},
      navigateTo,
      confirmLogExport,
    });

    const runtimeLevelSelect = getByRole("combobox", { name: "Runtime log level" }) as HTMLSelectElement;
    await waitFor(() => {
      expect(runtimeLevelSelect.value).toBe("info");
    });

    await fireEvent.change(runtimeLevelSelect, { target: { value: "debug" } });
    await fireEvent.click(getByRole("button", { name: "Apply runtime level" }));

    await findByText("runtime log level: debug");
    expect(lastPutBody).toBe(JSON.stringify({ level: "debug" }));
    expect(navigateTo).not.toHaveBeenCalled();
    expect(confirmLogExport).not.toHaveBeenCalled();
  });

  it("warns before downloading logs when debug + raw unsafe logging are enabled", async () => {
    let currentLevel = "info";
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

    const { getByRole, findByText } = render(LogsPanel, {
      onConnectionChange: () => {},
      navigateTo,
      confirmLogExport: confirmSpy,
    });

    const runtimeLevelSelect = getByRole("combobox", { name: "Runtime log level" }) as HTMLSelectElement;
    await waitFor(() => {
      expect(runtimeLevelSelect.value).toBe("info");
    });

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
