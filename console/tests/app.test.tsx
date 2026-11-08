import { afterAll, beforeAll, describe, expect, it } from 'bun:test';
import { mkdirSync, mkdtempSync, rmSync, statSync, writeFileSync } from 'node:fs';
import { tmpdir } from 'node:os';
import { join } from 'node:path';

import { createApp } from '@/app';
import { Connections } from '@/connections';
import { Ndb, NdbError } from '@/ndb';

const BIN = process.env.NDB_BIN ?? '/tmp/ndb-console-test/ndb';
const dir = mkdtempSync(join(tmpdir(), 'nc-'));
const sock = join(dir, 'n.sock');
const url = `unix://${sock}`;

let daemon: ReturnType<typeof Bun.spawn> | undefined;

const store = new Connections(join(dir, 'cfg', 'console.json'));
const app = createApp(store, { css: () => new Response('body{}'), libCss: () => new Response('.datepicker{}'), ui: () => new Response('//'), favicon: () => new Response('<svg/>') });

function page(path: string, init?: RequestInit): Promise<Response> {
  return Promise.resolve(app.fetch(new Request(`http://ui${path}`, init)));
}

function form(values: Record<string, string>, headers: Record<string, string> = {}): RequestInit {
  return { method: 'POST', body: new URLSearchParams(values), headers: { 'content-type': 'application/x-www-form-urlencoded', origin: 'http://ui', ...headers } };
}

beforeAll(async () => {
  mkdirSync(join(dir, 'data'));
  writeFileSync(join(dir, 'ndb.yml'), `data_dir: ${join(dir, 'data')}\nlisten:\n  unix: ${sock}\n`);

  daemon = Bun.spawn([BIN, '-config', join(dir, 'ndb.yml')], { stdout: 'ignore', stderr: 'ignore' });

  for (let i = 0; i < 200; i++) {
    const ready = await new Ndb({ url }).health().then(() => true).catch(() => false);

    if (ready) break;

    await Bun.sleep(25);
  }

  const insert = await fetch('http://ndb/orders/_bulk', {
    method: 'POST',
    body: JSON.stringify({ docs: [{ _id: 'a', status: 'new' }, { _id: 'b', status: 'paid' }] }),
    unix: sock,
  } as RequestInit);

  if (!insert.ok) throw new Error(`insert failed: ${await insert.text()}`);

  store.add({ name: 'local', url });
});

afterAll(() => {
  daemon?.kill();
  rmSync(dir, { recursive: true, force: true });
});

describe('ndb client', () => {
  it('reads health, stats, size, and collections over a unix socket', async () => {
    const ndb = new Ndb({ url });

    expect((await ndb.health()).status).toBe('ok');
    expect((await ndb.stats()).totals.rows).toBe(2);
    expect((await ndb.size()).data_dir).toContain('data');
    expect((await ndb.collections()).map(def => def.name)).toContain('orders');
  });

  it('turns an ndb error body into a typed error', async () => {
    const error = await new Ndb({ url }).collection('nope').catch(e => e);

    expect(error).toBeInstanceOf(NdbError);
    expect(error.code).toBe('unknown_collection');
    expect(error.status).toBe(404);
  });

  it('reports an unreachable socket', async () => {
    const error = await new Ndb({ url: 'unix:///tmp/nc-missing.sock' }).health().catch(e => e);

    expect(error.code).toBe('unreachable');
  });
});

describe('connections store', () => {
  it('writes the file with mode 0600', () => {
    expect(statSync(join(dir, 'cfg', 'console.json')).mode & 0o777).toBe(0o600);
  });

  it('uses the fixed connection and blocks changes', () => {
    const fixed = new Connections(join(dir, 'none.json'), { name: 'default', url: 'tcp://x:1', token: 't' });

    expect(fixed.list()).toHaveLength(1);
    expect(() => fixed.add({ name: 'b', url: 'tcp://y:2' })).toThrow();
  });
});

