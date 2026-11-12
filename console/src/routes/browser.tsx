import type { Context, Hono } from 'hono';

import { buildWhere, emptyRow, type FindQuery, LIMITS, readFind, searchBody } from '@/components/filter-model';
import { updateBody } from '@/components/json-model';
import { dropLock, heldLock, keepLock, lockKey } from '@/locks';
import { type Doc, type Ndb, NdbError, type SearchResult, type Stats, type UpdateResult } from '@/ndb';
import type { Env } from '@/prefix';
import { browserHref, type DocsLink, docsHref, type DocumentsProps } from '@/components/documents';
import { OfflinePage } from '@/views/offline';

import { connectionOf, redirectTo, type RouteDeps, wantsJson } from './context';

const DONE: Record<string, string> = { saved: 'Saved.', deleted: 'Deleted.', inserted: 'Inserted.', locked: 'Locked for 30 s.', unlocked: 'Unlocked.' };

const FAIL: Record<string, string> = {
  locked: 'Another caller holds the lock of this document.',
  lock: 'The lock failed.',
  delete: 'The delete failed.',
  some: 'Not all the selected documents were deleted.',
};

const LOCK_TTL = '30s';
const LOCK_ENDED = 'The lock ended before the write. Nothing was written. Try again, or lock again.';

// An export stops at this number of rows, and reads them in pages of this
// size, so a big collection never fills the memory of the console.
export const EXPORT_CAP = 10_000;
const EXPORT_PAGE = 1000;

const BULK_MAX = Math.max(...LIMITS);

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

// whereOf builds the filter of a find query, from the rows or from the JSON
// box.
function whereOf(find: FindQuery): WhereResult {
  return find.mode === 'json' ? readWhere(find.json) : buildWhere(find.rows);
}

// noticeOf is the text for the done code of a write that came back here.
function noticeOf(params: URLSearchParams): string | undefined {
  const count = Number(params.get('n'));

  if (params.get('done') === 'deleted' && count > 1) return `Deleted ${count} documents.`;

  return DONE[params.get('done') ?? ''];
}

// backHref is the Documents tab with the find query that a form sent in
// its find field, so a write comes back to the same list.
function backHref(name: string, coll: string, find: unknown, link: DocsLink): string {
  return docsHref(name, coll, readFind(new URLSearchParams(String(find ?? ''))), link);
}

function docPath(coll: string, id: string, route: string): string {
  return `/${encodeURIComponent(coll)}/${route}/${encodeURIComponent(id)}`;
}

function withLock(path: string, token: string | undefined): string {
  return token ? `${path}?lock=${encodeURIComponent(token)}` : path;
}

function lockEnded(error: unknown): boolean {
  return error instanceof NdbError && error.code === 'lock_expired';
}

async function readForm(c: Context<Env>, json: boolean): Promise<Record<string, unknown>> {
  const form = json ? await c.req.json().catch(() => ({})) : await c.req.parseBody().catch(() => ({}));

  return isObject(form) ? form : {};
}

function fileStamp(date: Date): string {
  return date.toISOString().slice(0, 19).replace(/[-:]/g, '').replace('T', '-');
}

// exportLines reads the matches page by page with the cursor and gives them
// as NDJSON lines, up to EXPORT_CAP rows. first is the first page, which the
// route already read to answer a bad query with an error.
function exportLines(ndb: Ndb, coll: string, body: Record<string, unknown>, first: SearchResult): ReadableStream<Uint8Array> {
  const encoder = new TextEncoder();

  let page: SearchResult | undefined = first;
  let sent = 0;

  return new ReadableStream<Uint8Array>({
    async pull(controller) {
      if (!page) {
        controller.close();

        return;
      }

      const rows = page.rows.slice(0, EXPORT_CAP - sent);
      const next = page.next;

      sent += rows.length;

      if (rows.length > 0) controller.enqueue(encoder.encode(rows.map(row => `${JSON.stringify(row)}\n`).join('')));

      if (!next || sent >= EXPORT_CAP) {
        controller.close();

        return;
      }

      try {
        page = await ndb.search(coll, { ...body, after: next });
      } catch (error) {
        controller.error(error);
      }
    },
  });
}

