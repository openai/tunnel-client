import { render } from "@testing-library/svelte";

import OverviewPanel from "../OverviewPanel.svelte";
import { jsonResponse, mockFetch } from "../../test/mockFetch";

describe("OverviewPanel", () => {
  it("renders control-plane proxy summary", async () => {
    mockFetch(async (url) => {
      if (url.includes("/api/status")) {
        return jsonResponse({
          version: "test",
          client_instance_id: "instance-test",
          channels: [],
          control_plane_route: {
            kind: "control_plane",
            name: "control-plane",
            route_mode: "proxy",
            proxy_id: "proxy-cp",
            proxy_url: "http://proxy.control:8080",
            proxy_source: "env:HTTPS_PROXY",
          },
          mcp_routes: [],
        });
      }
      return new Response("not found", { status: 404, statusText: "Not Found" });
    });

    const { findByText } = render(OverviewPanel, { active: true });

    expect(await findByText("Instance ID")).toBeTruthy();
    expect(await findByText("instance-test")).toBeTruthy();
    expect(await findByText("Proxy mode")).toBeTruthy();
    expect(await findByText("proxy-cp")).toBeTruthy();
    expect(await findByText("http://proxy.control:8080")).toBeTruthy();
    expect(await findByText("env:HTTPS_PROXY")).toBeTruthy();
  });
});
