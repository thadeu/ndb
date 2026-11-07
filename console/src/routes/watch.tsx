import type { Hono } from 'hono';

import { NdbError } from '@/ndb';
import type { Env } from '@/prefix';
import { OfflinePage } from '@/views/offline';
import { WatchPage } from '@/views/watch';

import { connectionOf, type RouteDeps } from './context';
import { readWhere } from './browser';

const COLLECTION = /^[A-Za-z0-9._-]{1,128}$/;
const encoder = new TextEncoder();

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

      return c.html(<WatchPage current={deps.currentOf(connection, stats)} collections={stats.collections} coll={coll} where={c.req.query('where') ?? ''} />);
    } catch (error) {
      return c.html(<OfflinePage current={deps.currentOf(connection)} message={deps.message(error)} />, 502);
    }
  });

  // The stream is a read-only tail: it opens a watch with a consumer of its
  // own, forwards each line as an SSE event, and never acks. The upstream
  // closes when the browser leaves.
  app.get('/c/:name/watch/stream', c => {
    const connection = connectionOf(deps, c);

    if (!connection) return c.text('Not found', 404);

    const coll = c.req.query('coll') ?? '';
    const parsed = readWhere(c.req.query('where') ?? '');
    const from = c.req.query('from') === 'beginning' ? 'beginning' : 'now';
    const problem = !COLLECTION.test(coll) ? 'Pick a collection.' : parsed.error;

    const abort = new AbortController();

    c.req.raw.signal.addEventListener('abort', () => abort.abort());

    const body = new ReadableStream<Uint8Array>({
      async start(controller) {
        if (problem) {
          controller.enqueue(frame('problem', problem));
          controller.close();

          return;
        }

        const consumer = `ndb-ui-${crypto.randomUUID().slice(0, 8)}`;
        const request: Record<string, unknown> = { consumer, from };

        if (parsed.where && Object.keys(parsed.where).length > 0) request.query = parsed.where;

        try {
          const upstream = await deps.client(connection).stream(`/${encodeURIComponent(coll)}/_watch`, request, abort.signal);

          controller.enqueue(frame('ready', consumer));

          await forward(upstream, controller, abort.signal);
        } catch (error) {
          if (!abort.signal.aborted) controller.enqueue(frame('problem', error instanceof NdbError ? error.message : 'The watch failed.'));
        }

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