// loadDocuments runs the find query of the Documents tab and reads the open
// document. Add and remove of a filter row come from the same form. With no
// filter the form shows one empty row, ready to fill in. A bad
// filter or a missing _id becomes a message on the page, never an error page.
export async function loadDocuments(ndb: Ndb, stats: Stats, coll: string, params: URLSearchParams, message: (error: unknown) => string): Promise<Omit<DocumentsProps, 'current'>> {
  const find = readFind(params);
  const remove = params.get('remove');

  if (params.get('add')) find.rows.push(emptyRow());

  if (remove !== null) find.rows.splice(Number(remove), 1);

  if (find.rows.length === 0) find.rows.push(emptyRow());

  const built = whereOf(find);
  const where = built.where ?? {};
  const body = searchBody(find, where);
  const trail = find.per ? [] : params.getAll('after').filter(Boolean);
  const after = trail.at(-1);
  const id = params.get('id') || undefined;
  const known = stats.collections.some(item => item.name === coll);

  let found: SearchResult = { rows: [] };
  let whereError = built.error;

  if (known && !whereError) {
    try {
      found = await ndb.search(coll, { ...body, count: true, versions: true, ...(after ? { after } : {}) });
    } catch (error) {
      whereError = message(error);
    }
  }

  let selected: Doc | undefined;
  let selectedError: string | undefined;

  if (known && id) {
    try {
      selected = await ndb.document(coll, id);
    } catch (error) {
      selectedError = error instanceof NdbError && error.code === 'not_found' ? `No document with _id ${id}.` : message(error);
    }
  }

  return {
    collections: stats.collections,
    coll,
    find,
    body,
    whereError,
    rows: found.rows,
    plan: found.plan,
    examined: found.examined,
    total: found.total,
    next: found.next,
    trail,
    id,
    selected,
    selectedError,
    notice: noticeOf(params),
    problem: FAIL[params.get('fail') ?? ''],
    insertOpen: params.get('insert') === '1',
    insertError: params.get('err') || undefined,
    findOpen: params.has('add') || remove !== null,
  };
}

