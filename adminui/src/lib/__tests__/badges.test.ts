import { describe, expect, it } from "vitest";

import { healthBadgeFromResponse, readyBadgeFromResponse } from "../badges";

describe("badge helpers", () => {
  it("keeps readiness pending responses visible as warnings", () => {
    expect(
      readyBadgeFromResponse({
        ok: false,
        status: 503,
        text: "mcp startup probe pending",
      }),
    ).toEqual({
      kind: "warn",
      text: "Ready: mcp startup probe pending",
    });
  });

  it("keeps readiness failures visible as errors", () => {
    expect(
      readyBadgeFromResponse({
        ok: false,
        status: 503,
        text: "mcp probe failed: boom",
      }),
    ).toEqual({
      kind: "bad",
      text: "Ready: mcp probe failed: boom",
    });
  });

  it("keeps health failures visible as errors", () => {
    expect(
      healthBadgeFromResponse({
        ok: false,
        status: 503,
        text: "listener not initialized",
      }),
    ).toEqual({
      kind: "bad",
      text: "Health: listener not initialized",
    });
  });
});
