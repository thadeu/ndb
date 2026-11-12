import type { Context, Hono } from 'hono';

import { NdbError, target } from '@/ndb';
import { restoreName, utc } from '@/pitr';
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

    const query: BackupsQuery = {
      page: Number(q.page ?? 1),
      restore: q.restore,
      at: q.at,
      where: q.where,
      confirm: q.confirm === '1',
      restored: q.restored,
      rename: q.rename === '1',
      from: q.from,
      to: q.to,
    };

    try {
      const [stats, pitr] = await Promise.all([client.stats(), client.pitr()]);

      const rows = Object.fromEntries(stats.collections.map(item => [item.name, item.rows]));

      return c.html(
        <BackupsPage
          current={deps.currentOf(connection, stats)}
          online={Boolean(target(connection.url).unix)}
          pitr={pitr}
          rows={rows}
          query={query}
          notice={q.notice}
          error={q.error}
        />,
      );
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

  // Restore is POST /{coll}/_restore: always a copy in a new collection. With
  // where=copy the page comes back with a notice. With replace=1 the copy
  // takes the name of the collection at once (the confirm step said so). In
  // other cases the page comes back with step 2 open and filled in: the
  // rename that puts the copy in place. back=explorer returns to the Backups
  // tab of the collection.
  app.post('/c/:name/backups/restore', async c => {
    const connection = connectionOf(deps, c);

    if (!connection) return c.text('Not found', 404);

    const form = await c.req.parseBody().catch(() => ({}) as Record<string, unknown>);
    const collection = String(form.collection ?? '');
    const at = String(form.at ?? '');
    const copyOnly = form.where === 'copy';
    const replace = !copyOnly && form.replace === '1';
    const to = replace ? restoreName(collection) : String(form.to ?? '').trim();

    const client = deps.client(connection);
    const instant = `${at.replace('T', ' ')} UTC`;

    const back = (params: Record<string, string>) => {
      if (form.back !== 'explorer') return backTo(c, connection.name, params);

      const { restore: _restore, from: _from, ...rest } = params;

      return toExplorer(c, connection.name, { coll: collection, tab: 'backups', ...rest });
    };

    const draft: Record<string, string> = { restore: collection, at, where: copyOnly ? 'copy' : 'place', ...(replace ? {} : { to }) };

    try {
      const res = await client.restore(collection, utc(at), to);
      const done = `${collection} as of ${instant} is in ${to} (${res.duration_ms} ms).`;

      if (copyOnly) return back({ notice: `${done} ${collection} did not change.`, restored: to });

      if (!replace) return back({ notice: `${done} Check it, then rename it in place.`, from: to, to: collection });
    } catch (error) {
      return back({ error: error instanceof NdbError ? error.message : 'The restore failed.', ...draft });
    }

    try {
      await client.rename(to, collection, true);

      return back({ notice: `${collection} is now as it was at ${instant}. The old ${collection} is in the trash and stays restorable for its PITR window.`, restored: collection });
    } catch (error) {
      const why = error instanceof NdbError ? error.message : 'ndb did not answer.';

      return back({ error: `The copy ${to} is ready, but ${collection} did not change: ${why}`, ...draft });
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

function toExplorer(c: Context<Env>, name: string, params: Record<string, string>) {
  return c.redirect(`${c.var.prefix}/c/${encodeURIComponent(name)}/explorer?${new URLSearchParams(params)}`, 303);
}
