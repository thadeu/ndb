import type { Context, Hono } from 'hono';

import { NdbError, target } from '@/ndb';
import { utc } from '@/pitr';
import type { Env } from '@/prefix';
import { BackupsPage, type BackupsQuery } from '@/views/backups';
import { OfflinePage } from '@/views/offline';

import { connectionOf, type RouteDeps } from './context';

function stamp(date: Date): string {
  return date.toISOString().replace(/[-:]/g, '').replace(/\..*/, '').replace('T', '-');
}

export function backupsRoutes(app: Hono<Env>, deps: RouteDeps): void {
  app.get('/c/:name/backups', async c => {
    const connection = connectionOf(deps, c);

    if (!connection) return c.text('Not found', 404);

    const client = deps.client(connection);
    const q = c.req.query();

    const query: BackupsQuery = { page: Number(q.page ?? 1), restore: q.restore, at: q.at, rename: q.rename === '1', from: q.from, to: q.to };

    try {
      const [stats, pitr] = await Promise.all([client.stats(), client.pitr()]);

      return c.html(<BackupsPage current={deps.currentOf(connection, stats)} online={Boolean(target(connection.url).unix)} pitr={pitr} query={query} notice={q.notice} error={q.error} />);
    } catch (error) {
      return c.html(<OfflinePage current={deps.currentOf(connection)} message={deps.message(error)} />, 502);
    }
  });

  // The download streams POST /_backup from ndb to the browser. ndb answers
  // 403 over TCP, so the console refuses a TCP connection before it asks.
  app.post('/c/:name/backups/download', async c => {
    const connection = connectionOf(deps, c);

    if (!connection) return c.text('Not found', 404);

    const back = (error: string) => backTo(c, connection.name, { error });

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

  // Restore is POST /{coll}/_restore. On success the page comes back with the
  // rename dialog open and filled in, to put the copy in place.
  app.post('/c/:name/backups/restore', async c => {
    const connection = connectionOf(deps, c);

    if (!connection) return c.text('Not found', 404);

    const form = await c.req.parseBody().catch(() => ({}) as Record<string, unknown>);
    const collection = String(form.collection ?? '');
    const at = String(form.at ?? '');
    const to = String(form.to ?? '').trim();

    try {
      const res = await deps.client(connection).restore(collection, utc(at), to);

      return backTo(c, connection.name, { notice: `${collection} as of ${at.replace('T', ' ')} UTC is in ${to} (${res.duration_ms} ms). Check it, then rename it in place.`, from: to, to: collection });
    } catch (error) {
      return backTo(c, connection.name, { error: error instanceof NdbError ? error.message : 'The restore failed.', restore: collection, at });
    }
  });

  app.post('/c/:name/backups/rename', async c => {
    const connection = connectionOf(deps, c);

    if (!connection) return c.text('Not found', 404);

    const form = await c.req.parseBody().catch(() => ({}) as Record<string, unknown>);
    const from = String(form.from ?? '').trim();
    const to = String(form.to ?? '').trim();
    const replace = form.replace === '1' || form.replace === 'on';

    try {
      await deps.client(connection).rename(from, to, replace);

      return backTo(c, connection.name, { notice: replace ? `${from} is now ${to}. The old ${to} stays restorable for its window.` : `${from} is now ${to}.` });
    } catch (error) {
      return backTo(c, connection.name, { error: error instanceof NdbError ? error.message : 'The rename failed.', from, to });
    }
  });
}

function backTo(c: Context<Env>, name: string, params: Record<string, string>) {
  return c.redirect(`${c.var.prefix}/c/${encodeURIComponent(name)}/backups?${new URLSearchParams(params)}`, 303);
}
