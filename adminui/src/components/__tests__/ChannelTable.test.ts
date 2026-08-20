import { fireEvent, render } from "@testing-library/svelte";

import ChannelTable from "../ChannelTable.svelte";

describe("ChannelTable", () => {
  it("renders direct/proxy route status and proxy details", async () => {
    const { findAllByText, findByText } = render(ChannelTable, {
      channels: [
        {
          name: "main",
          enabled: true,
          server_kind: "external",
          transport_kind: "http-streamable",
          details: [{ key: "address", value: "https://mcp.example/main" }],
        },
        {
          name: "tools",
          enabled: true,
          server_kind: "external",
          transport_kind: "http-streamable",
          details: [{ key: "address", value: "https://mcp.example/tools" }],
        },
      ],
      mcpRoutes: [
        {
          name: "main",
          route_mode: "proxy",
          proxy_id: "proxy-main",
          proxy_url: "http://proxy.example:8080",
          proxy_source: "env:HTTPS_PROXY",
          target: "mcp.example:443",
        },
        {
          name: "tools",
          route_mode: "direct",
          proxy_source: "none",
          target: "mcp.example:443",
        },
      ],
    });

    expect((await findAllByText("proxy-main")).length).toBeGreaterThan(0);
    expect((await findAllByText("direct")).length).toBeGreaterThan(0);

    const expandButtons = await findAllByText("+ Details");
    await fireEvent.click(expandButtons[0]);

    expect((await findAllByText("proxy_mode")).length).toBeGreaterThan(0);
    expect((await findAllByText("proxy_url")).length).toBeGreaterThan(0);
    expect((await findAllByText("http://proxy.example:8080")).length).toBeGreaterThan(0);
  });
});
