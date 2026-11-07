import type { Hono } from 'hono';

import { updateBody } from '@/components/json-model';
import { NdbError, type Doc, type SearchResult } from '@/ndb';
import type { Env } from '@/prefix';
import { BrowserPage, browserHref } from '@/views/browser';
import { OfflinePage } from '@/views/offline';

import { connectionOf, redirectTo, type RouteDeps, wantsJson } from './context';

const PAGE = 25;
const DONE: Record<string, string> = { saved: 'Saved.', deleted: 'Deleted.', inserted: 'Inserted.' };

interface WhereResult {
  where?: Record<string, unknown>;
  error?: string;
}

// readWhere parses the where box. An empty box matches every document. A bad
// filter becomes a message on the page, never an error page.
export function readWhere(text: string): WhereResult {
  if (text.trim() === '') return { where: {} };

  try {
    const value = JSON.parse(text);

    if (value === null || typeof value !== 'object' || Array.isArray(value)) return { error: 'The filter must be a JSON object, such as {"status":"new"}.' };

    return { where: value };
  } catch (error) {
    return { error: `The filter is not valid JSON: ${error instanceof Error ? error.message : 'parse error'}.` };
  }
}

function isObject(value: unknown): value is Record<string, unknown> {
  return value !== null && typeof value === 'object' && !Array.isArray(value);
}

export function browserRoutes(app: Hono<Env>, deps: RouteDeps): void {
  app.get('/c/:name/browser', async c => {
    const connection = connectionOf(deps, c);

    if (!connection) return c.text('Not found', 404);

    const ndb = deps.client(connection);

    let stats;

    try {
      stats = await ndb.stats();
    } catch (error) {
      return c.html(<OfflinePage current={deps.currentOf(connection)} message={deps.message(error)} />, 502);
    }

    const coll = c.req.query('coll') ?? '';
    const first = stats.collections[0];

    if (coll === '' && first) return c.redirect(`${c.var.prefix}/${browserHref(connection.name, { coll: first.name })}`, 302);

    const query = { coll, where: c.req.query('where') ?? '', id: c.req.query('id') || undefined, after: c.req.query('after') || undefined, q: c.req.query('q') || undefined };
    const known = stats.collections.some(item => item.name === coll);
    const parsed = readWhere(query.where);

    let found: SearchResult = { rows: [] };
    let whereError = parsed.error;

    if (known && parsed.where) {
      try {
        found = await ndb.search(coll, { query: parsed.where, limit: PAGE, count: true, versions: true, ...(query.after ? { after: query.after } : {}) });
      } catch (error) {
        whereError = deps.message(error);
      }
    }

    const selectedId = query.id ?? (found.rows[0] ? String(found.rows[0]._id) : undefined);

    let selected: Doc | undefined;
    let selectedError: string | undefined;

    if (known && selectedId) {
      try {
        selected = await ndb.document(coll, selectedId);
      } catch (error) {
        selectedError = error instanceof NdbError && error.code === 'not_found' ? `No document with _id ${selectedId}.` : deps.message(error);
      }
    }

    return c.html(
      <BrowserPage
        current={deps.currentOf(connection, stats)}
        collections={stats.collections}
        query={{ ...query, id: selectedId }}
        rows={found.rows}
        plan={found.plan}
        examined={found.examined}
        total={found.total}
        next={found.next}
        whereError={whereError}
        selected={selected}
        selectedError={selectedError}
        notice={DONE[c.req.query('done') ?? '']}
        insertOpen={c.req.query('insert') === '1'}
        insertError={c.req.query('err') || undefined}
      />,
    );
  });

  // The save route takes the diff that the island computed and writes it with
  // the _v guard. matched 0 means somebody else wrote first.
  app.post('/c/:name/browser/save', async c => {
    const connection = connectionOf(deps, c);

    if (!connection) return c.json({ message: 'Not found.' }, 404);

    const body = await c.req.json().catch(() => null);

    if (!isObject(body) || typeof body.coll !== 'string' || typeof body.id !== 'string' || typeof body._v !== 'number' || !isObject(body.set) || !Array.isArray(body.unset)) {
      return c.json({ message: 'The save request is not valid.' }, 400);
    }

    const unset = body.unset.filter((item): item is string => typeof item === 'string');
    const operators = updateBody(body._v, { set: body.set, unset, blocked: [] });

    if (Object.keys(operators).length === 1) return c.json({ message: 'Nothing changed.' }, 400);

    try {
      const result = await deps.client(connection).update(body.coll, body.id, operators);

      if (result.matched === 0) return c.json({ conflict: true, message: 'Somebody else changed this document; reload.' }, 409);

      return c.json({ ok: true, doc: result.doc });
    } catch (error) {
      return c.json({ message: deps.message(error) }, 400);
    }
  });

  app.post('/c/:name/browser/insert', async c => {
    const connection = connectionOf(deps, c);

    if (!connection) return c.text('Not found', 404);

    const json = wantsJson(c);
    const form = json ? await c.req.json().catch(() => ({})) : await c.req.parseBody().catch(() => ({}));
    const coll = String(form.coll ?? '');

    const fail = (message: string) => {
      if (json) return c.json({ message }, 400);

      return c.redirect(`${c.var.prefix}/${browserHref(connection.name, { coll, insert: true })}&${new URLSearchParams({ err: message })}`, 303);
    };

    let doc: unknown;

    try {
      doc = JSON.parse(String(form.doc ?? ''));
    } catch (error) {
      return fail(`The document is not valid JSON: ${error instanceof Error ? error.message : 'parse error'}.`);
    }

    if (!isObject(doc)) return fail('The document must be a JSON object.');

    try {
      const result = await deps.client(connection).insert(coll, [doc]);
      const id = result.results[0]?._id;
      const location = browserHref(connection.name, { coll, id, done: 'inserted' });

      return json ? c.json({ ok: true, location }) : redirectTo(c, location);
    } catch (error) {
      return fail(deps.message(error));
    }
  });

  app.post('/c/:name/browser/delete', async c => {
    const connection = connectionOf(deps, c);

    if (!connection) return c.text('Not found', 404);

    const form = await c.req.parseBody().catch(() => ({}) as Record<string, unknown>);
    const coll = String(form.coll ?? '');
    const id = String(form.id ?? '');

    try {
      await deps.client(connection).remove(coll, id);
    } catch (error) {
      return redirectTo(c, browserHref(connection.name, { coll, id }));
    }

    return redirectTo(c, browserHref(connection.name, { coll, done: 'deleted' }));
  });
}
