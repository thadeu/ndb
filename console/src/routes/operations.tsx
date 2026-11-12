import type { Hono } from 'hono';

import { NdbError } from '@/ndb';
import { LOG_LEVELS, type LogLevel, logSettings, metrics, parseCount, setLogSettings } from '@/operations';
import type { Env } from '@/prefix';
import { OfflinePage } from '@/views/offline';
import { OperationsPage } from '@/views/operations';

import { connectionOf, redirectTo, type RouteDeps } from './context';

export function operationsRoutes(app: Hono<Env>, deps: RouteDeps): void {
  app.get('/c/:name/operations', async c => {
    const connection = connectionOf(deps, c);

    if (!connection) return c.text('Not found', 404);

    const client = deps.client(connection);

    try {
      const [stats, log, counters] = await Promise.all([
        client.stats(),
        logSettings(client).catch((error: unknown) => (error instanceof NdbError ? error : new NdbError('http_error', 'The request failed.'))),
        metrics(client),
      ]);

      return c.html(
        <OperationsPage
          current={deps.currentOf(connection, stats)}
          unix={connection.url.startsWith('unix://')}
          hasToken={Boolean(connection.token)}
          log={log instanceof NdbError ? undefined : log}
          logError={log instanceof NdbError ? log.message : undefined}
          logForbidden={log instanceof NdbError && log.status === 403}
          metrics={counters}
          notice={c.req.query('notice')}
          error={c.req.query('error')}
        />,
      );
    } catch (error) {
      return c.html(<OfflinePage current={deps.currentOf(connection)} message={deps.message(error)} />, 502);
    }
  });

  // The log form: PUT /_log on the daemon. The Origin check of POST /c/* runs
  // before it.
  app.post('/c/:name/operations/log', async c => {
    const connection = connectionOf(deps, c);

    if (!connection) return c.text('Not found', 404);

    const page = `c/${encodeURIComponent(connection.name)}/operations`;
    const back = (params: Record<string, string>) => redirectTo(c, `${page}?${new URLSearchParams(params)}`);

    const form = await c.req.parseBody().catch(() => ({}) as Record<string, unknown>);

    const level = String(form.level ?? '');
    const bodyMax = parseCount(form.body_max);
    const slowMs = parseCount(form.slow_ms);

    if (!LOG_LEVELS.includes(level as LogLevel)) return back({ error: 'The level must be debug, info, warn, error or quiet.' });

    if (bodyMax === undefined || slowMs === undefined) return back({ error: 'body_max and slow_ms must be whole numbers of zero or more.' });

    try {
      const applied = await setLogSettings(deps.client(connection), { level, body_max: bodyMax, slow_ms: slowMs });

      return back({ notice: `Log settings applied: level ${applied.level}, body_max ${applied.body_max}, slow_ms ${applied.slow_ms}.` });
    } catch (error) {
      return back({ error: deps.message(error) });
    }
  });
}
