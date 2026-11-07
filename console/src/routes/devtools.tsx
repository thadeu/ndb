import type { Hono } from 'hono';

import { NdbError, target } from '@/ndb';
import type { Env } from '@/prefix';
import { DevtoolsPage } from '@/views/devtools';
import { OfflinePage } from '@/views/offline';

import { connectionOf, type RouteDeps } from './context';

const METHODS = new Set(['GET', 'POST', 'PUT', 'DELETE']);
const LIMIT = 4 * 1024 * 1024;

export interface SendRequest {
  method: string;
  path: string;
  body?: string;
}

// checkRequest says why a request may not go to ndb, or returns undefined.
// Every route of the API passes, except the backup (it has its own tab) and a
// watch (a stream that Dev Tools cannot show).
export function checkRequest(request: SendRequest): string | undefined {
  if (!METHODS.has(request.method)) return `Method ${request.method} is not supported. Use GET, POST, PUT, or DELETE.`;

  const path = request.path;

  if (!path.startsWith('/') || path.startsWith('//') || /[\s\\]/.test(path)) return 'The path must start with a single / and have no spaces.';

  let pathname: string;

  try {
    pathname = new URL(path, 'http://ndb').pathname;
  } catch {
    return 'The path is not valid.';
  }

  const segments = [...path.split(/[?#]/)[0]!.split('/'), ...pathname.split('/')];

  if (segments.some(part => part === '..' || part === '.')) return 'The path must not contain . or .. segments.';

  if (pathname === '/_backup') return 'Backups have their own tab: use Backups to download a file.';

  if (/^\/[^/]+\/_watch$/.test(pathname)) return 'Watch is a stream. Use the Watch tab to tail changes.';

  if (pathname === '/') return 'Write a path, such as GET /_stats or POST /orders/_search.';

  return undefined;
}

// readCapped reads a body up to the limit, so a huge answer cannot fill the
// memory of the console.
async function readCapped(response: Response): Promise<{ text: string; bytes: number; truncated: boolean }> {
  const reader = response.body?.getReader();

  if (!reader) return { text: '', bytes: 0, truncated: false };

  const chunks: Uint8Array[] = [];
  let bytes = 0;
  let truncated = false;

  for (;;) {
    const { done, value } = await reader.read();

    if (done) break;

    chunks.push(value);
    bytes += value.length;

    if (bytes > LIMIT) {
      truncated = true;
      await reader.cancel();

      break;
    }
  }

  const text = new TextDecoder().decode(Buffer.concat(chunks));

  return { text, bytes, truncated };
}

function curlTarget(url: string): { kind: 'unix' | 'tcp'; socket?: string; base?: string } {
  const parsed = target(url);

  return parsed.unix ? { kind: 'unix', socket: parsed.unix } : { kind: 'tcp', base: parsed.base };
}

export function devtoolsRoutes(app: Hono<Env>, deps: RouteDeps): void {
  app.get('/c/:name/devtools', async c => {
    const connection = connectionOf(deps, c);

    if (!connection) return c.text('Not found', 404);

    try {
      const stats = await deps.client(connection).stats();

      return c.html(<DevtoolsPage current={deps.currentOf(connection, stats)} collection={stats.collections[0]?.name ?? 'orders'} curl={curlTarget(connection.url)} />);
    } catch (error) {
      return c.html(<OfflinePage current={deps.currentOf(connection)} message={deps.message(error)} />, 502);
    }
  });

  app.post('/c/:name/devtools/send', async c => {
    const connection = connectionOf(deps, c);

    if (!connection) return c.json({ message: 'Not found.' }, 404);

    const input = await c.req.json().catch(() => null);

    if (!input || typeof input.method !== 'string' || typeof input.path !== 'string') return c.json({ message: 'The request is not valid.' }, 400);

    const request: SendRequest = { method: input.method.toUpperCase(), path: input.path, body: typeof input.body === 'string' ? input.body : undefined };
    const refused = checkRequest(request);

    if (refused) return c.json({ message: refused }, 400);

    const started = performance.now();

    try {
      const response = await deps.client(connection).forward(request.method, request.path, request.method === 'GET' || request.method === 'DELETE' ? undefined : request.body);
      const read = await readCapped(response);

      const headers: Record<string, string> = {};

      response.headers.forEach((value, key) => {
        if (key.startsWith('x-ndb-') || key === 'content-type') headers[key] = value;
      });

      return c.json({ status: response.status, ms: Math.round(performance.now() - started), bytes: read.bytes, headers, body: read.text, truncated: read.truncated });
    } catch (error) {
      return c.json({ message: error instanceof NdbError ? error.message : 'The request failed.' }, 502);
    }
  });
}
