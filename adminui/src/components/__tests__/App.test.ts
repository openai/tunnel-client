import { fireEvent, render, waitFor } from "@testing-library/svelte";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";

import App from "../../App.svelte";
import { jsonResponse, mockFetchRequest, textResponse, type MockFetchRequest } from "../../test/mockFetch";

class ConnectingEventSource {
  onopen: ((event: Event) => void) | null = null;
  onerror: ((event: Event) => void) | null = null;

  constructor(public readonly url: string) {
    queueMicrotask(() => {
      this.onopen?.(new Event("open"));
    });
  }

  addEventListener(_type: string, _listener: EventListenerOrEventListenerObject): void {}

  removeEventListener(_type: string, _listener: EventListenerOrEventListenerObject): void {}

  close(): void {}
}

function statusFixture() {
  return {
    version: "v1.2.3-test",
    uptime_seconds: 7322,
    health_listen_addr: "127.0.0.1:8080",
    control_plane_base_url: "https://api.openai.com",
    control_plane_tunnel_id: "tun_adminui_test",
    control_plane_poll_timeout: "30s",
    control_plane_max_inflight: 8,
    control_plane_route: {
      route_mode: "proxy",
      proxy_id: "proxy-control",
      proxy_url: "https://proxy.example.test",
      proxy_source: "config",
    },
    tunnel_metadata: {
      name: "Customer MCP",
      description: "local connector",
    },
    warnings: ["MCP server has no OAuth metadata"],
    channels: [
      {
        name: "mcp",
        enabled: true,
        server_kind: "remote",
        transport_kind: "http",
      },
    ],
    mcp_routes: [
      {
        name: "primary",
        target: "mcp",
        route_mode: "proxy",
        proxy_id: "proxy-mcp",
        proxy_url: "https://mcp-proxy.example.test",
        proxy_source: "config",
      },
    ],
    raw_http_logging_enabled: false,
  };
}

function systemFixture() {
  return {
    tls: {
      system_trust: {
        enabled: true,
        source: "system cert pool",
        source_paths: ["/etc/ssl/certs"],
      },
      extra_bundle: {
        path: "/etc/tunnel/ca.pem",
        cert_count: 1,
        parse_errors: 0,
        certificates: [
          {
            name: "Partner Root",
            subject_cn: "Partner Root CA",
            issuer_cn: "Partner Root CA",
            not_after: "2030-01-02T03:04:05Z",
            parse_status: "ok",
            cert_id: "cert_partner_root",
          },
        ],
      },
    },
    proxy_identity_map: [
      {
        proxy_id: "proxy-mcp",
        proxy_url: "https://mcp-proxy.example.test",
        proxy_source: "config",
      },
    ],
    proxy_health: [
      {
        route: {
          kind: "mcp",
          name: "primary",
          target: "mcp",
          route_mode: "proxy",
          proxy_id: "proxy-mcp",
          proxy_url: "https://mcp-proxy.example.test",
          proxy_source: "config",
        },
        health_state: "healthy",
        last_check: "2026-06-12T00:00:00Z",
        last_success: "2026-06-12T00:00:00Z",
        history: [
          {
            timestamp: "2026-06-12T00:00:00Z",
            success: true,
            tcp_duration_ms: 12,
            connect_duration_ms: 23,
            http_status_category: "2xx",
          },
        ],
      },
    ],
  };
}

function codexStatusFixture() {
  return {
    ready: true,
    command: "codex",
    command_cwd: "/workspace/tunnel-client",
    auth_method: "chatgpt",
    account: {
      type: "chatgpt",
      email: "operator@example.com",
      plan_type: "business",
    },
    thread: {
      id: "thread_adminui",
      cwd: "/workspace/tunnel-client",
    },
  };
}

