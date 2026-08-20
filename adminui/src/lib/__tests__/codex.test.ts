import { codexAuthText, codexStatusText, deriveCodexState } from "../codex";

describe("codex helpers", () => {
  it("surfaces codex missing from explicit state", () => {
    const status = {
      state: "codex_missing",
      last_error: "zsh:1: command not found: codex",
    };

    expect(deriveCodexState(status)).toBe("codex_missing");
    expect(codexStatusText(status)).toBe("codex missing");
    expect(codexAuthText(status)).toBe("install codex");
  });

  it("falls back to last_error when older status payloads omit state", () => {
    const status = {
      ready: false,
      last_error: "zsh:1: command not found: codex",
    };

    expect(deriveCodexState(status)).toBe("codex_missing");
    expect(codexStatusText(status)).toBe("codex missing");
  });

  it("reports logged-out state when Codex requires auth", () => {
    const status = {
      ready: true,
      requires_openai_auth: true,
    };

    expect(deriveCodexState(status)).toBe("logged_out");
    expect(codexStatusText(status)).toBe("logged out");
    expect(codexAuthText(status)).toBe("logged out");
  });
});
