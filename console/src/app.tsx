import { type Context, Hono } from 'hono';
import { contextStorage } from 'hono/context-storage';
import { csrf } from 'hono/csrf';
import { secureHeaders } from 'hono/secure-headers';

import { type Connection, Connections, NAME } from './connections';
import { type DiscoverOptions, DiscoverError, discover } from './discover';
import { formatUptime } from './format';
import { hostGuard, originGuard, parseAllowedHosts } from './guard';
import { Ndb, NdbError, type Stats, target } from './ndb';
import { clusterSettings, metrics } from './operations';
import { type Env, readPrefix } from './prefix';

import { explorerRoutes } from '@/routes/explorer';
import { aggregateRoutes } from '@/routes/aggregate';
import { backupsRoutes } from '@/routes/backups';
import { browserRoutes } from '@/routes/browser';
import { pageOrigin, sameOrigin } from '@/routes/context';
import { devtoolsRoutes } from '@/routes/devtools';
import { operationsRoutes } from '@/routes/operations';
import { streamsRoutes } from '@/routes/streams';
import { watchRoutes } from '@/routes/watch';

import { BOOT_SCRIPT, type Current } from '@/html/layout';
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
  icons?: Record<string, () => Response>;
  islands?: Record<string, () => Response>;
}

// The web app manifest: added to the home screen, the console opens full
// screen, like an app. Its URLs are relative to it, so a mount path works.
const MANIFEST = {
  name: 'Ndb Console',
  short_name: 'ndb',
  start_url: './',
  scope: './',
  display: 'standalone',
  background_color: '#ffffff',
  theme_color: '#ffffff',
  icons: [
    { src: 'assets/icon-192.png', sizes: '192x192', type: 'image/png' },
    { src: 'assets/icon-512.png', sizes: '512x512', type: 'image/png' },
  ],
};

export interface AppOptions {
  timeoutMs?: number;
  // Host names the console answers for, besides IP addresses and localhost.
  // Default: NDB_UI_ALLOWED_HOSTS.
  allowedHosts?: string[];
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
      const [, stats, definitions] = await Promise.all([ndb.health(), ndb.stats(), ndb.collections().catch(() => undefined)]);
      const streams = definitions?.filter(definition => (definition as { stream?: boolean }).stream).length;

      return { connection, online: true, stats, streams };
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
      saved: connections.list().map(item => ({ name: item.name, url: item.url })),
    };
  }

  const allowedHosts = options.allowedHosts ?? parseAllowedHosts(process.env.NDB_UI_ALLOWED_HOSTS);

  // DNS rebinding: refuse a Host that is not an IP, localhost, or allowed.
  app.use(hostGuard(allowedHosts));

  app.use(contextStorage());
  app.use(async (c, next) => {
    c.set('prefix', readPrefix(c.req.header('x-forwarded-prefix')));
    c.set('connections', connections);

    await next();
  });

  // Form posts must come from the console page itself. A mount in another
  // app runs with its session, so a form on another site must not reach it.
  app.use(csrf({ origin: (origin, c) => origin === pageOrigin(c) }));

  // Any write (POST, PUT, PATCH, DELETE) with the Origin of another site is
  // refused, whatever the content type and the route.
  app.use(originGuard());

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
  app.get('/assets/boot.js', c => c.body(BOOT_SCRIPT, 200, { 'content-type': 'text/javascript; charset=utf-8', 'cache-control': 'no-cache' }));
  app.get('/assets/favicon.svg', () => assets.favicon());
  app.get('/assets/:file{[a-z-]+\\.js}', c => {
    const render = assets.islands?.[c.req.param('file').replace(/\.js$/, '')];

    return render ? render() : c.text('Not found', 404);
  });
  app.get('/assets/:file{[a-z0-9-]+\\.png}', c => {
    const render = assets.icons?.[c.req.param('file')];

    return render ? render() : c.text('Not found', 404);
  });
  app.get('/manifest.webmanifest', c => c.body(JSON.stringify(MANIFEST), 200, { 'content-type': 'application/manifest+json' }));
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
      const [stats, size, definitions, counters, pitr, settings] = await Promise.all([
        ndb.stats(),
        ndb.size().catch(() => undefined),
        ndb.collections().catch(() => []),
        metrics(ndb),
        ndb.pitr().catch(() => undefined),
        clusterSettings(ndb).catch(() => undefined),
      ]);

      const updated = new Date().toLocaleTimeString('en-US', { hour: 'numeric', minute: '2-digit' });
      const tcpNoToken = !connection.url.startsWith('unix://') && !connection.token;

      return c.html(
        <OverviewPage
          current={currentOf(connection, stats)}
          stats={stats}
          size={size}
          definitions={definitions}
          metrics={counters}
          pitrWindow={pitr?.default}
          updated={updated}
          tcpNoToken={tcpNoToken}
          keySource={settings?.encryption?.source}
        />,
      );
    } catch (error) {
      return c.html(<OfflinePage current={currentOf(connection)} message={message(error)} />, 502);
    }
  });

  const deps = { connections, client, currentOf, message };

  browserRoutes(app, deps);
  devtoolsRoutes(app, deps);
  watchRoutes(app, deps);
  backupsRoutes(app, deps);
  explorerRoutes(app, deps);
  aggregateRoutes(app, deps);
  streamsRoutes(app, deps);
  operationsRoutes(app, deps);

  return app;
}