function installAppFetchMock({
  healthStatus = 200,
  healthText = "ok",
  readyStatus = 200,
  readyText = "ok",
  onRequest,
}: {
  healthStatus?: number;
  healthText?: string;
  readyStatus?: number;
  readyText?: string;
  onRequest?: (request: MockFetchRequest) => Response | Promise<Response> | undefined;
} = {}) {
  return mockFetchRequest(async (request) => {
    const override = await onRequest?.(request);
    if (override) {
      return override;
    }

    const { url, init } = request;
    if (url.endsWith("/healthz")) {
      return textResponse(healthText, healthStatus);
    }
    if (url.endsWith("/readyz")) {
      return textResponse(readyText, readyStatus);
    }
    if (url.endsWith("/metrics")) {
      return textResponse("tunnel_client_commands_poll_cycles_total 7\n");
    }
    if (url.includes("/api/status")) {
      return jsonResponse(statusFixture());
    }
    if (url.includes("/api/oauth")) {
      return jsonResponse({
        discovery_urls: ["https://mcp.example.test/.well-known/oauth-protected-resource"],
        metadata: {
          attempts: [
            {
              source: "resource",
              url: "https://mcp.example.test/.well-known/oauth-protected-resource",
              tried: true,
              selected: true,
              status_code: 200,
              body: { resource: "https://mcp.example.test" },
            },
          ],
        },
      });
    }
    if (url.includes("/api/harpoon/status")) {
      return jsonResponse({
        enabled: true,
        capture_payloads: true,
        allow_plaintext_http: false,
        max_response_bytes: 65536,
        max_redirects: 3,
        proxy_routes: [
          {
            name: "primary",
            target: "mcp",
            route_mode: "proxy",
            proxy_id: "proxy-mcp",
            proxy_url: "https://mcp-proxy.example.test",
            proxy_source: "config",
          },
        ],
      });
    }
    if (url.includes("/api/harpoon/targets")) {
      return jsonResponse({
        targets: [
          {
            label: "docs",
            description: "Documentation backend",
            url: "https://docs.example.test",
            tags: ["read-only"],
          },
        ],
      });
    }
    if (url.includes("/api/harpoon/calls")) {
      return jsonResponse({
        calls: [
          {
            timestamp: "2026-06-12T00:00:00Z",
            label: "docs",
            method: "GET",
            url: "https://docs.example.test/search",
            status: 200,
            latency_ms: 42,
            req_bytes: 12,
            resp_bytes: 34,
            response_body: "{\"ok\":true}",
          },
        ],
      });
    }
    if (url.includes("/api/system")) {
      return jsonResponse(systemFixture());
    }
    if (url.includes("/api/logs?limit=500")) {
      return jsonResponse({
        events: [
          {
            time: "2026-06-12T00:00:00Z",
            level: "info",
            message: "admin ui ready",
            attrs: { component: "adminui" },
          },
        ],
      });
    }
    if (url.endsWith("/api/log-level")) {
      if ((init?.method || "GET").toUpperCase() === "PUT") {
        return jsonResponse({
          level: JSON.parse(String(init?.body || "{}")).level || "info",
          supported_levels: ["debug", "info", "warn"],
        });
      }
      return jsonResponse({ level: "info", supported_levels: ["debug", "info", "warn"] });
    }
    if (url.includes("/api/codex/status")) {
      return jsonResponse(codexStatusFixture());
    }
    if (url.includes("/api/codex/events")) {
      return jsonResponse({
        events: [
          {
            seq: 1,
            time: "2026-06-12T00:00:00Z",
            method: "item/started",
            thread_id: "thread_adminui",
            turn_id: "turn_1",
            payload: {
              params: {
                item: {
                  type: "userMessage",
                  text: "Summarize the tunnel status",
                },
              },
            },
          },
          {
            seq: 2,
            time: "2026-06-12T00:00:01Z",
            method: "item/agentMessage/delta",
            thread_id: "thread_adminui",
            turn_id: "turn_1",
            delta: "Tunnel is connected.",
          },
        ],
      });
    }
    return new Response("not found", { status: 404, statusText: "Not Found" });
  });
}