export function browserRoutes(app: Hono<Env>, deps: RouteDeps): void {
  // The old Browser page is now the Documents tab of the Explorer. Old links
  // go there, with the first collection when none is chosen.
  app.get('/c/:name/browser', async c => {
    const connection = connectionOf(deps, c);

    if (!connection) return c.text('Not found', 404);

    let coll = c.req.query('coll') ?? '';

    if (coll === '') {
      try {
        coll = (await deps.client(connection).stats()).collections[0]?.name ?? '';
      } catch (error) {
        return c.html(<OfflinePage current={deps.currentOf(connection)} message={deps.message(error)} />, 502);
      }
    }

    const location = browserHref(connection.name, { coll, where: c.req.query('where'), id: c.req.query('id'), after: c.req.query('after') });

    return c.redirect(`${c.var.prefix}/${location}`, 302);
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

    const key = lockKey(connection.url, body.coll, body.id);
    const path = withLock(docPath(body.coll, body.id, '_update'), heldLock(key)?.token);

    try {
      const result = await deps.client(connection).call<UpdateResult>('POST', path, operators);

      if (result.matched === 0) return c.json({ conflict: true, message: 'Somebody else changed this document; reload.' }, 409);

      return c.json({ ok: true, doc: result.doc });
    } catch (error) {
      if (!lockEnded(error)) return c.json({ message: deps.message(error) }, 400);

      dropLock(key);

      return c.json({ lockEnded: true, message: LOCK_ENDED }, 409);
    }
  });

  // The lock route takes the pessimistic lock of one document for 30 s. The
  // token stays in this server; the answer has only the time of the end.
  app.post('/c/:name/browser/lock', async c => {
    const connection = connectionOf(deps, c);

    if (!connection) return c.text('Not found', 404);

    const json = wantsJson(c);
    const form = await readForm(c, json);
    const coll = String(form.coll ?? '');
    const id = String(form.id ?? '');
    const key = lockKey(connection.url, coll, id);
    const mine = heldLock(key);

    if (mine) return json ? c.json({ ok: true, until: mine.until }) : redirectTo(c, backHref(connection.name, coll, form.find, { id }));

    try {
      const answer = await deps.client(connection).call<{ token: string; expires_at: string; doc: Doc }>('POST', docPath(coll, id, '_lock'), { ttl: LOCK_TTL, wait: 0 });

      keepLock(key, { token: answer.token, until: answer.expires_at });

      return json ? c.json({ ok: true, until: answer.expires_at, _v: answer.doc._v }) : redirectTo(c, backHref(connection.name, coll, form.find, { id, done: 'locked' }));
    } catch (error) {
      const taken = error instanceof NdbError && error.code === 'locked';

      if (json) return c.json({ message: taken ? FAIL.locked : deps.message(error) }, taken ? 423 : 400);

      return redirectTo(c, backHref(connection.name, coll, form.find, { id, fail: taken ? 'locked' : 'lock' }));
    }
  });

  // Unlock releases the lock that this console holds. A lock that already
  // ended is not an error: the document is not locked, as asked.
  app.post('/c/:name/browser/unlock', async c => {
    const connection = connectionOf(deps, c);

    if (!connection) return c.text('Not found', 404);

    const json = wantsJson(c);
    const form = await readForm(c, json);
    const coll = String(form.coll ?? '');
    const id = String(form.id ?? '');
    const key = lockKey(connection.url, coll, id);
    const mine = heldLock(key);

    dropLock(key);

    if (mine) await deps.client(connection).call('POST', docPath(coll, id, '_unlock'), { token: mine.token }).catch(() => undefined);

    return json ? c.json({ ok: true }) : redirectTo(c, backHref(connection.name, coll, form.find, { id, done: 'unlocked' }));
  });

  // Export sends the matches of the find query as an NDJSON file. It reads
  // ndb page by page, so nothing but one page is in memory here.
  app.get('/c/:name/browser/export', async c => {
    const connection = connectionOf(deps, c);

    if (!connection) return c.text('Not found', 404);

    const params = new URL(c.req.url).searchParams;
    const coll = params.get('coll') ?? '';
    const find = readFind(params);
    const built = whereOf(find);

    if (built.error) return c.text(built.error, 400);

    const ndb = deps.client(connection);
    const query = searchBody(find, built.where ?? {});
    const body = find.per ? query : { ...query, limit: EXPORT_PAGE };

    let first: SearchResult;

    try {
      first = await ndb.search(coll, body);
    } catch (error) {
      return c.text(deps.message(error), error instanceof NdbError && error.status >= 400 && error.status < 500 ? 400 : 502);
    }

    const filename = `${coll.replace(/[^A-Za-z0-9._-]/g, '_')}-${fileStamp(new Date())}.ndjson`;

    return new Response(exportLines(ndb, coll, body, first), { headers: { 'content-type': 'application/x-ndjson', 'content-disposition': `attachment; filename="${filename}"`, 'cache-control': 'no-store' } });
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

    const form = await readForm(c, false);
    const coll = String(form.coll ?? '');
    const id = String(form.id ?? '');
    const key = lockKey(connection.url, coll, id);

    try {
      await deps.client(connection).call('DELETE', withLock(docPath(coll, id, '_doc'), heldLock(key)?.token));
    } catch (error) {
      if (lockEnded(error)) dropLock(key);

      return redirectTo(c, backHref(connection.name, coll, form.find, { id, fail: 'delete' }));
    }

    dropLock(key);

    return redirectTo(c, backHref(connection.name, coll, form.find, { done: 'deleted' }));
  });

  // The bulk delete takes the ids of the checked rows of one page. Each
  // document is one delete, with the lock token when this console holds its
  // lock. It goes on after a failure and reports it.
  app.post('/c/:name/browser/delete-many', async c => {
    const connection = connectionOf(deps, c);

    if (!connection) return c.text('Not found', 404);

    const form = await c.req.parseBody({ all: true }).catch(() => ({}) as Record<string, unknown>);
    const coll = String(form.coll ?? '');
    const picked = form.ids === undefined ? [] : [form.ids].flat();
    const ids = [...new Set(picked.map(String).filter(Boolean))].slice(0, BULK_MAX);
    const ndb = deps.client(connection);

    let deleted = 0;
    let failed = false;

    for (const id of ids) {
      const key = lockKey(connection.url, coll, id);

      try {
        const answer = await ndb.call<{ deleted: number }>('DELETE', withLock(docPath(coll, id, '_doc'), heldLock(key)?.token));

        deleted += answer.deleted;
      } catch (error) {
        failed = failed || !(error instanceof NdbError && error.code === 'not_found');
      }

      dropLock(key);
    }

    const link: DocsLink = { fail: failed ? 'some' : undefined, ...(deleted > 0 ? { done: 'deleted', count: deleted } : {}) };

    return redirectTo(c, backHref(connection.name, coll, form.find, link));
  });
}