describe('pages', () => {
  it('shows an online connection card', async () => {
    const html = await (await page('/')).text();

    expect(html).toContain('online');
    expect(html).toContain('1 collections');
    expect(html).toContain('2 rows');
  });

  it('never renders the token', async () => {
    store.add({ name: 'secret', url, token: 'hunter2-token' });

    const html = await (await page('/')).text();

    expect(html).toContain('token set');
    expect(html).not.toContain('hunter2-token');
  });

  it('renders the collection rows in the overview', async () => {
    const html = await (await page('/c/local')).text();

    expect(html).toContain('orders');
    expect(html).toContain('engine');
    expect(html).toContain('Disk');
  });

  it('tests a connection and answers json', async () => {
    const response = await page('/connections/test', form({ name: 'x', url }, { accept: 'application/json' }));
    const body = await response.json();

    expect(body.ok).toBe(true);
  });

  it('rejects a post without a matching Origin', async () => {
    const wrong = await page('/connections', form({ name: 'evil', url }, { origin: 'http://evil.example' }));
    const missing = await page('/connections', { method: 'POST', body: new URLSearchParams({ name: 'evil', url }) });

    expect(wrong.status).toBe(403);
    expect(missing.status).toBe(403);
    expect(store.get('evil')).toBeUndefined();
  });

  it('saves and removes a connection', async () => {
    const saved = await page('/connections', form({ name: 'extra', url }));

    expect(saved.status).toBe(303);
    expect(store.get('extra')).toBeDefined();

    await page('/connections/extra/delete', form({}));

    expect(store.get('extra')).toBeUndefined();
  });

  it('edits a connection and keeps its token when the field is empty', async () => {
    store.add({ name: 'edit-me', url, token: 'kept-token' });

    const html = await (await page('/?edit=edit-me')).text();

    expect(html).toContain('Edit edit-me');
    expect(html).not.toContain('kept-token');

    const saved = await page('/connections/edit-me/edit', form({ current: 'edit-me', name: 'edited', url, token: '' }));

    expect(saved.status).toBe(303);
    expect(store.get('edit-me')).toBeUndefined();
    expect(store.get('edited')).toEqual({ name: 'edited', url, token: 'kept-token' });

    await page('/connections/edited/edit', form({ current: 'edited', name: 'edited', url, clear_token: '1' }));

    expect(store.get('edited')?.token).toBeUndefined();

    await page('/connections/edited/delete', form({}));
  });

  it('refuses to rename a connection to a name in use', async () => {
    store.add({ name: 'one', url });
    store.add({ name: 'two', url });

    const response = await page('/connections/one/edit', form({ current: 'one', name: 'two', url }));

    expect(response.headers.get('location')).toContain('edit=one');
    expect(store.get('one')).toBeDefined();

    store.remove('one');
    store.remove('two');
  });

  it('uses the mount prefix in the base tag', async () => {
    const html = await (await page('/', { headers: { 'x-forwarded-prefix': '/ndb' } })).text();

    expect(html).toContain('<base href="/ndb/"');
  });
});

function json(body: unknown, headers: Record<string, string> = {}): RequestInit {
  return { method: 'POST', body: JSON.stringify(body), headers: { 'content-type': 'application/json', origin: 'http://ui', ...headers } };
}

async function ndbPost(path: string, body: unknown): Promise<Response> {
  return fetch(`http://ndb${path}`, { method: 'POST', body: JSON.stringify(body), unix: sock } as RequestInit);
}

