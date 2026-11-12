import type { Context, Hono } from 'hono';

import {
  COLLECTION_NAME,
  compact,
  definitionBody,
  definitions as loadDefinitions,
  drop,
  dropIndex,
  dropNotice,
  dropRefusal,
  ensureIndex,
  explain as runExplain,
  fieldsError,
  isPattern,
  matchPattern,
  parseFields,
  putDefinition,
  quarantine as loadQuarantine,
  truncate,
} from '@/collections';
import type { RestoreDraft } from '@/components/restore-form';
import type { Connection } from '@/connections';
import { formatBytes } from '@/format';
import { type Ndb, NdbError } from '@/ndb';
import type { Env } from '@/prefix';
import { type ExplainResult, type ExplorerDialog, type ExplorerQuery, type ExplorerShow, type ExplorerTab, ExplorerPage } from '@/views/explorer';
import { OfflinePage } from '@/views/offline';

import { loadDocuments } from './browser';

import { connectionOf, type RouteDeps } from './context';

const TABS: ExplorerTab[] = ['overview', 'documents', 'indexes', 'storage', 'locks', 'backups'];
const SHOWS: ExplorerShow[] = ['all', 'favorites', 'streams'];
const DIALOGS: ExplorerDialog[] = ['new', 'edit', 'rename'];

type Form = Record<string, unknown>;

interface Action {
  connection: Connection;
  client: Ndb;
  form: Form;
  coll: string;
  tab: ExplorerTab;
}

function tabOf(value: unknown): ExplorerTab {
  return TABS.includes(value as ExplorerTab) ? (value as ExplorerTab) : 'overview';
}

function reason(error: unknown, fallback: string): string {
  return error instanceof NdbError ? error.message : fallback;
}

// explainFilter runs the filter of the Indexes tab. A bad filter is a
// message on the page, not an error page.
async function explainFilter(client: Ndb, coll: string, text: string): Promise<ExplainResult> {
  let filter: unknown;

  try {
    filter = JSON.parse(text);
  } catch {
    return { error: 'The filter is not valid JSON.' };
  }

  if (filter === null || typeof filter !== 'object' || Array.isArray(filter)) return { error: 'The filter must be a JSON object.' };

  try {
    return { result: await runExplain(client, coll, filter as Record<string, unknown>) };
  } catch (error) {
    return { error: reason(error, 'The search failed.') };
  }
}

