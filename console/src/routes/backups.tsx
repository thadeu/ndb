import type { Hono } from 'hono';

import { NdbError, target } from '@/ndb';
import type { Env } from '@/prefix';
import { BackupsPage } from '@/views/backups';
import { OfflinePage } from '@/views/offline';

import { connectionOf, type RouteDeps } from './context';

function stamp(date: Date): string {
  return date.toISOString().replace(/[-:]/g, '').replace(/\..*/, '').replace('T', '-');
}

export function backupsRoutes(app: Hono<Env>, deps: RouteDeps): void {
  app.get('/c/:name/backups', async c => {
    const connection = connectionOf(deps, c);

    if (!connection) return c.text('Not found', 404);

    try {
      const stats = await deps.client(connection).stats();

      return c.html(<BackupsPage current={deps.currentOf(connection, stats)} online={Boolean(target(connection.url).unix)} error={c.req.query('error')} />);
    } catch (error) {
      return c.html(<OfflinePage current={deps.currentOf(connection)} message={deps.message(error)} />, 502);
    }
  });

  // The download streams POST /_backup from ndb to the browser. ndb answers
  // 403 over TCP, so the console refuses a TCP connection before it asks.
  app.post('/c/:name/backups/download', async c => {
    const connection = connectionOf(deps, c);

    if (!connection) return c.text('Not found', 404);

    const back = (error: string) => c.redirect(`${c.var.prefix}/c/${encodeURIComponent(connection.name)}/backups?${new URLSearchParams({ error })}`, 303);

    if (!target(connection.url).unix) return back('Backups work only on a unix socket connection. Run ndb backup on the host.');

    const form = await c.req.parseBody().catch(() => ({}) as Record<string, unknown>);
    const gzip = form.gzip === '1' || form.gzip === 'on';
    const noBlobs = form.no_blobs === '1' || form.no_blobs === 'on';

    try {
      const upstream = await deps.client(connection).stream('/_backup', { no_blobs: noBlobs }, c.req.raw.signal);
      const source = upstream.body;

      if (!source) return back('ndb sent an empty backup.');

      const body = gzip ? source.pipeThrough(new CompressionStream('gzip')) : source;
      const filename = `ndb-${connection.name}-${stamp(new Date())}.ndb${gzip ? '.gz' : ''}`;

      return new Response(body, { headers: { 'content-type': gzip ? 'application/gzip' : 'application/x-ndjson', 'content-disposition': `attachment; filename="${filename}"`, 'cache-control': 'no-store' } });
    } catch (error) {
      return back(error instanceof NdbError ? error.message : 'The backup failed.');
    }
  });
}