describe('browser tab', () => {
  it('lists the documents with _v and shows the selected one as a tree', async () => {
    const html = await (await page('/c/local/browser?coll=orders')).text();

    expect(html).toContain('docrow');
    expect(html).toContain('_v 1');
    expect(html).toContain('2 matches');
    expect(html).toContain('id="doc-editor"');
    expect(html).toContain('class="tkey">status');
    expect(html.indexOf('>_id<')).toBeLessThan(html.indexOf('>_v<'));
  });

  it('redirects to the first collection when none is chosen', async () => {
    const response = await page('/c/local/browser');

    expect(response.status).toBe(302);
    expect(response.headers.get('location')).toContain('coll=orders');
  });

  it('shows an invalid filter inline and does not crash', async () => {
    const response = await page(`/c/local/browser?coll=orders&where=${encodeURIComponent('{oops')}`);
    const html = await response.text();

    expect(response.status).toBe(200);
    expect(html).toContain('not valid JSON');
  });

  it('filters with where', async () => {
    const html = await (await page(`/c/local/browser?coll=orders&where=${encodeURIComponent('{"status":"paid"}')}`)).text();

    expect(html).toContain('1 match');
    expect(html).toContain('id=b');
    expect(html).not.toContain('id=a"');
  });

  it('saves a diff, and a stale _v is a conflict', async () => {
    const read = await ndbGet('/orders/_doc/a');
    const ok = await page('/c/local/browser/save', json({ coll: 'orders', id: 'a', _v: read._v, set: { status: 'shipped', 'customer.tier': 'gold' }, unset: [] }));

    expect(ok.status).toBe(200);
    expect((await ndbGet('/orders/_doc/a')).customer.tier).toBe('gold');

    const stale = await page('/c/local/browser/save', json({ coll: 'orders', id: 'a', _v: read._v, set: { status: 'late' }, unset: [] }));
    const body = await stale.json();

    expect(stale.status).toBe(409);
    expect(body.conflict).toBe(true);
    expect((await ndbGet('/orders/_doc/a')).status).toBe('shipped');
  });

  it('unsets a field', async () => {
    const read = await ndbGet('/orders/_doc/a');

    await page('/c/local/browser/save', json({ coll: 'orders', id: 'a', _v: read._v, set: {}, unset: ['customer'] }));

    expect((await ndbGet('/orders/_doc/a')).customer).toBeUndefined();
  });

  it('inserts from json and from a form, then deletes', async () => {
    const made = await (await page('/c/local/browser/insert', json({ coll: 'orders', doc: '{"_id":"n1","status":"new"}' }))).json();

    expect(made.location).toContain('id=n1');

    const bad = await page('/c/local/browser/insert', json({ coll: 'orders', doc: '{nope' }));

    expect(bad.status).toBe(400);

    const removed = await page('/c/local/browser/delete', form({ coll: 'orders', id: 'n1' }));

    expect(removed.status).toBe(303);
    expect((await fetch('http://ndb/orders/_doc/n1', { unix: sock } as RequestInit)).status).toBe(404);
  });

  it('refuses a save without the right Origin', async () => {
    const response = await page('/c/local/browser/save', json({ coll: 'orders', id: 'a', _v: 1, set: {}, unset: [] }, { origin: 'http://evil.example' }));

    expect(response.status).toBe(403);
  });
});

async function ndbGet(path: string): Promise<Record<string, any>> {
  return (await fetch(`http://ndb${path}`, { unix: sock } as RequestInit)).json() as Promise<Record<string, any>>;
}

describe('dev tools tab', () => {
  it('renders the page', async () => {
    const html = await (await page('/c/local/devtools')).text();

    expect(html).toContain('id="devtools"');
    expect(html).not.toContain('Authorization');
  });

  it('forwards a request and returns status, headers, and body', async () => {
    const response = await page('/c/local/devtools/send', json({ method: 'POST', path: '/orders/_search', body: '{"limit":1}' }));
    const answer = await response.json();

    expect(answer.status).toBe(200);
    expect(answer.headers['x-ndb-plan']).toBeDefined();
    expect(answer.headers['content-type']).toBe('application/json');
    expect(answer.body).toContain('"_id"');
    expect(answer.bytes).toBeGreaterThan(0);
  });

  it('passes an ndb error status through', async () => {
    const answer = await (await page('/c/local/devtools/send', json({ method: 'GET', path: '/nope/_stats' }))).json();

    expect(answer.status).toBe(404);
    expect(answer.body).toContain('unknown_collection');
  });

  it('refuses the backup and a watch', async () => {
    for (const path of ['/_backup', '/orders/_watch']) {
      const response = await page('/c/local/devtools/send', json({ method: 'POST', path }));

      expect(response.status).toBe(400);
    }
  });
});

