import { readFileSync, readdirSync, statSync } from 'node:fs';
import { join } from 'node:path';

export type DiscoverState = 'ok' | 'starting' | 'needs_token';

export interface Found {
  url: string;
  state: DiscoverState;
  version?: string;
  collections?: number;
}

export interface DiscoverResult {
  found: Found[];
  searched: number;
  hint?: string;
}

export interface DiscoverOptions {
  fetch?: typeof fetch;
  configPaths?: string[];
  socketDirs?: string[];
  tmpDir?: string;
  timeoutMs?: number;
  env?: Record<string, string | undefined>;
}

export const DEFAULT_PORT = 7070;
export const MAX_PORTS = 10;
export const MAX_CANDIDATES = 50;

const LOCAL_HOSTS = new Set(['', 'localhost', '127.0.0.1', '::1']);
const HOSTNAME = /^(?=.{1,253}$)([a-z0-9]([a-z0-9-]{0,61}[a-z0-9])?)(\.[a-z0-9]([a-z0-9-]{0,61}[a-z0-9])?)*$/i;
const IPV4 = /^(\d{1,3})(\.\d{1,3}){3}$/;
const IPV6 = /^[0-9a-f:]+(%[a-z0-9]+)?$/i;

export class DiscoverError extends Error {}

// parseHost returns the trimmed host, or throws when it is not a hostname or
// an IP address. A scheme, a path, a port, or a space is refused.
export function parseHost(raw: string): string {
  const host = raw.trim().replace(/^\[(.*)\]$/, '$1');

  if (host === '') return '';

  const v4 = IPV4.test(host) && host.split('.').every(part => Number(part) <= 255);
  const v6 = host.includes(':') && IPV6.test(host);
  const name = !host.includes(':') && !IPV4.test(host) && HOSTNAME.test(host);

  if (!v4 && !v6 && !name) throw new DiscoverError('Use a host name or an IP address, without a scheme or a path.');

  return host;
}

// parsePorts reads a comma separated list. Only 1 to 65535 count, at most 10.
export function parsePorts(raw: string): number[] {
  const ports: number[] = [];

  for (const part of raw.split(',')) {
    const text = part.trim();

    if (text === '') continue;

    if (!/^\d{1,5}$/.test(text)) throw new DiscoverError(`Bad port: ${text.slice(0, 12)}`);

    const port = Number(text);

    if (port < 1 || port > 65535) throw new DiscoverError(`Bad port: ${port}`);

    if (!ports.includes(port)) ports.push(port);
  }

  if (ports.length > MAX_PORTS) throw new DiscoverError(`Use at most ${MAX_PORTS} ports.`);

  return ports.length > 0 ? ports : [DEFAULT_PORT];
}

// unixFromConfig finds the `unix:` key under `listen:` in an ndb.yml text.
export function unixFromConfig(text: string): string | undefined {
  let inListen = false;

  for (const line of text.split('\n')) {
    if (/^\s*(#.*)?$/.test(line)) continue;

    if (/^listen:\s*(#.*)?$/.test(line)) {
      inListen = true;

      continue;
    }

    if (!/^\s/.test(line)) {
      inListen = false;

      continue;
    }

    const match = inListen ? line.match(/^\s+unix:\s*(.*?)\s*(#.*)?$/) : null;
    const value = match?.[1]?.replace(/^["']|["']$/g, '');

    if (value) return value;
  }

  return undefined;
}

function isSocket(path: string): boolean {
  try {
    return statSync(path).isSocket();
  } catch {
    return false;
  }
}

function socketsIn(dir: string): string[] {
  try {
    return readdirSync(dir)
      .filter(name => name.endsWith('.sock'))
      .map(name => join(dir, name));
  } catch {
    return [];
  }
}

function ndbDirsIn(dir: string): string[] {
  try {
    return readdirSync(dir)
      .filter(name => name.includes('ndb'))
      .map(name => join(dir, name))
      .filter(path => statSync(path).isDirectory());
  } catch {
    return [];
  }
}

// localSockets lists the unix sockets that may be an ndb daemon.
export function localSockets(options: DiscoverOptions = {}): string[] {
  const env = options.env ?? process.env;
  const tmpDir = options.tmpDir ?? '/tmp';
  const configPaths = options.configPaths ?? [env.NDB_CONFIG, './ndb.yml', '/etc/ndb/ndb.yml'].filter((path): path is string => !!path);
  const dirs = options.socketDirs ?? ['/run/ndb', '/var/run', '/run', tmpDir, env.XDG_RUNTIME_DIR].filter((dir): dir is string => !!dir);
  const candidates: string[] = [];

  for (const path of configPaths) {
    try {
      const unix = unixFromConfig(readFileSync(path, 'utf8'));

      if (unix) candidates.push(unix);
    } catch {
      continue;
    }
  }

  candidates.push('/run/ndb/ndb.sock');

  for (const dir of dirs) candidates.push(...socketsIn(dir));

  for (const dir of ndbDirsIn(tmpDir)) candidates.push(...socketsIn(dir));

  return [...new Set(candidates)].filter(isSocket).slice(0, MAX_CANDIDATES);
}

function baseOf(url: string): { base: string; unix?: string } {
  if (url.startsWith('unix://')) return { base: 'http://ndb', unix: url.slice('unix://'.length) };

  return { base: `http://${url.slice('tcp://'.length)}` };
}

// probe asks one candidate. It never sends a token. A thing that is not ndb
// gives undefined.
export async function probe(url: string, options: DiscoverOptions = {}): Promise<Found | undefined> {
  const doFetch = options.fetch ?? fetch;
  const timeoutMs = options.timeoutMs ?? 800;
  const { base, unix } = baseOf(url);

  const get = (path: string) => doFetch(base + path, { headers: { accept: 'application/json' }, signal: AbortSignal.timeout(timeoutMs), unix } as RequestInit);

  try {
    const response = await get('/_health');
    const body = (await response.json().catch(() => null)) as { status?: unknown; error?: { code?: unknown } } | null;

    if (response.status === 401 && body?.error?.code === 'unauthorized') return { url, state: 'needs_token' };

    if (response.status === 503 && (body?.status === 'recovering' || body?.error?.code === 'recovering' || body?.error?.code === 'not_ready')) return { url, state: 'starting' };

    if (response.status !== 200 || body?.status !== 'ok') return undefined;

    const stats = await get('/_stats')
      .then(r => (r.ok ? r.json() : null))
      .catch(() => null) as { version?: string; totals?: { collections?: number } } | null;

    return { url, state: 'ok', version: stats?.version, collections: stats?.totals?.collections };
  } catch {
    return undefined;
  }
}

// discover searches this machine (sockets and 127.0.0.1) or a remote host.
export async function discover(rawHost: string, rawPorts: string, options: DiscoverOptions = {}): Promise<DiscoverResult> {
  const host = parseHost(rawHost);
  const ports = parsePorts(rawPorts);
  const local = LOCAL_HOSTS.has(host);

  const sockets = local ? localSockets(options).map(path => `unix://${path}`) : [];
  const target = local ? '127.0.0.1' : host.includes(':') ? `[${host}]` : host;
  const tcp = ports.map(port => `tcp://${target}:${port}`);
  const candidates = [...sockets, ...tcp];

  const settled = await Promise.allSettled(candidates.map(url => probe(url, options)));
  const found = settled.flatMap(item => (item.status === 'fulfilled' && item.value ? [item.value] : []));

  const result: DiscoverResult = { found, searched: candidates.length };

  if (local && found.length === 0) result.hint = 'No socket found. If the console runs in Docker, mount the ndb socket volume.';

  return result;
}
