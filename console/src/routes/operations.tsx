import type { Hono } from 'hono';

import { NdbError } from '@/ndb';
import { isOlderDaemon, LOG_LEVELS, type LogLevel, logSettings, metrics, OLDER_DAEMON, parseCount, saveLogSettings, clusterSettings } from '@/operations';
import type { Env } from '@/prefix';
import { OfflinePage } from '@/views/offline';
import { OperationsPage, type SettingsState } from '@/views/operations';

import { connectionOf, redirectTo, type RouteDeps } from './context';

// settingsState reads /_cluster/settings, or says why the page cannot show
// it: an older daemon (404), or a token with no admin of * (403).
async function settingsState(client: Parameters<typeof clusterSettings>[0]): Promise<SettingsState> {
  try {
    return { settings: await clusterSettings(client) };
  } catch (error) {
    if (isOlderDaemon(error)) return { older: true };

    if (error instanceof NdbError && error.status === 403) return { forbidden: true };

    return { error: error instanceof Error ? error.message : 'The request failed.' };
  }
}

export function operationsRoutes(app: Hono<Env>, deps: RouteDeps): void {
  app.get('/c/:name/operations', async c => {
    const connection = connectionOf(deps, c);

    if (!connection) return c.text('Not found', 404);

    const client = deps.client(connection);

    try {
      const [stats, log, counters, settings] = await Promise.all([
        client.stats(),
        logSettings(client).catch((error: unknown) => (error instanceof NdbError ? error : new NdbError('http_error', 'The request failed.'))),
        metrics(client),
        settingsState(client),
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
          settings={settings}
          notice={c.req.query('notice')}
          warnings={c.req.queries('warning') ?? []}
          error={c.req.query('error')}
        />,
      );
    } catch (error) {
      return c.html(<OfflinePage current={deps.currentOf(connection)} message={deps.message(error)} />, 502);
    }
  });

  // The log form: PUT /_cluster/settings on the daemon, so the change holds
  // across a restart, or PUT /_log on a daemon older than 1.21. The Origin
  // check of POST /c/* runs before it.
  app.post('/c/:name/operations/log', async c => {
    const connection = connectionOf(deps, c);

    if (!connection) return c.text('Not found', 404);

    const page = `c/${encodeURIComponent(connection.name)}/operations`;
    const back = (params: Record<string, string>, warnings: string[] = []) => {
      const query = new URLSearchParams(params);

      for (const warning of warnings) query.append('warning', warning);

      return redirectTo(c, `${page}?${query}`);
    };

    const form = await c.req.parseBody().catch(() => ({}) as Record<string, unknown>);

    const level = String(form.level ?? '');
    const bodyMax = parseCount(form.body_max);
    const slowMs = parseCount(form.slow_ms);

    if (!LOG_LEVELS.includes(level as LogLevel)) return back({ error: 'The level must be debug, info, warn, error or quiet.' });

    if (bodyMax === undefined || slowMs === undefined) return back({ error: 'body_max and slow_ms must be whole numbers of zero or more.' });

    try {
      const saved = await saveLogSettings(deps.client(connection), { level, body_max: bodyMax, slow_ms: slowMs });
      const values = `level ${saved.level}, body_max ${saved.body_max}, slow_ms ${saved.slow_ms}`;

      if (!saved.stored) return back({ notice: `Log settings applied: ${values}.` }, [OLDER_DAEMON]);

      return back({ notice: `Log settings saved: ${values}. They hold across a restart.` }, saved.warnings);
    } catch (error) {
      if (error instanceof NdbError && error.status === 403) return back({ error: 'This token has no admin of *. Use a token with admin of *, or the unix socket, to change the log settings.' });

      return back({ error: deps.message(error) });
    }
  });
}