describe('watch tab', () => {
  it('renders the page with the filter in the form', async () => {
    const html = await (await page(`/c/local/watch?coll=orders&where=${encodeURIComponent('{"a":1}')}`)).text();

    expect(html).toContain('id="watch"');
    expect(html).toContain('ndb-ui-xxxxxxxx');
  });

  it('streams an event after an insert, and closes the upstream when the browser leaves', async () => {
    const abort = new AbortController();
    const response = await page('/c/local/watch/stream?coll=orders&from=now', { signal: abort.signal });
    const reader = response.body!.getReader();
    const decoder = new TextDecoder();

    let seen = '';

    const until = async (needle: string) => {
      while (!seen.includes(needle)) {
        const { done, value } = await reader.read();

        if (done) break;

        seen += decoder.decode(value);
      }
    };

    await until('event: ready');
    await ndbPost('/orders/_bulk', { docs: [{ _id: 'w1', status: 'watch' }] });
    await until('"_id":"w1"');

    abort.abort();

    expect(seen).toContain('"op":"put"');
    expect(seen).toContain('"_id":"w1"');
  });

  it('reports a bad filter as a problem event', async () => {
    const text = await (await page('/c/local/watch/stream?coll=orders&where=%7Boops')).text();

    expect(text).toContain('event: problem');
  });
});

describe('backups tab', () => {
  it('renders the restore command and enables the download on a unix socket', async () => {
    const html = await (await page('/c/local/backups')).text();

    expect(html).toContain('ndb restore');
    expect(html).toContain('Download backup');
    expect(html).not.toContain('disabled');
  });

  it('streams the backup file as an attachment', async () => {
    const response = await page('/c/local/backups/download', form({}));
    const text = await response.text();

    expect(response.headers.get('content-disposition')).toMatch(/attachment; filename="ndb-local-\d{8}-\d{6}\.ndb"/);
    expect(text.split('\n')[0]).toContain('"ndb_backup"');
  });

  it('compresses with gzip', async () => {
    const response = await page('/c/local/backups/download', form({ gzip: '1' }));
    const bytes = new Uint8Array(await response.arrayBuffer());

    expect(response.headers.get('content-disposition')).toContain('.ndb.gz');
    expect(bytes[0]).toBe(0x1f);
    expect(bytes[1]).toBe(0x8b);
  });

  it('disables the download over tcp', async () => {
    store.add({ name: 'remote', url: 'tcp://127.0.0.1:1' });

    const response = await page('/c/remote/backups/download', form({}));

    expect(response.status).toBe(303);
    expect(response.headers.get('location')).toContain('unix+socket');
  });
});

describe('discover route', () => {
  const post = (body: unknown, headers: Record<string, string> = {}) =>
    page('/connections/discover', { method: 'POST', body: JSON.stringify(body), headers: { 'content-type': 'application/json', origin: 'http://ui', ...headers } });

  const open = createApp(store, { css: () => new Response('body{}'), libCss: () => new Response('.datepicker{}'), ui: () => new Response('//'), favicon: () => new Response('<svg/>') }, { discover: { configPaths: [], socketDirs: [dir], tmpDir: join(dir, 'none') } });

  it('finds the test socket and classifies fake TCP servers', async () => {
    const auth = Bun.serve({ port: 0, fetch: () => Response.json({ error: { code: 'unauthorized', message: 'x' } }, { status: 401 }) });
    const hello = Bun.serve({ port: 0, fetch: () => new Response('hello') });

    try {
      const local = await open.fetch(new Request('http://ui/connections/discover', { method: 'POST', body: JSON.stringify({ host: '', ports: `${auth.port},${hello.port}` }), headers: { 'content-type': 'application/json', origin: 'http://ui' } }));
      const body = (await local.json()) as { found: { url: string; state: string }[]; searched: number; hint?: string };

      expect(body.found.find(f => f.url === url)?.state).toBe('ok');
      expect(body.found.find(f => f.url === `tcp://127.0.0.1:${auth.port}`)?.state).toBe('needs_token');
      expect(body.found.some(f => f.url.endsWith(`:${hello.port}`))).toBe(false);
      expect(body.hint).toBeUndefined();
    } finally {
      auth.stop(true);
      hello.stop(true);
    }
  });

  it('rejects a bad Origin and a bad host', async () => {
    expect((await post({ host: '' }, { origin: 'http://evil' })).status).toBe(403);
    expect((await post({ host: 'http://x/y' })).status).toBe(400);
  });
});

