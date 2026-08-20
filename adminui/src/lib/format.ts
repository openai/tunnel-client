export function fmtUptime(seconds: number): string {
  let remaining = Math.max(0, Math.floor(seconds || 0));
  const d = Math.floor(remaining / 86400);
  remaining -= d * 86400;
  const h = Math.floor(remaining / 3600);
  remaining -= h * 3600;
  const m = Math.floor(remaining / 60);
  remaining -= m * 60;
  const parts: string[] = [];
  if (d) parts.push(`${d}d`);
  if (h) parts.push(`${h}h`);
  if (m) parts.push(`${m}m`);
  parts.push(`${remaining}s`);
  return parts.join(" ");
}

export function fmtTimestampSeconds(ts?: number | null): string {
  if (!ts || !Number.isFinite(ts) || ts <= 0) {
    return "—";
  }
  const d = new Date(ts * 1000);
  const ageSec = Math.floor((Date.now() - d.getTime()) / 1000);
  return `${d.toISOString()} (${fmtUptime(ageSec)} ago)`;
}

export function fmtTimestamp(ts?: string | null): string {
  if (!ts) {
    return "—";
  }
  const d = new Date(ts);
  if (Number.isNaN(d.getTime())) {
    return ts;
  }
  return d.toISOString();
}

export function formatBytes(bytes?: number | null): string {
  if (bytes == null) {
    return "—";
  }
  return bytes.toString();
}
