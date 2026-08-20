import type { BadgeKind, CodexStatusResponse } from "./types";

export function deriveCodexState(status: CodexStatusResponse | null): string {
  if (status?.state) return status.state;
  const lastError = (status?.last_error || "").toLowerCase();
  if (lastError.includes("command not found: codex") || lastError.includes("executable file not found")) {
    return "codex_missing";
  }
  if (lastError.includes("app-server") && lastError.includes("unknown command")) {
    return "app_server_unsupported";
  }
  if (status?.account?.type === "chatgpt") return "ready";
  if (status?.requires_openai_auth) return "logged_out";
  if (status?.ready) return "ready";
  if (status?.starting) return "starting";
  if (lastError) return "error";
  return "not_ready";
}

export function codexStatusKind(status: CodexStatusResponse | null): BadgeKind {
  switch (deriveCodexState(status)) {
    case "ready":
      return "ok";
    case "logged_out":
    case "starting":
    case "not_ready":
      return "warn";
    default:
      return "bad";
  }
}

export function codexStatusText(status: CodexStatusResponse | null): string {
  switch (deriveCodexState(status)) {
    case "ready":
      return "ready";
    case "logged_out":
      return "logged out";
    case "starting":
      return "starting";
    case "codex_missing":
      return "codex missing";
    case "app_server_unsupported":
      return "app-server unsupported";
    case "error":
      return "error";
    default:
      return "not ready";
  }
}

export function codexAuthKind(status: CodexStatusResponse | null): BadgeKind {
  if (status?.account?.type === "chatgpt") return "ok";
  if (status?.login?.pending) return "warn";
  switch (deriveCodexState(status)) {
    case "codex_missing":
    case "app_server_unsupported":
    case "error":
      return "bad";
    default:
      return "warn";
  }
}

export function codexAuthText(status: CodexStatusResponse | null): string {
  if (status?.account?.type === "chatgpt") {
    const plan = status.account.plan_type ? ` (${status.account.plan_type})` : "";
    return `${status.account.email || "chatgpt"}${plan}`;
  }
  if (status?.login?.pending) return "device code pending";
  switch (deriveCodexState(status)) {
    case "codex_missing":
      return "install codex";
    case "app_server_unsupported":
      return "upgrade codex";
    case "logged_out":
      return "logged out";
    case "error":
      return status?.last_error || "bridge error";
    default:
      return status?.auth_method || "logged out";
  }
}
