import type { MetricMap, MetricSample } from "./types";

export function parseMetrics(text: string): MetricMap {
  const out: MetricMap = new Map();
  const lines = (text || "").split("\n");
  for (const raw of lines) {
    const line = (raw || "").trim();
    if (!line || line.startsWith("#")) continue;
    const match = line.match(
      /^([a-zA-Z_:][a-zA-Z0-9_:]*)(\{[^}]*\})?\s+((?:[-+]?(?:\d+\.?\d*|\d*\.?\d+)(?:[eE][-+]?\d+)?)|NaN|\+Inf|-Inf|Inf)(?:\s+\d+)?$/,
    );
    if (!match) continue;
    const name = match[1];
    const value = Number(match[3]);
    if (!Number.isFinite(value)) continue;
    const sample: MetricSample = { labels: match[2] || "", value };
    if (!out.has(name)) out.set(name, []);
    out.get(name)?.push(sample);
  }
  return out;
}

function firstMetricSeries(map: MetricMap, names: string[]): MetricSample[] | null {
  for (const name of names) {
    const series = map.get(name);
    if (series && series.length) return series;
  }
  return null;
}

function findMetricSeries(map: MetricMap, names: string[]): MetricSample[] | null {
  for (const name of names) {
    const series = map.get(name);
    if (series && series.length) return series;
  }

  const keys = Array.from(map.keys());
  for (const name of names) {
    const matches = keys.filter((k) => k === name || k.endsWith(name));
    if (matches.length) {
      matches.sort((a, b) => a.length - b.length);
      const series = map.get(matches[0]);
      if (series && series.length) return series;
    }
  }

  for (const name of names) {
    const matches = keys.filter((k) => k.includes(name));
    if (matches.length) {
      matches.sort((a, b) => a.length - b.length);
      const series = map.get(matches[0]);
      if (series && series.length) return series;
    }
  }

  return null;
}

export function maxMetric(map: MetricMap, names: string[]): number | null {
  const series = findMetricSeries(map, names) ?? firstMetricSeries(map, names);
  if (!series) return null;
  let max: number | null = null;
  for (const sample of series) {
    if (max == null || sample.value > max) {
      max = sample.value;
    }
  }
  return max;
}

export function sumMetric(map: MetricMap, names: string[]): number | null {
  const series = findMetricSeries(map, names) ?? firstMetricSeries(map, names);
  if (!series) return null;
  return series.reduce((sum, sample) => sum + sample.value, 0);
}

export function extractInterestingMetricKeys(map: MetricMap): string[] {
  return Array.from(map.keys())
    .filter((key) => key.includes("commands_") || key.includes("dispatcher_"))
    .slice(0, 12);
}
