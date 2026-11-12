// The system values of one daemon that are not in /_stats: the log settings
// (GET and PUT /_log) and the counters of GET /_metrics.

import type { Ndb } from '@/ndb';

export const LOG_LEVELS = ['debug', 'info', 'warn', 'error', 'quiet'] as const;

export type LogLevel = (typeof LOG_LEVELS)[number];

export interface LogSettings {
  level: string;
  format: string;
  body_max: number;
  slow_ms: number;
}

export function logSettings(ndb: Ndb): Promise<LogSettings> {
  return ndb.call<LogSettings>('GET', '/_log');
}

export function setLogSettings(ndb: Ndb, change: Partial<Pick<LogSettings, 'level' | 'body_max' | 'slow_ms'>>): Promise<LogSettings> {
  return ndb.call<LogSettings>('PUT', '/_log', change);
}

// Metrics holds the series of /_metrics, summed over their labels: the
// memtable bytes of every collection give one number.
export class Metrics {
  constructor(private readonly sums: Map<string, number>) {}

  get(name: string): number | undefined {
    return this.sums.get(name);
  }
}

// parseMetrics reads the Prometheus text format. It skips comments and the
// lines that it cannot read.
export function parseMetrics(text: string): Metrics {
  const sums = new Map<string, number>();

  for (const line of text.split('\n')) {
    const match = /^([a-zA-Z_:][a-zA-Z0-9_:]*)(?:\{.*\})?\s+(\S+)$/.exec(line.trim());

    if (!match) continue;

    const value = Number(match[2]);

    if (!Number.isFinite(value)) continue;

    sums.set(match[1]!, (sums.get(match[1]!) ?? 0) + value);
  }

  return new Metrics(sums);
}

// metrics reads /_metrics. A daemon that refuses it gives undefined, so the
// page shows the other parts.
export async function metrics(ndb: Ndb): Promise<Metrics | undefined> {
  try {
    const response = await ndb.forward('GET', '/_metrics');

    if (!response.ok) return undefined;

    return parseMetrics(await response.text());
  } catch {
    return undefined;
  }
}

// parseCount reads a form field as a whole number of zero or more.
export function parseCount(value: unknown): number | undefined {
  const text = String(value ?? '').trim();

  if (!/^\d{1,9}$/.test(text)) return undefined;

  return Number(text);
}
