import { render, waitFor } from "@testing-library/svelte";

import CodexPanel from "../CodexPanel.svelte";
import { jsonResponse, mockFetchRequest } from "../../test/mockFetch";

describe("CodexPanel", () => {
  it("renders bridge status, login state, and current thread", async () => {
    mockFetchRequest(async ({ url }) => {
      if (url.includes("/api/codex/status")) {
        return jsonResponse({
          ready: true,
          command: "codex",
          command_cwd: "/workspace/openai",
          auth_method: "chatgpt",
          account: {
            type: "chatgpt",
            email: "worker@example.com",
            plan_type: "business",
          },
          login: {
            pending: true,
            user_code: "ABCD-EFGH",
            verification_url: "https://auth.openai.com/codex/device",
          },
          thread: {
            id: "thread_123",
            cwd: "/workspace/openai",
          },
          turn: {
            id: "turn_456",
            status: "in_progress",
          },
          initialize_info: {
            platform_os: "linux",
            platform_family: "unix",
          },
        });
      }
      if (url.includes("/api/codex/events")) {
        return jsonResponse({
          events: [
            {
              seq: 1,
              time: "2026-04-22T00:00:00Z",
              method: "item/agentMessage/delta",
              delta: "hello",
              summary: "assistant delta",
            },
          ],
        });
      }
      return new Response("not found", { status: 404 });
    });

    const { getAllByText, getByText } = render(CodexPanel, { props: { active: true } });

    await waitFor(() => {
      expect(getByText("ABCD-EFGH")).toBeTruthy();
      expect(getAllByText("thread_123").length).toBeGreaterThan(0);
      expect(getByText("hello")).toBeTruthy();
    });
  });
});