export function explorerRoutes(app: Hono<Env>, deps: RouteDeps): void {
  app.get('/c/:name/explorer', async c => {
    const connection = connectionOf(deps, c);

    if (!connection) return c.text('Not found', 404);

    const client = deps.client(connection);
    const q = c.req.query();

    const tab = tabOf(q.tab);
    const show = SHOWS.includes(q.show as ExplorerShow) ? (q.show as ExplorerShow) : 'all';
    const dialog = DIALOGS.includes(q.dialog as ExplorerDialog) ? (q.dialog as ExplorerDialog) : undefined;
    const find = (q.find ?? '').trim();
    const query: ExplorerQuery = { coll: q.coll, tab, find: find || undefined, show, dialog, filter: q.filter };
    const restore: RestoreDraft = { at: q.at, where: q.where === 'place' ? 'place' : 'copy', to: q.to, confirm: q.confirm === '1' };

    try {
      const [stats, definitions, pitr] = await Promise.all([client.stats(), loadDefinitions(client).catch(() => []), client.pitr().catch(() => undefined)]);

      const window = pitr?.collections.find(item => item.collection === query.coll);
      const selected = stats.collections.find(item => item.name === query.coll);
      const coll = selected?.name;

      let matched: string[] | undefined;
      let findError: string | undefined;

      if (isPattern(find)) {
        try {
          matched = await matchPattern(client, find);
        } catch (error) {
          matched = [];
          findError = reason(error, 'The pattern failed.');
        }
      }

      const documents = tab === 'documents' && coll ? await loadDocuments(client, stats, coll, new URL(c.req.url).searchParams, deps.message) : undefined;
      const explain = tab === 'indexes' && coll && query.filter ? await explainFilter(client, coll, query.filter) : undefined;

      const bad = tab === 'storage' && coll && selected.write === 'direct' && selected.quarantined > 0;
      const quarantine = bad ? await loadQuarantine(client, coll).then(body => body.rows).catch(() => []) : undefined;

      const updated = new Date().toLocaleTimeString('en-US', { hour: 'numeric', minute: '2-digit' });

      return c.html(
        <ExplorerPage
          current={deps.currentOf(connection, stats)}
          stats={stats}
          definitions={definitions}
          query={query}
          matched={matched}
          findError={findError}
          pitr={window}
          documents={documents}
          explain={explain}
          quarantine={quarantine}
          updated={updated}
          restore={restore}
          restored={q.restored}
          notice={q.notice}
          error={q.error}
        />,
      );
    } catch (error) {
      return c.html(<OfflinePage current={deps.currentOf(connection)} message={deps.message(error)} />, 502);
    }
  });

  // action registers one write of the explorer page. The global csrf and
  // originGuard middlewares check the Origin of every POST before it runs.
  const action = (path: string, run: (c: Context<Env>, input: Action) => Promise<Response>) => {
    app.post(`/c/:name/explorer/${path}`, async c => {
      const connection = connectionOf(deps, c);

      if (!connection) return c.text('Not found', 404);

      const form = (await c.req.parseBody().catch(() => ({}))) as Form;
      const coll = String(form.coll ?? '').trim();

      return run(c, { connection, client: deps.client(connection), form, coll, tab: tabOf(form.tab) });
    });
  };

  action('create', async (c, { connection, client, form }) => {
    const name = String(form.collection ?? '').trim();

    if (!COLLECTION_NAME.test(name)) return backTo(c, connection.name, { dialog: 'new', error: 'A name has lower case letters, digits, _ and -, and starts with a letter or a digit.' });

    try {
      const known = await loadDefinitions(client);

      if (known.some(item => item.name === name)) return backTo(c, connection.name, { dialog: 'new', error: `The collection ${name} exists.` });

      await putDefinition(client, name, definitionBody(form));

      return backTo(c, connection.name, { coll: name, notice: `The collection ${name} is ready.` });
    } catch (error) {
      return backTo(c, connection.name, { dialog: 'new', error: reason(error, 'ndb did not create the collection.') });
    }
  });

  action('definition', async (c, { connection, client, form, coll, tab }) => {
    try {
      const { write: _write, path: _path, indexes: _indexes, unique: _unique, ...body } = definitionBody(form);

      await putDefinition(client, coll, body);

      return backTo(c, connection.name, { coll, tab, notice: 'The definition is saved.' });
    } catch (error) {
      return backTo(c, connection.name, { coll, tab, dialog: 'edit', error: reason(error, 'ndb did not save the definition.') });
    }
  });

  action('compact', async (c, { connection, client, coll, tab }) => {
    try {
      const report = await compact(client, coll);
      const notice = `Compacted ${coll} in ${report.duration_ms} ms: ${report.before.segments} segments (${formatBytes(report.before.bytes)}) to ${report.after.segments} (${formatBytes(report.after.bytes)}).`;

      return backTo(c, connection.name, { coll, tab, notice });
    } catch (error) {
      return backTo(c, connection.name, { coll, tab, error: reason(error, 'The compaction failed.') });
    }
  });

  action('truncate', async (c, { connection, client, coll, tab }) => {
    try {
      await truncate(client, coll);

      return backTo(c, connection.name, { coll, tab, notice: `${coll} is empty. The definition and the indexes stay.` });
    } catch (error) {
      return backTo(c, connection.name, { coll, tab, error: reason(error, 'The truncate failed.') });
    }
  });

  action('drop', async (c, { connection, client, coll, tab }) => {
    try {
      await drop(client, coll);

      return backTo(c, connection.name, { notice: `${coll} is dropped. It stays restorable for its PITR window.` });
    } catch (error) {
      return backTo(c, connection.name, { coll, tab, error: reason(error, 'The drop failed.') });
    }
  });

  action('rename', async (c, { connection, client, form, coll, tab }) => {
    const to = String(form.to ?? '').trim();
    const replace = form.replace === '1' || form.replace === 'on';

    try {
      await client.rename(coll, to, replace);

      return backTo(c, connection.name, { coll: to, tab, notice: `${coll} is now ${to}.` });
    } catch (error) {
      return backTo(c, connection.name, { coll, tab, dialog: 'rename', error: reason(error, 'The rename failed.') });
    }
  });

  action('indexes', async (c, { connection, client, form, coll }) => {
    const fields = parseFields(String(form.fields ?? ''));
    const unique = form.unique === '1' || form.unique === 'on';
    const invalid = fieldsError(fields);

    if (invalid) return backTo(c, connection.name, { coll, tab: 'indexes', error: invalid });

    try {
      await ensureIndex(client, coll, fields, unique);

      const name = fields.join(',');

      return backTo(c, connection.name, { coll, tab: 'indexes', notice: unique ? `The unique key ${name} is on.` : `The index ${name} is building.` });
    } catch (error) {
      return backTo(c, connection.name, { coll, tab: 'indexes', error: reason(error, 'ndb did not create the index.') });
    }
  });

  action('indexes/drop', async (c, { connection, client, form, coll }) => {
    const index = String(form.index ?? '').trim();

    try {
      const definition = (await loadDefinitions(client)).find(item => item.name === coll);
      const refusal = dropRefusal(definition, index);

      if (refusal) return backTo(c, connection.name, { coll, tab: 'indexes', error: refusal });

      await dropIndex(client, coll, index);

      return backTo(c, connection.name, { coll, tab: 'indexes', notice: dropNotice(definition, index) });
    } catch (error) {
      return backTo(c, connection.name, { coll, tab: 'indexes', error: reason(error, 'ndb did not drop the index.') });
    }
  });
}

function backTo(c: Context<Env>, name: string, params: Record<string, string>) {
  const kept = Object.entries(params).filter(([key, value]) => value !== '' && !(key === 'tab' && value === 'overview'));

  return c.redirect(`${c.var.prefix}/c/${encodeURIComponent(name)}/explorer?${new URLSearchParams(kept)}`, 303);
}
