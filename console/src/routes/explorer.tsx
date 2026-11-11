import type { Hono } from 'hono';

import type { Env } from '@/prefix';
import { type ExplorerQuery, type ExplorerTab, ExplorerPage } from '@/views/explorer';
import { OfflinePage } from '@/views/offline';

import { loadDocuments } from './browser';

import { connectionOf, type RouteDeps } from './context';

const TABS: ExplorerTab[] = ['overview', 'documents', 'indexes', 'storage', 'backups'];

export function explorerRoutes(app: Hono<Env>, deps: RouteDeps): void {
  app.get('/c/:name/explorer', async c => {
    const connection = connectionOf(deps, c);

    if (!connection) return c.text('Not found', 404);

    const client = deps.client(connection);
    const q = c.req.query();

    const tab = TABS.includes(q.tab as ExplorerTab) ? (q.tab as ExplorerTab) : 'overview';
    const query: ExplorerQuery = { coll: q.coll, tab, find: q.find };

    try {
      const [stats, definitions, pitr] = await Promise.all([client.stats(), client.collections().catch(() => []), client.pitr().catch(() => undefined)]);

      const definition = definitions.find(item => item.name === query.coll);
      const window = pitr?.collections.find(item => item.collection === query.coll);
      const known = stats.collections.some(item => item.name === query.coll);
      const documents = tab === 'documents' && known && query.coll ? await loadDocuments(client, stats, query.coll, new URL(c.req.url).searchParams, deps.message) : undefined;

      const updated = new Date().toLocaleString('en-US', { dateStyle: 'medium', timeStyle: 'short' });

      return c.html(<ExplorerPage current={deps.currentOf(connection, stats)} stats={stats} query={query} definition={definition} pitr={window} documents={documents} updated={updated} />);
    } catch (error) {
      return c.html(<OfflinePage current={deps.currentOf(connection)} message={deps.message(error)} />, 502);
    }
  });
}