describe("App", () => {
  beforeEach(() => {
    vi.stubGlobal("EventSource", ConnectingEventSource);
    window.history.replaceState(null, "", "/");
  });

  afterEach(() => {
    window.history.replaceState(null, "", "/");
    vi.unstubAllGlobals();
  });

  it("renders overview data from local admin endpoints", async () => {
    installAppFetchMock();

    const { getAllByText } = render(App);

    await waitFor(() => {
      expect(getAllByText("v1.2.3-test").length).toBeGreaterThan(0);
      expect(getAllByText("tun_adminui_test").length).toBeGreaterThan(0);
      expect(getAllByText("Customer MCP").length).toBeGreaterThan(0);
    });

    expect(getAllByText("proxy").length).toBeGreaterThan(0);
    expect(getAllByText(/MCP server has no OAuth metadata/).length).toBeGreaterThan(0);
  });

  it("registers and switches to the System tab", async () => {
    installAppFetchMock();

    const { container, getAllByText, getByRole } = render(App);

    const systemTab = getByRole("tab", { name: "System" });
    expect(systemTab).toBeTruthy();

    await fireEvent.click(systemTab);

    await waitFor(() => {
      const panel = container.querySelector("#panel-system");
      expect(panel?.getAttribute("aria-hidden")).toBe("false");
    });

    const overviewPanel = container.querySelector("#panel-overview");
    expect(overviewPanel?.getAttribute("aria-hidden")).toBe("true");

    await waitFor(() => {
      expect(getAllByText("Partner Root CA").length).toBeGreaterThan(0);
      expect(getAllByText("reachable").length).toBeGreaterThan(0);
    });
  });

  it("posts an Assistant turn through the rendered shell", async () => {
    let lastTurnBody = "";

    installAppFetchMock({
      onRequest: ({ url, init }) => {
        if (url.includes("/api/codex/turn/start")) {
          lastTurnBody = String(init?.body || "");
          return jsonResponse({
            turn_id: "turn_2",
            thread_id: "thread_adminui",
            status: "queued",
          });
        }
        return undefined;
      },
    });

    const { container, getAllByText, getByLabelText, getByRole, getByText } = render(App);

    await fireEvent.click(getByRole("tab", { name: "Assistant" }));

    await waitFor(() => {
      expect(container.querySelector("#panel-codex")?.getAttribute("aria-hidden")).toBe("false");
      expect(getAllByText("thread_adminui").length).toBeGreaterThan(0);
      expect(getByText("Tunnel is connected.")).toBeTruthy();
    });

    await fireEvent.input(getByLabelText("Prompt"), {
      target: { value: "Check tunnel health" },
    });
    await fireEvent.click(getByRole("button", { name: "Send" }));

    await waitFor(() => {
      expect(lastTurnBody).not.toBe("");
    });
    expect(JSON.parse(lastTurnBody)).toEqual({
      thread_id: "thread_adminui",
      prompt: "Check tunnel health",
      approval_policy: "never",
      sandbox_type: "workspace-write",
      inject_context: true,
    });
  });

  it("starts an Assistant chat with a managed-compatible sandbox", async () => {
    let lastThreadBody = "";

    installAppFetchMock({
      onRequest: ({ url, init }) => {
        if (url.includes("/api/codex/thread/start")) {
          lastThreadBody = String(init?.body || "");
          return jsonResponse({
            thread_id: "thread_new",
            cwd: "/workspace/tunnel-client",
            approval_policy: "never",
            sandbox: "workspace-write",
          });
        }
        return undefined;
      },
    });

    const { getByRole } = render(App);

    await fireEvent.click(getByRole("tab", { name: "Assistant" }));
    const startButton = await waitFor(() => {
      const button = getByRole("button", { name: "New chat" }) as HTMLButtonElement;
      expect(button.disabled).toBe(false);
      return button;
    });
    await fireEvent.click(startButton);

    await waitFor(() => {
      expect(lastThreadBody).not.toBe("");
    });
    expect(JSON.parse(lastThreadBody)).toEqual({
      cwd: "/workspace/tunnel-client",
      model: "",
      approval_policy: "never",
      sandbox_type: "workspace-write",
      developer_instructions: "",
      inject_context: true,
    });
  });
});
