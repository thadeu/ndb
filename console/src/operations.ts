// The system values of one daemon that are not in /_stats: the settings of
// the data dir (GET and PUT /_cluster/settings), the log settings (GET and
// PUT /_log) and the counters of GET /_metrics.

import { type Ndb, NdbError } from '@/ndb';

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

// Setting is one value of /_cluster/settings and where it comes from:
// default, yml, env, flag or stored.
export interface Setting<T> {
  value: T;
  source: string;
}

// EncryptionKey is the key of the data dir, masked: the daemon never gives
// the key itself. source is made, stored or env.
export interface EncryptionKey {
  key: string;
  key_id: string;
  fingerprint: string;
  source: string;
  created_at?: string;
}

export interface ClusterSettings {
  encryption?: EncryptionKey;
  log?: {
    level?: Setting<string>;
    body_max?: Setting<number>;
    slow_ms?: Setting<number>;
  };
  warnings?: string[];
}

export function clusterSettings(ndb: Ndb): Promise<ClusterSettings> {
  return ndb.call<ClusterSettings>('GET', '/_cluster/settings');
}

// keyInDataDir says that the key is kept in the data dir (made or stored):
// a copy of the disk or of a backup can decrypt the values.
export function keyInDataDir(source?: string): boolean {
  return source === 'made' || source === 'stored';
}

// isOlderDaemon says that the daemon has no /_cluster/settings: it is older
// than 1.21 and answers 404.
export function isOlderDaemon(error: unknown): boolean {
  return error instanceof NdbError && error.status === 404;
}

export const OLDER_DAEMON = 'This daemon is older than 1.21: the change lasts until a restart.';

export interface SavedLog {
  level: string;
  body_max: number;
  slow_ms: number;
  warnings: string[];
  // stored is false when the daemon is older and the console used PUT /_log.
  stored: boolean;
}

type LogChange = Partial<Pick<LogSettings, 'level' | 'body_max' | 'slow_ms'>>;

// saveLogSettings stores the log settings with PUT /_cluster/settings, so
// they hold across a restart. An older daemon answers 404: then it uses PUT
// /_log, and the change lasts until the daemon stops.
export async function saveLogSettings(ndb: Ndb, change: LogChange): Promise<SavedLog> {
  try {
    const answer = await ndb.call<ClusterSettings>('PUT', '/_cluster/settings', { log: change });
    const log = answer.log ?? {};

    return {
      level: log.level?.value ?? String(change.level ?? ''),
      body_max: log.body_max?.value ?? Number(change.body_max ?? 0),
      slow_ms: log.slow_ms?.value ?? Number(change.slow_ms ?? 0),
      warnings: answer.warnings ?? [],
      stored: true,
    };
  } catch (error) {
    if (!isOlderDaemon(error)) throw error;

    const applied = await setLogSettings(ndb, change);

    return { level: applied.level, body_max: applied.body_max, slow_ms: applied.slow_ms, warnings: [], stored: false };
  }
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
