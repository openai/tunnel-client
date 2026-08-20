import { vi } from "vitest";

export function jsonResponse(body: unknown, status = 200): Response {
  return new Response(JSON.stringify(body), {
    status,
    headers: { "content-type": "application/json" },
  });
}

export function textResponse(body: string, status = 200): Response {
  return new Response(body, { status, headers: { "content-type": "text/plain" } });
}

function requestURL(input: RequestInfo | URL): string {
  if (typeof input === "string") return input;
  if (input instanceof URL) return input.toString();
  if (typeof input === "object" && input !== null && "url" in input) {
    const maybeRequest = input as { url?: unknown };
    if (typeof maybeRequest.url === "string") {
      return maybeRequest.url;
    }
  }
  return String(input);
}

export interface MockFetchRequest {
  input: RequestInfo | URL;
  init?: RequestInit;
  url: string;
}

export function mockFetchRequest(
  handler: (request: MockFetchRequest) => Response | Promise<Response>,
) {
  const impl = async (input: RequestInfo | URL, init?: RequestInit): Promise<Response> => {
    const url = requestURL(input);
    return await handler({ input, init, url });
  };

  const globalSpy = vi.spyOn(globalThis, "fetch").mockImplementation(impl as typeof fetch);

  if (typeof window !== "undefined" && "fetch" in window) {
    vi.spyOn(window, "fetch").mockImplementation(impl as typeof window.fetch);
  }

  return globalSpy;
}

export function mockFetch(handler: (url: string) => Response | Promise<Response>) {
  return mockFetchRequest(({ url }) => handler(url));
}
