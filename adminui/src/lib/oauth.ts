import type { OAuthRow, OAuthRowDetails, OAuthStatusResponse } from "./types";

const discoveryOrder = [
  { source: "www_authenticate" },
  { source: "well_known_path" },
  { source: "well_known_root" },
];

function fallbackURLs(discoveryUrls?: string[]): {
  well_known_path: string;
  well_known_root: string;
} {
  const urls = Array.isArray(discoveryUrls) ? discoveryUrls : [];
  const out = { well_known_path: "", well_known_root: "" };
  if (urls.length === 1) {
    out.well_known_root = urls[0];
  } else if (urls.length >= 2) {
    if (urls.length >= 3) {
      out.well_known_path = urls[1];
      out.well_known_root = urls[2];
    } else {
      out.well_known_path = urls[0];
      out.well_known_root = urls[1];
    }
  }
  return out;
}

function reasonForAttempt(
  attempt: { error?: string; tried?: boolean } | undefined,
  pending: boolean,
  type: string,
  probe?: { error?: string },
): string {
  if (attempt) {
    if (attempt.error) return attempt.error;
    if (!attempt.tried && !pending) {
      return "Skipped: higher-priority selected";
    }
  }
  if (type === "www_authenticate" && probe?.error) {
    return probe.error;
  }
  return "";
}

function buildDetails(details: OAuthRowDetails): OAuthRowDetails {
  return {
    statusCode: details.statusCode ?? 0,
    status: details.status ?? "",
    fetchedAt: details.fetchedAt ?? "",
    source: details.source ?? "",
    sourceURL: details.sourceURL ?? "",
    headers: details.headers ?? null,
    body: details.body,
    bodyText: details.bodyText ?? "",
    error: details.error ?? "",
  };
}

function buildPRMDRows(payload: OAuthStatusResponse | null): OAuthRow[] {
  const pending = payload?.pending ?? false;
  const probe = payload?.www_authenticate_probe ?? undefined;
  const attempts = payload?.metadata?.attempts ?? [];
  const attemptsBySource = new Map(
    attempts
      .filter((attempt) => attempt?.source)
      .map((attempt) => [attempt.source as string, attempt]),
  );

  const fallback = fallbackURLs(payload?.discovery_urls);
  const rows: OAuthRow[] = [];

  discoveryOrder.forEach((entry, idx) => {
    const type = entry.source;
    const attempt = attemptsBySource.get(type);
    const reason = reasonForAttempt(attempt, pending, type, probe);

    let urlText = attempt?.url || "";
    if (type === "www_authenticate") {
      if (!urlText && probe?.url) urlText = probe.url;
    } else if (!urlText) {
      urlText = type === "well_known_path" ? fallback.well_known_path : fallback.well_known_root;
    }
    if (!urlText) urlText = "—";

    let status = "skipped";
    if (attempt?.selected) {
      status = "fetched";
    } else if (attempt?.tried && attempt?.error) {
      status = "error";
    } else if (attempt?.tried) {
      status = "tried";
    } else if (pending) {
      status = "pending";
    }

    rows.push({
      key: `prmd|${type}|${urlText}`,
      priority: idx + 1,
      step: type,
      url: urlText,
      status,
      details: buildDetails({
        statusCode: attempt?.status_code,
        status,
        fetchedAt: payload?.metadata?.fetched_at,
        source: payload?.metadata_source,
        sourceURL: urlText,
        headers: attempt?.selected ? payload?.metadata?.headers ?? null : null,
        body: attempt?.selected ? payload?.metadata?.body ?? null : null,
        bodyText: attempt?.selected ? payload?.metadata?.body_text ?? "" : "",
        error: reason,
      }),
    });
  });

  return rows;
}

function buildAuthMetaRows(payload: OAuthStatusResponse | null, startPriority: number): OAuthRow[] {
  const rows: OAuthRow[] = [];
  const attempts = payload?.metadata?.auth_server_metadata?.attempts ?? [];

  attempts.forEach((attempt, idx) => {
    let status = "skipped";
    if (attempt?.selected) {
      status = "fetched";
    } else if (attempt?.tried && attempt?.error) {
      status = "error";
    } else if (attempt?.tried) {
      status = "tried";
    }

    const stepBits = ["auth_server_meta"];
    if (attempt?.document) stepBits.push(String(attempt.document));
    if (attempt?.path_style) stepBits.push(`(${String(attempt.path_style)})`);

    rows.push({
      key: `auth|${idx}|${attempt?.url ?? "—"}`,
      priority: startPriority + idx,
      step: stepBits.join(" "),
      url: attempt?.url ?? "—",
      status,
      details: buildDetails({
        statusCode: attempt?.status_code,
        status,
        fetchedAt: payload?.metadata?.fetched_at,
        source: "auth_server_metadata",
        sourceURL: attempt?.url ?? "—",
        headers: attempt?.headers ?? null,
        body: attempt?.body ?? null,
        bodyText: attempt?.body_text ?? "",
        error: attempt?.error ?? "",
      }),
    });
  });

  return rows;
}

export function buildOAuthRows(payload: OAuthStatusResponse | null): OAuthRow[] {
  const prmdRows = buildPRMDRows(payload);
  const authRows = buildAuthMetaRows(payload, prmdRows.length + 1);
  return prmdRows.concat(authRows);
}

export function hasDetails(details: OAuthRowDetails): boolean {
  return !!(
    details.statusCode ||
    details.error ||
    (details.headers && Object.keys(details.headers).length) ||
    details.body ||
    details.bodyText ||
    details.fetchedAt
  );
}
