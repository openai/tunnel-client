import { fireEvent, render } from "@testing-library/svelte";

import SystemPanel from "../SystemPanel.svelte";
import { jsonResponse, mockFetch } from "../../test/mockFetch";

describe("SystemPanel", () => {
  it("renders trust data, proxy identity map, and expandable route history", async () => {
    mockFetch(async (url) => {
      if (url.includes("/api/system")) {
        return jsonResponse({
          tls: {
            system_trust: {
              enabled: true,
              source: "system cert pool",
              source_paths: ["/etc/ssl/certs"],
              fallback_note: "source paths unavailable on this platform",
            },
            extra_bundle: {
              path: "/etc/tunnel/ca.pem",
              cert_count: 1,
              parse_errors: 0,
              certificates: [
                {
                  cert_id: "cert-1",
                  name: "Corp Root",
                  subject_cn: "Corp Root",
                  issuer_cn: "Corp Root",
                  not_after: "2030-01-01T00:00:00Z",
                  parse_status: "ok",
                },
              ],
            },
          },
          proxy_identity_map: [
            {
              proxy_id: "proxy-a",
              proxy_url: "http://proxy.example:8080",
              proxy_source: "env:HTTPS_PROXY",
            },
          ],
          proxy_health: [
            {
              route: {
                kind: "control_plane",
                name: "control-plane",
                target: "api.openai.com:443",
                route_mode: "proxy",
                proxy_source: "env:HTTPS_PROXY",
                proxy_url: "http://proxy.example:8080",
                proxy_id: "proxy-a",
              },
              health_state: "healthy",
              last_check: "2026-02-06T12:00:00Z",
              last_success: "2026-02-06T11:59:50Z",
              history: [
                {
                  timestamp: "2026-02-06T11:59:20Z",
                  success: false,
                  tcp_duration_ms: 12,
                  connect_duration_ms: 20,
                  error_phase: "connect",
                  error_reason: "timeout",
                  http_status_category: "5xx",
                },
              ],
            },
          ],
        });
      }
      return new Response("not found", { status: 404, statusText: "Not Found" });
    });

    const { getByText, findAllByText, findByText } = render(SystemPanel, { active: true });

    await findByText("Proxy identity map");
    expect((await findAllByText("proxy-a")).length).toBeGreaterThan(0);
    expect(getByText("/etc/tunnel/ca.pem")).toBeTruthy();

    const expandButton = await findByText("+ History");
    await fireEvent.click(expandButton);

    expect(await findByText("− History")).toBeTruthy();
    expect((await findAllByText("timeout")).length).toBeGreaterThan(0);
    expect((await findAllByText("reachable")).length).toBeGreaterThan(0);
  });
});
