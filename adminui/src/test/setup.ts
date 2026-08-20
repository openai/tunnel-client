import { cleanup } from "@testing-library/svelte";
import { afterEach, vi } from "vitest";

class MockEventSource {
  url: string;
  onopen: ((event?: unknown) => void) | null = null;
  onerror: ((event?: unknown) => void) | null = null;

  constructor(url: string | URL) {
    this.url = String(url);
  }

  addEventListener(_type: string, _listener: EventListenerOrEventListenerObject): void {}

  removeEventListener(_type: string, _listener: EventListenerOrEventListenerObject): void {}

  close(): void {}
}

globalThis.EventSource = MockEventSource as unknown as typeof EventSource;

afterEach(() => {
  cleanup();
  vi.restoreAllMocks();
  vi.useRealTimers();
});
