import type { TextResponse } from "./api";
import type { BadgeKind } from "./types";

export interface BadgeState {
  kind: BadgeKind;
  text: string;
}

export function healthBadgeFromResponse(response: TextResponse): BadgeState {
  const text = response.text.trim();
  if (response.ok) {
    return { kind: "ok", text: `Health: ${text || "ok"}` };
  }
  return { kind: "bad", text: `Health: ${text || `HTTP ${response.status}`}` };
}

export function readyBadgeFromResponse(response: TextResponse): BadgeState {
  const text = response.text.trim();
  if (response.ok) {
    return { kind: "ok", text: `Ready: ${text || "ok"}` };
  }
  return {
    kind: text.toLowerCase().includes("pending") ? "warn" : "bad",
    text: `Ready: ${text || `HTTP ${response.status}`}`,
  };
}
