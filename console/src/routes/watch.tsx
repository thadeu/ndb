import type { Hono } from 'hono';

import { checkSettings, readFrom, watchBody, type WatchSettings } from '@/components/watch-model';
import { NdbError } from '@/ndb';
import type { Env } from '@/prefix';
import { OfflinePage } from '@/views/offline';
import { WatchPage } from '@/views/watch';

import { connectionOf, type RouteDeps } from './context';
import { readWhere } from './browser';

const COLLECTION = /^[A-Za-z0-9._-]{1,128}$/;
const encoder = new TextEncoder();
const PING = encoder.encode(': ping\n\n');

// PING_MS keeps the stream busy while the collection is quiet: Bun closes a
// connection after 10 s without bytes (and proxies have limits of their
// own), and ndb's heartbeat comes every 15 s. EventSource skips comments.
const PING_MS = 5_000;

function frame(event: string | undefined, data: string): Uint8Array {
  return encoder.encode(`${event ? `event: ${event}\n` : ''}data: ${data}\n\n`);
}

export function watchRoutes(app: Hono<Env>, deps: RouteDeps): void {
  app.get('/c/:name/watch', async c => {
    const connection = connectionOf(deps, c);

    if (!connection) return c.text('Not found', 404);

    try {
      const stats = await deps.client(connection).stats();
      const coll = c.req.query('coll') || stats.collections[0]?.name || '';

      const settings: WatchSettings = { coll, where: c.req.query('where') ?? '', from: readFrom(c.req.query('from')), consumer: c.req.query('consumer') ?? '' };

      return c.html(<WatchPage current={deps.currentOf(connection, stats)} collections={stats.collections} settings={settings} />);
    } catch (error) {
      return c.html(<OfflinePage current={deps.currentOf(connection)} message={deps.message(error)} />, 502);
    }
  });

  // The stream is a read-only tail: it opens a watch, forwards each line as
  // an SSE event, and never acks. The consumer is one of its own, or a named
  // one (to read from its saved position, with no filter: see checkSettings).
  // The upstream closes when the browser leaves.
  app.get('/c/:name/watch/stream', c => {
    const connection = connectionOf(deps, c);

    if (!connection) return c.text('Not found', 404);

    const settings: WatchSettings = { coll: c.req.query('coll') ?? '', where: c.req.query('where') ?? '', from: readFrom(c.req.query('from')), consumer: c.req.query('consumer') ?? '' };
    const coll = settings.coll;
    const parsed = readWhere(settings.where);
    const problem = !COLLECTION.test(coll) ? 'Pick a collection.' : (parsed.error ?? checkSettings(settings, parsed.where));

    const abort = new AbortController();

    c.req.raw.signal.addEventListener('abort', () => abort.abort());

    const body = new ReadableStream<Uint8Array>({
      async start(controller) {
        if (problem) {
          controller.enqueue(frame('problem', problem));
          controller.close();

          return;
        }

        const ping = setInterval(() => {
          if (!abort.signal.aborted) controller.enqueue(PING);
        }, PING_MS);

        abort.signal.addEventListener('abort', () => clearInterval(ping));

        const consumer = settings.from === 'consumer' ? settings.consumer : `ndb-ui-${crypto.randomUUID().slice(0, 8)}`;
        const request = watchBody(settings, parsed.where, consumer);

        try {
          const upstream = await deps.client(connection).stream(`/${encodeURIComponent(coll)}/_watch`, request, abort.signal);

          controller.enqueue(frame('ready', consumer));

          await forward(upstream, controller, abort.signal);
        } catch (error) {
          if (!abort.signal.aborted) controller.enqueue(frame('problem', error instanceof NdbError ? error.message : 'The watch failed.'));
        }

        clearInterval(ping);

        if (!abort.signal.aborted) controller.enqueue(frame('end', 'closed'));

        controller.close();
      },
      cancel() {
        abort.abort();
      },
    });

    return new Response(body, { headers: { 'content-type': 'text/event-stream; charset=utf-8', 'cache-control': 'no-cache, no-transform', 'x-accel-buffering': 'no' } });
  });
}

async function forward(upstream: Response, controller: ReadableStreamDefaultController<Uint8Array>, signal: AbortSignal): Promise<void> {
  const reader = upstream.body?.getReader();

  if (!reader) return;

  const decoder = new TextDecoder();
  let buffer = '';

  while (!signal.aborted) {
    const { done, value } = await reader.read();

    if (done) break;

    buffer += decoder.decode(value, { stream: true });

    const lines = buffer.split('\n');

    buffer = lines.pop() ?? '';

    for (const line of lines) {
      if (line.trim() !== '') controller.enqueue(frame(undefined, line));
    }
  }
}
