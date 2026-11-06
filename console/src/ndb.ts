// Client for one ndb daemon. Only the server uses it, so the token never
// reaches a browser. The API contract is docs/dev/http-api.md in thadeu/ndb.

export interface Endpoint {
  url: string;
  token?: string;
}

export interface IndexState {
  field: string;
  state: string;
  done: number;
  total: number;
}

export interface Parts {
  wal: number;
  segments: number;
  meta: number;
  blobs?: number;
  cursors?: number;
}

export interface CollectionStats {
  name: string;
  write: string;
  rows: number;
  quarantined: number;
  indexes: IndexState[];
  segments: number;
  wal_files: number;
  bytes: Parts;
  total: number;
}

export interface Stats {
  version: string;
  uptime_seconds: number;
  collections: CollectionStats[];
  totals: { collections: number; rows: number; quarantined: number; segments: number; wal_files: number; bytes: number };
  blobs_bytes: number;
  cursors_bytes: number;
  shared_bytes: number;
}

export interface Size {
  data_dir: string;
  total: number;
  collections: { name: string; bytes: Required<Parts>; total: number; wal_files: number; segment_files: number }[];
  shared: Required<Parts>;
}

export interface Definition {
  name: string;
  time?: string;
  write?: string;
  path?: string;
  indexes?: string[];
  unique?: string[];
  durability?: string;
  retention?: string;
  wal_retention?: string;
  ttl?: string;
  expires_field?: string;
}

export type Doc = Record<string, unknown>;

export interface FindResult {
  rows: Doc[];
  plan?: string;
  examined?: number;
  total?: number;
  next?: string;
}

export interface UpdateResult {
  matched: number;
  modified: number;
  doc?: Doc;
}

// NdbError carries the code of the ndb error body, such as unknown_collection.
// Network failures use the codes unreachable and timeout.
export class NdbError extends Error {
  constructor(
    readonly code: string,
    message: string,
    readonly status = 0,
  ) {
    super(message);
  }
}

export interface Target {
  base: string;
  unix?: string;
}

// target turns a connection URL into the base URL for fetch. A unix socket
// uses a fixed host name and the unix option of Bun fetch.
export function target(url: string): Target {
  if (url.startsWith('unix://')) {
    const path = url.slice('unix://'.length);

    if (!path.startsWith('/')) throw new NdbError('invalid_url', 'A unix URL needs an absolute path: unix:///path/to.sock');

    return { base: 'http://ndb', unix: path };
  }

  if (url.startsWith('tcp://')) return { base: `http://${url.slice('tcp://'.length).replace(/\/+$/, '')}` };

  if (/^https?:\/\/[^/]+/.test(url)) return { base: url.replace(/\/+$/, '') };

  throw new NdbError('invalid_url', 'Use unix:///path, tcp://host:port, or http(s)://host.');
}

export class Ndb {
  constructor(
    private readonly endpoint: Endpoint,
    private readonly timeoutMs = 3000,
  ) {}

  health(): Promise<{ status: string }> {
    return this.get('/health');
  }

  stats(): Promise<Stats> {
    return this.get('/v1/stats');
  }

  size(): Promise<Size> {
    return this.get('/v1/size');
  }

  async collections(): Promise<Definition[]> {
    const body = await this.get<{ collections: Definition[] }>('/v1/c');

    return body.collections;
  }

  collection(name: string): Promise<{ definition: Definition; indexes: IndexState[] }> {
    return this.get(`/v1/c/${encodeURIComponent(name)}`);
  }

  async find(collection: string, query: Record<string, unknown>): Promise<FindResult> {
    const response = await this.open('POST', `/v1/c/${encodeURIComponent(collection)}/find`, { body: JSON.stringify(query) });

    if (!response.ok) throw await failure(response);

    const text = await response.text();
    const rows = text
      .split('\n')
      .filter(line => line.trim() !== '')
      .map(line => JSON.parse(line) as Doc);

    const total = response.headers.get('x-ndb-total');
    const examined = response.headers.get('x-ndb-examined');

    return {
      rows,
      plan: response.headers.get('x-ndb-plan') ?? undefined,
      examined: examined === null ? undefined : Number(examined),
      total: total === null ? undefined : Number(total),
      next: response.headers.get('x-ndb-next') ?? undefined,
    };
  }

  document(collection: string, id: string): Promise<Doc> {
    return this.json('GET', `/v1/c/${encodeURIComponent(collection)}/${encodeURIComponent(id)}`);
  }

  update(collection: string, id: string, body: Record<string, unknown>): Promise<UpdateResult> {
    return this.json('POST', `/v1/c/${encodeURIComponent(collection)}/${encodeURIComponent(id)}/update`, JSON.stringify(body));
  }

  insert(collection: string, docs: Doc[]): Promise<{ results: { _id: string; v: number }[] }> {
    return this.json('POST', `/v1/c/${encodeURIComponent(collection)}/insert`, JSON.stringify({ insert: docs }));
  }

  remove(collection: string, id: string): Promise<{ deleted: number }> {
    return this.json('DELETE', `/v1/c/${encodeURIComponent(collection)}/${encodeURIComponent(id)}`);
  }

  // forward sends a request as it is and returns the response, even when the
  // status is not 2xx. Only a network failure throws.
  forward(method: string, path: string, body?: string): Promise<Response> {
    return this.open(method, path, { body, timeoutMs: 30_000 });
  }

  // stream opens a long answer, such as a watch or a backup. It has no
  // timeout: the caller ends it with the signal. A non-2xx status throws.
  async stream(path: string, body: unknown, signal: AbortSignal): Promise<Response> {
    const response = await this.open('POST', path, { body: JSON.stringify(body), timeoutMs: 0, signal });

    if (!response.ok) throw await failure(response);

    return response;
  }

  private get<T>(path: string): Promise<T> {
    return this.json('GET', path);
  }

  private async json<T>(method: string, path: string, body?: string): Promise<T> {
    const response = await this.open(method, path, { body });
    const parsed = await response.json().catch(() => null);

    if (response.ok) return parsed as T;

    throw errorOf(response, parsed);
  }

  private async open(method: string, path: string, init: { body?: string; timeoutMs?: number; signal?: AbortSignal } = {}): Promise<Response> {
    const { base, unix } = target(this.endpoint.url);
    const headers: Record<string, string> = { accept: 'application/json' };

    if (this.endpoint.token) headers.authorization = `Bearer ${this.endpoint.token}`;

    const timeoutMs = init.timeoutMs ?? this.timeoutMs;
    const limit = timeoutMs > 0 ? AbortSignal.timeout(timeoutMs) : undefined;
    const signals = [limit, init.signal].filter((signal): signal is AbortSignal => signal !== undefined);
    const signal = signals.length > 0 ? AbortSignal.any(signals) : undefined;

    try {
      return await fetch(base + path, { method, headers, body: init.body, unix, signal } as RequestInit);
    } catch (error) {
      const timedOut = error instanceof Error && error.name === 'TimeoutError';

      throw new NdbError(timedOut ? 'timeout' : 'unreachable', timedOut ? 'ndb did not answer in time.' : 'Cannot reach ndb at this URL.');
    }
  }
}

function errorOf(response: Response, body: { error?: { code: unknown; message: unknown } } | null): NdbError {
  if (body?.error) return new NdbError(String(body.error.code), String(body.error.message), response.status);

  return new NdbError(response.status === 503 ? 'recovering' : 'http_error', `ndb answered ${response.status}.`, response.status);
}

async function failure(response: Response): Promise<NdbError> {
  return errorOf(response, await response.json().catch(() => null));
}
