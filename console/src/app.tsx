import { type Context, Hono } from 'hono';
import { contextStorage } from 'hono/context-storage';
import { csrf } from 'hono/csrf';
import { secureHeaders } from 'hono/secure-headers';

import { type Connection, Connections, NAME } from './connections';
import { type DiscoverOptions, DiscoverError, discover } from './discover';
import { formatUptime } from './format';
import { Ndb, NdbError, type Stats, target } from './ndb';
import { type Env, readPrefix } from './prefix';

import { backupsRoutes } from '@/routes/backups';
import { browserRoutes } from '@/routes/browser';
import { pageOrigin, sameOrigin } from '@/routes/context';
import { devtoolsRoutes } from '@/routes/devtools';
import { watchRoutes } from '@/routes/watch';

import type { Current } from '@/html/layout';
import { suggestName } from '@/components/discover';
import { ConnectionsPage, type ConnectionStatus } from '@/views/connections';
import { OfflinePage } from '@/views/offline';
import { OverviewPage } from '@/views/overview';
import { UiPage } from '@/views/ui';

export interface Assets {
  css: () => Response;
  libCss: () => Response;
  ui: () => Response;
  favicon: () => Response;
  islands?: Record<string, () => Response>;
}

export interface AppOptions {
  timeoutMs?: number;
  discover?: DiscoverOptions;
}

function message(error: unknown): string {
  return error instanceof NdbError ? error.message : 'The request failed.';
}