describe('pitr block of the backups tab', () => {
  it('lists the windows and opens the restore dialog from the url', async () => {
    const html = await (await page('/c/local/backups')).text();

    expect(html).toContain('Point in time recovery (PITR)');
    expect(html).toContain('orders');
    expect(html).not.toContain('id="pitr-restore"');

    const open = await (await page('/c/local/backups?restore=orders')).text();

    expect(open).toContain('id="pitr-restore"');
    expect(open).toContain('value="orders_restored"');
  });

  it('restores a copy, then renames it in place from the open rename dialog', async () => {
    const at = new Date(Date.now() + 1000).toISOString().slice(0, 19);

    await Bun.sleep(2100);
    await new Ndb({ url }).remove('orders', 'a');

    const restored = await page('/c/local/backups/restore', form({ collection: 'orders', at, to: 'orders_restored' }));
    const location = restored.headers.get('location') ?? '';

    expect(restored.status).toBe(303);
    expect(location).toContain('notice=');
    expect(location).toContain('from=orders_restored');
    expect((await new Ndb({ url }).document('orders_restored', 'a'))._id).toBe('a');

    const next = await (await page(location.replace(/^.*\/c\//, '/c/'))).text();

    expect(next).toContain('id="pitr-rename"');

    const renamed = await page('/c/local/backups/rename', form({ from: 'orders_restored', to: 'orders', replace: '1' }));

    expect(renamed.headers.get('location')).toContain('notice=');
    expect((await new Ndb({ url }).document('orders', 'a'))._id).toBe('a');
  });

  it('comes back with the ndb error and the dialog open', async () => {
    const response = await page('/c/local/backups/restore', form({ collection: 'orders', at: '2020-01-01T00:00:00', to: 'x' }));
    const location = decodeURIComponent(response.headers.get('location') ?? '');

    expect(location).toContain('error=');
    expect(location).toContain('restore=orders');
  });
});

describe('pitr helpers', () => {
  it('read a datetime-local value as UTC, keep the default in the window, and page in memory', async () => {
    const { defaultAt, pageOf, utc } = await import('@/pitr');
    const now = Date.parse('2026-11-12T14:10:00Z');

    expect(defaultAt(undefined, now)).toBe('2026-11-12 14:05:00');
    expect(defaultAt('2026-11-12T14:08:00Z', now)).toBe('2026-11-12 14:08:01');
    expect(utc('2026-11-12 14:04:05')).toBe('2026-11-12T14:04:05Z');
    expect(utc('2026-11-12T14:04')).toBe('2026-11-12T14:04:00Z');
    expect(utc('2026-11-12T14:04:05Z')).toBe('2026-11-12T14:04:05Z');

    const items = Array.from({ length: 23 }, (_, i) => i);

    expect(pageOf(items, 3)).toEqual({ rows: [20, 21, 22], page: 3, pages: 3 });
    expect(pageOf(items, 9).page).toBe(3);
    expect(pageOf([], 1)).toEqual({ rows: [], page: 1, pages: 1 });
  });
});

describe('native forms', () => {
  it('send their Origin: the referrer policy is same-origin, not no-referrer', async () => {
    const response = await page('/c/local/backups');

    expect(response.headers.get('referrer-policy')).toBe('same-origin');
  });
});
