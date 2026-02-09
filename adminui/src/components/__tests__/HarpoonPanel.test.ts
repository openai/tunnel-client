import { render } from "@testing-library/svelte";

import HarpoonPanel from "../HarpoonPanel.svelte";
import { jsonResponse, mockFetch } from "../../test/mockFetch";

describe("HarpoonPanel", () => {
  it("renders proxy route summary with status", async () => {
    mockFetch(async (url) => {
      if (url.includes("/api/harpoon/status")) {
        return jsonResponse({
          enabled: true,
          capture_payloads: false,
          allow_plaintext_http: false,
          max_response_bytes: 1024,
          max_redirects: 3,
          proxy_routes: [
            {
              kind: "harpoon_target",
              name: "auth",
              route_mode: "proxy",
              proxy_id: "proxy-harpoon",
              proxy_url: "http://proxy.harpoon:8080",
              proxy_source: "flag:http-proxy",
            },
            {
              kind: "harpoon_target",
              name: "public",
              route_mode: "direct",
              proxy_source: "none",
            },
          ],
        });
      }
      if (url.includes("/api/harpoon/targets")) {
        return jsonResponse({
          targets: [
            {
              label: "auth",
              url: "https://auth.example",
              description: "Auth target",
              category: "oauth",
              source: "oauth",
              tags: ["auth-server-metadata", "issuer"],
            },
          ],
        });
      }
      if (url.includes("/api/harpoon/calls")) {
        return jsonResponse({ calls: [] });
      }
      return new Response("not found", { status: 404, statusText: "Not Found" });
    });

    const { container, findByText } = render(HarpoonPanel, { active: true });

    expect(await findByText(/Proxy routes: 2 \(1 proxy \/ 1 direct\)/)).toBeTruthy();
    expect(await findByText("Harpoon proxy routes")).toBeTruthy();
    expect(await findByText("proxy-harpoon")).toBeTruthy();
    expect(await findByText("http://proxy.harpoon:8080")).toBeTruthy();
    expect(await findByText("category/source: oauth")).toBeTruthy();
    expect(await findByText("tags: auth-server-metadata, issuer")).toBeTruthy();

    const proxyRoutesTable = container.querySelector("table.harpoon-proxy-routes-table");
    expect(proxyRoutesTable).toBeTruthy();
    expect(proxyRoutesTable?.closest(".harpoon-table-wrap")).toBeTruthy();

    const targetsTable = container.querySelector("table.harpoon-targets-table");
    expect(targetsTable).toBeTruthy();
  });
});