// createApp builds the routes. The browser talks only to this server, which
// adds the ndb token.
export function createApp(connections: Connections, assets: Assets, options: AppOptions = {}): Hono<Env> {
  const timeoutMs = options.timeoutMs ?? 2500;
  const app = new Hono<Env>();

  const client = (connection: Connection) => new Ndb(connection, timeoutMs);

  async function status(connection: Connection): Promise<ConnectionStatus> {
    try {
      const ndb = client(connection);
      const [, stats] = await Promise.all([ndb.health(), ndb.stats()]);

      return { connection, online: true, stats };
    } catch (error) {
      return { connection, online: false, error: message(error) };
    }
  }

  function currentOf(connection: Connection, stats?: Stats): Current {
    return {
      name: connection.name,
      url: connection.url,
      online: stats !== undefined,
      version: stats?.version,
      uptime: stats ? formatUptime(stats.uptime_seconds) : undefined,
    };
  }

  app.use(contextStorage());
  app.use(async (c, next) => {
    c.set('prefix', readPrefix(c.req.header('x-forwarded-prefix')));
    c.set('connections', connections);

    await next();
  });

  // Form posts must come from the console page itself. A mount in another
  // app runs with its session, so a form on another site must not reach it.
  app.use(csrf({ origin: (origin, c) => origin === pageOrigin(c) }));

  // A POST to a tab route is a fetch or a form of the console page. Its
  // Origin must be the page itself, whatever the content type.
  app.post('/c/*', async (c, next) => {
    if (!sameOrigin(c)) return c.text('Forbidden', 403);

    await next();
  });

  app.use(secureHeaders({ referrerPolicy: 'same-origin', contentSecurityPolicy: { defaultSrc: ["'self'"], styleSrc: ["'self'", "'unsafe-inline'", 'https://fonts.googleapis.com'], fontSrc: ['https://fonts.gstatic.com'], formAction: ["'self'"], scriptSrc: ["'self'"] } }));

  app.get('/healthz', c => c.json({ status: 'ok' }));
  app.get('/assets/app.css', () => assets.css());
  app.get('/assets/date-picker.css', () => assets.libCss());
  app.get('/assets/ui.js', () => assets.ui());
  app.get('/assets/favicon.svg', () => assets.favicon());
  app.get('/assets/:file{[a-z-]+\\.js}', c => {
    const render = assets.islands?.[c.req.param('file').replace(/\.js$/, '')];

    return render ? render() : c.text('Not found', 404);
  });
  app.get('/favicon.ico', c => c.redirect(`${c.var.prefix}/assets/favicon.svg`, 301));
  app.get('/ui', c => c.html(<UiPage />));

  app.get('/', async c => {
    const items = await Promise.all(connections.list().map(status));
    const tested = c.req.query('tested');
    const editing = c.req.query('edit');

    return c.html(
      <ConnectionsPage
        items={items}
        fixed={connections.fixed}
        notice={c.req.query('notice')}
        error={c.req.query('error')}
        adding={!editing && (c.req.query('new') === '1' || Boolean(tested) || Boolean(c.req.query('error')))}
        editing={editing}
        tested={tested ? { ok: tested === 'ok', message: c.req.query('msg') ?? '' } : undefined}
      />,
    );
  });

  // readForm reads the connection form. In the edit form, `current` names the
  // saved connection: an empty token keeps its token, unless `clear_token`.
  async function readForm(c: Context<Env>): Promise<Connection & { current?: string }> {
    const form = await c.req.parseBody().catch(() => ({}) as Record<string, unknown>);

    const url = String(form.url ?? '').trim();
    const name = String(form.name ?? '').trim() || suggestName(url);

    const current = String(form.current ?? '') || undefined;
    const typed = String(form.token ?? '') || undefined;
    const kept = current && !form.clear_token ? connections.get(current)?.token : undefined;

    return { name, url, token: typed ?? kept, current };
  }

  function back(c: Context<Env>, params: Record<string, string>) {
    return c.redirect(`${c.var.prefix}/?${new URLSearchParams(params)}`, 303);
  }

  app.post('/connections', async c => {
    if (connections.fixed) return c.text('The connection is fixed by NDB_URL.', 403);

    const connection = await readForm(c);

    try {
      target(connection.url);
      connections.add(connection);
    } catch (error) {
      return back(c, { error: error instanceof Error ? error.message : 'Cannot save.' });
    }

    return c.redirect(`${c.var.prefix}/c/${encodeURIComponent(connection.name)}`, 303);
  });

  app.post('/connections/test', async c => {
    const connection = await readForm(c);
    const started = performance.now();

    let result: { ok: boolean; message: string };

    try {
      const stats = await client(connection).stats();

      result = { ok: true, message: `Connected · ndb ${stats.version} · ${Math.round(performance.now() - started)} ms` };
    } catch (error) {
      result = { ok: false, message: message(error) };
    }

    if ((c.req.header('accept') ?? '').includes('json')) return c.json(result);

    return back(c, { tested: result.ok ? 'ok' : 'bad', msg: result.message, ...(connection.current ? { edit: connection.current } : {}) });
  });

  app.post('/connections/discover', async c => {
    if (!sameOrigin(c)) return c.text('Forbidden', 403);

    if (connections.fixed) return c.text('The connection is fixed by NDB_URL.', 403);

    const body = (await c.req.json().catch(() => ({}))) as { host?: unknown; ports?: unknown };

    try {
      return c.json(await discover(String(body.host ?? ''), String(body.ports ?? ''), options.discover));
    } catch (error) {
      if (error instanceof DiscoverError) return c.json({ error: error.message }, 400);

      throw error;
    }
  });

  app.post('/connections/:name/edit', async c => {
    if (connections.fixed) return c.text('The connection is fixed by NDB_URL.', 403);

    const name = c.req.param('name');
    const { current: _current, ...connection } = await readForm(c);

    try {
      target(connection.url);
      connections.update(name, connection);
    } catch (error) {
      return back(c, { edit: name, error: error instanceof Error ? error.message : 'Cannot save.' });
    }

    return c.redirect(`${c.var.prefix}/c/${encodeURIComponent(connection.name)}`, 303);
  });

  app.post('/connections/:name/delete', c => {
    if (connections.fixed) return c.text('The connection is fixed by NDB_URL.', 403);

    connections.remove(c.req.param('name'));

    return back(c, { notice: 'Connection removed.' });
  });

  app.get('/c/:name', async c => {
    const connection = connections.get(c.req.param('name'));

    if (!connection || !NAME.test(connection.name)) return c.text('Not found', 404);

    const ndb = client(connection);

    try {
      const [stats, size, definitions] = await Promise.all([ndb.stats(), ndb.size().catch(() => undefined), ndb.collections().catch(() => [])]);

      return c.html(<OverviewPage current={currentOf(connection, stats)} stats={stats} size={size} definitions={definitions} />);
    } catch (error) {
      return c.html(<OfflinePage current={currentOf(connection)} message={message(error)} />, 502);
    }
  });

  const deps = { connections, client, currentOf, message };

  browserRoutes(app, deps);
  devtoolsRoutes(app, deps);
  watchRoutes(app, deps);
  backupsRoutes(app, deps);

  return app;
}
