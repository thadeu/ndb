import type { Hono } from 'hono';

import { NdbError } from '@/ndb';
import type { Env } from '@/prefix';
import { OfflinePage } from '@/views/offline';
import { PitrPage, untilValue } from '@/views/pitr';

import { connectionOf, type RouteDeps } from './context';

const DEFAULT_AGO_MS = 5 * 60_000;

// The PITR tab: the windows of GET /_pitr, a restore form (POST
// /{coll}/_restore) and a rename form (POST /{coll}/_rename) to put the copy
// in place. Both forms are native: they post here and come back with a
// notice or an error in the query string.
export function pitrRoutes(app: Hono<Env>, deps: RouteDeps): void {
  app.get('/c/:name/pitr', async c => {
    const connection = connectionOf(deps, c);

    if (!connection) return c.text('Not found', 404);

    const client = deps.client(connection);

    try {
      const [stats, pitr] = await Promise.all([client.stats(), client.pitr()]);
      const q = c.req.query();
      const selected = pitr.collections.find(item => item.collection === q.collection) ?? pitr.collections.find(item => !item.dropped);

      return c.html(
        <PitrPage
          current={deps.currentOf(connection, stats)}
          window={pitr.default}
          collections={pitr.collections}
          selected={q.collection}
          until={q.until ?? defaultUntil(selected?.oldest)}
          renameFrom={q.from}
          renameTo={q.to}
          notice={q.notice}
          error={q.error}
        />,
      );
    } catch (error) {
      return c.html(<OfflinePage current={deps.currentOf(connection)} message={deps.message(error)} />, 502);
    }
  });

  app.post('/c/:name/pitr/restore', async c => {
    const connection = connectionOf(deps, c);

    if (!connection) return c.text('Not found', 404);

    const form = await c.req.parseBody().catch(() => ({}) as Record<string, unknown>);
    const collection = String(form.collection ?? '');
    const into = String(form.into ?? '').trim();
    const until = String(form.until ?? '');
    const back = (params: Record<string, string>) => c.redirect(`${c.var.prefix}/c/${encodeURIComponent(connection.name)}/pitr?${new URLSearchParams(params)}`, 303);

    try {
      const res = await deps.client(connection).restore(collection, utc(until), into);

      return back({ notice: `${collection} as of ${until.replace('T', ' ')} UTC is in ${into} (${res.duration_ms} ms). Check it, then rename it in place.`, from: into, to: collection });
    } catch (error) {
      return back({ error: error instanceof NdbError ? error.message : 'The restore failed.', collection, until });
    }
  });

  app.post('/c/:name/pitr/rename', async c => {
    const connection = connectionOf(deps, c);

    if (!connection) return c.text('Not found', 404);

    const form = await c.req.parseBody().catch(() => ({}) as Record<string, unknown>);
    const from = String(form.from ?? '').trim();
    const to = String(form.to ?? '').trim();
    const replace = form.replace === '1' || form.replace === 'on';
    const back = (params: Record<string, string>) => c.redirect(`${c.var.prefix}/c/${encodeURIComponent(connection.name)}/pitr?${new URLSearchParams(params)}`, 303);

    try {
      await deps.client(connection).rename(from, to, replace);

      return back({ notice: replace ? `${from} is now ${to}. The old ${to} stays restorable for its window.` : `${from} is now ${to}.` });
    } catch (error) {
      return back({ error: error instanceof NdbError ? error.message : 'The rename failed.', from, to });
    }
  });
}

// defaultUntil is a few minutes ago, but not before the window starts.
export function defaultUntil(oldest?: string, now = Date.now()): string {
  const start = oldest ? Date.parse(oldest) + 1000 : 0;

  return untilValue(new Date(Math.min(now, Math.max(now - DEFAULT_AGO_MS, start))));
}

// utc reads the value of a datetime-local input as UTC.
export function utc(value: string): string {
  return /[zZ]|[+-]\d\d:\d\d$/.test(value) ? value : `${value.length === 16 ? `${value}:00` : value}Z`;
}
