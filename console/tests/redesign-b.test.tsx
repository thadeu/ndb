import { afterAll, beforeAll, describe, expect, it } from 'bun:test';
import { mkdirSync, mkdtempSync, rmSync, writeFileSync } from 'node:fs';
import { tmpdir } from 'node:os';
import { join } from 'node:path';

import { createApp } from '@/app';
import { definitionBody, dropNotice, dropRefusal, favoritesKey, fieldsError, indexKind, indexRequest, indexRows, indexServes, indexState, isPattern, lines, parseFields, planText, requestText, toggleFavorite } from '@/collections';
import { Connections } from '@/connections';
import { Ndb } from '@/ndb';
import { restoreName } from '@/pitr';

const BIN = process.env.NDB_BIN ?? '/tmp/ndb-console-test/ndb';
const dir = mkdtempSync(join(tmpdir(), 'nb-'));
const sock = join(dir, 'n.sock');
const url = `unix://${sock}`;

let daemon: ReturnType<typeof Bun.spawn> | undefined;

const store = new Connections(join(dir, 'cfg', 'console.json'));
const app = createApp(store, { css: () => new Response('body{}'), libCss: () => new Response('.datepicker{}'), ui: () => new Response('//'), favicon: () => new Response('<svg/>') }, { allowedHosts: ['ui'] });

function page(path: string, init?: RequestInit): Promise<Response> {
  return Promise.resolve(app.fetch(new Request(`http://ui${path}`, init)));
}

function form(values: Record<string, string>, headers: Record<string, string> = {}): RequestInit {
  return { method: 'POST', body: new URLSearchParams(values), headers: { 'content-type': 'application/x-www-form-urlencoded', origin: 'http://ui', ...headers } };
}

async function html(path: string): Promise<string> {
  return (await page(path)).text();
}

async function ndb<T>(method: string, path: string, body?: unknown): Promise<T> {
  const response = await fetch(`http://ndb${path}`, { method, body: body === undefined ? undefined : JSON.stringify(body), unix: sock } as RequestInit);

  if (!response.ok) throw new Error(`${method} ${path} failed: ${await response.text()}`);

  return (await response.json()) as T;
}

// follow posts a form and returns the redirect target as a path of the app.
async function post(path: string, values: Record<string, string>): Promise<string> {
  const response = await page(path, form(values));

  expect(response.status).toBe(303);

  return decodeURIComponent((response.headers.get('location') ?? '').replaceAll('+', ' '));
}

beforeAll(async () => {
  mkdirSync(join(dir, 'data'));
  writeFileSync(join(dir, 'ndb.yml'), `data_dir: ${join(dir, 'data')}\nlisten:\n  unix: ${sock}\n`);

  daemon = Bun.spawn([BIN, '-config', join(dir, 'ndb.yml'), '-tcp', ''], { stdout: 'ignore', stderr: 'ignore' });

  for (let i = 0; i < 200; i++) {
    const ready = await new Ndb({ url }).health().then(() => true).catch(() => false);

    if (ready) break;

    await Bun.sleep(25);
  }

  await ndb('PUT', '/orders', { time: 'created_at', indexes: ['status', 'account_id,created_at'], unique: ['account_id,email'] });
  await ndb('POST', '/orders/_bulk', { docs: [{ _id: 'a', status: 'new', account_id: 'x', email: 'e' }, { _id: 'b', status: 'paid', account_id: 'x', email: 'f' }] });
  await ndb('POST', '/orders_events/_bulk', { docs: [{ _id: 'e1' }] });
  await ndb('PUT', '/jobs', { stream: true });

  store.add({ name: 'local', url });
});

afterAll(() => {
  daemon?.kill();
  rmSync(dir, { recursive: true, force: true });
});

describe('collections logic', () => {
  it('reads the fields of an index in order, once each', () => {
    expect(parseFields(' account_id, status  account_id\nitems.sku ')).toEqual(['account_id', 'status', 'items.sku']);
    expect(parseFields('')).toEqual([]);
  });

  it('refuses no field, more than 4 fields, and _id', () => {
    expect(fieldsError([])).toBeDefined();
    expect(fieldsError(['a', 'b', 'c', 'd', 'e'])).toContain('at most 4');
    expect(fieldsError(['_id', 'a'])).toContain('_id');
    expect(fieldsError(['a', 'b', 'c', 'd'])).toBeUndefined();
  });

  it('builds the request of an index and of a unique key', () => {
    expect(requestText(indexRequest('orders', ['account_id', 'status'], false))).toBe('PUT /orders/_indexes/account_id,status');
    expect(requestText(indexRequest('orders', ['email'], true))).toBe('PUT /orders\n{"unique":["email"]}');
  });

  it('names the kind and the use of an index', () => {
    expect(indexKind('_id')).toBe('built in');
    expect(indexKind('status')).toBe('single');
    expect(indexKind('items.sku')).toBe('path');
    expect(indexKind('a,b.c')).toBe('compound');
    expect(indexServes('account_id,created_at')).toBe('equality on account_id, range or sort on created_at');
    expect(indexServes('account_id,email', true)).toBe('one email for each account_id');
  });

  it('shows the build progress of an index', () => {
    expect(indexState({ field: 'a', state: 'ready', done: 0, total: 0 })).toBe('ready');
    expect(indexState({ field: 'a', state: 'building', done: 31, total: 50 })).toBe('building 62%');
    expect(indexState({ field: 'a', state: 'building', done: 0, total: 0 })).toBe('building');
  });

  it('lists _id, the indexes, and the unique keys', () => {
    const rows = indexRows([{ field: 'status', state: 'ready', done: 0, total: 0 }, { field: 'email', state: 'building', done: 1, total: 2 }], ['email', 'a,b']);

    expect(rows.map(row => row.fields)).toEqual(['_id', 'status', 'email', 'a,b']);
    expect(rows.map(row => row.unique)).toEqual([true, false, true, true]);
    expect(rows.map(row => row.droppable)).toEqual([false, true, true, false]);
    expect(rows.map(row => row.keyOnly)).toEqual([false, false, false, true]);
    expect(rows[2]?.state).toBe('building 50%');
    expect(rows[3]?.state).toBe('enforced');
  });

  it('refuses the drop of a name that is not an index', () => {
    const definition = { indexes: ['status', 'email'], unique: ['email', 'a,b'] };

    expect(dropRefusal(definition, 'status')).toBeUndefined();
    expect(dropRefusal(definition, 'email')).toBeUndefined();
    expect(dropRefusal(undefined, 'status')).toBeUndefined();
    expect(dropRefusal(definition, 'a,b')).toContain('a,b is a unique key, not an index.');
    expect(dropRefusal(definition, 'nope')).toBe('The collection has no index nope.');
    expect(dropRefusal(definition, '_id')).toBe('The _id index always exists.');
    expect(dropRefusal(definition, '')).toBe('Give the name of the index.');

    expect(dropNotice(definition, 'status')).toBe('Dropped the index status.');
    expect(dropNotice(definition, 'email')).toBe('Dropped the index email. The unique key email stays.');
  });

  it('builds a definition body with only the keys that have a value', () => {
    const body = definitionBody({ write: 'engine', time: ' ts ', ttl: '', indexes: 'status\n account_id, created_at \n', unique: '' });

    expect(body).toEqual({ write: 'engine', time: 'ts', indexes: ['status', 'account_id,created_at'] });
    expect(lines('a\r\n\r\nb')).toEqual(['a', 'b']);
  });

  it('knows a pattern, a plan, and the favorites', () => {
    expect(isPattern('logs-2026-*')).toBe(true);
    expect(isPattern('orders')).toBe(false);
    expect(planText('index:status')).toBe('index status');
    expect(planText('scan')).toBe('scan');
    expect(toggleFavorite(['a'], 'b')).toEqual(['a', 'b']);
    expect(toggleFavorite(['a', 'b'], 'a')).toEqual(['b']);
    expect(favoritesKey('local')).toBe('ndb-console:favorites:local');
  });
});

describe('collection list', () => {
  it('shows the count, the rows, the chips, and the stream badge', async () => {
    const text = await html('/c/local/explorer');

    expect(text).toContain('<span class="explorer-list-count">(3)</span>');
    expect(text).toContain('aria-label="New collection"');
    expect(text).toContain('placeholder="Find, or a pattern: logs-2026-*"');
    expect(text).toContain('>Favorites</a>');
    expect(text).toContain('data-favorites="local"');
    expect(text).toContain('data-favorite="orders"');
    expect(text).toContain('href="c/local/streams?stream=jobs"');
    expect(text).toContain('<span class="explorer-item-kind">stream</span>');
    expect(text).toContain('href="c/local/explorer?coll=orders"');
    expect(text).toContain('Select a collection');
  });

  it('finds by text and by a pattern of the daemon', async () => {
    const byText = await html('/c/local/explorer?find=event');

    expect(byText).toContain('data-collection="orders_events"');
    expect(byText).not.toContain('data-collection="jobs"');

    const byPattern = await html(`/c/local/explorer?find=${encodeURIComponent('ord*')}`);

    expect(byPattern).toContain('data-collection="orders"');
    expect(byPattern).toContain('data-collection="orders_events"');
    expect(byPattern).not.toContain('data-collection="jobs"');
  });

  it('shows only streams with the Streams chip', async () => {
    const text = await html('/c/local/explorer?show=streams');

    expect(text).toContain('data-collection="jobs"');
    expect(text).not.toContain('data-collection="orders"');
    expect(text).toContain('data-show="streams"');
  });

  it('opens the New collection dialog from the URL', async () => {
    const text = await html('/c/local/explorer?dialog=new');

    expect(text).toMatch(/<dialog id="new-collection"[^>]* open/);
    expect(text).toContain('action="c/local/explorer/create"');
  });
});

describe('collection header and tabs', () => {
  it('shows the name, the badges, the actions, and six tabs', async () => {
    const text = await html('/c/local/explorer?coll=orders');

    expect(text).toContain('class="explorer has-coll"');
    expect(text).toContain('<h1 class="mono explorer-title">orders</h1>');
    expect(text).toContain('<span class="badge neutral">engine</span>');
    expect(text).toContain('<span class="badge ok">healthy</span>');
    expect(text).toMatch(/Updated \d+:\d\d/);
    expect(text).toContain('data-popover-open="collection-actions"');
    expect(text).toContain('action="c/local/explorer/compact"');
    expect(text).toContain('action="c/local/explorer/truncate"');
    expect(text).toContain('action="c/local/explorer/drop"');
    expect(text).toContain('action="c/local/explorer/rename"');
    expect(text).toContain('Explore documents');

    for (const tab of ['Overview', 'Documents', 'Indexes', 'Storage', 'Locks', 'Backups']) expect(text).toContain(`${tab}</a>`);
  });

  it('shows the overview with real values', async () => {
    const text = await html('/c/local/explorer?coll=orders');

    expect(text).toContain('General information');
    expect(text).toContain('engine (SDK writes)');
    expect(text).toContain('Quarantined lines');
    expect(text).toContain('Definition');
    expect(text).toContain('created_at');
    expect(text).toContain('Indexes and keys');
    expect(text).toContain('compound unique');
    expect(text).toContain('WAL files');
    expect(text).not.toContain('[N]');
    expect(text).not.toContain('[SIZE]');
  });

  it('keeps the documents tab', async () => {
    const text = await html('/c/local/explorer?coll=orders&tab=documents');

    expect(text).toContain('paid');
    expect(text).toContain('aria-current="page"');
  });

  it('shows storage, locks, and backups', async () => {
    const storage = await html('/c/local/explorer?coll=orders&tab=storage');

    expect(storage).toContain('Disk use by part');
    expect(storage).toContain('Segments');

    const locks = await html('/c/local/explorer?coll=orders&tab=locks');

    expect(locks).toContain('POST /orders/_lock/{id}');
    expect(locks).toContain('POST /orders/_unlock/{id}');
    expect(locks).toContain('no route that lists the open locks');

    const backups = await html('/c/local/explorer?coll=orders&tab=backups');

    expect(backups).toContain('href="c/local/backups"');
  });
});

describe('indexes tab', () => {
  it('lists the indexes with kind, unique, state, and Drop', async () => {
    const text = await html('/c/local/explorer?coll=orders&tab=indexes');

    expect(text).toContain('<td class="mono strong index-fields">_id</td>');
    expect(text).toContain('<span class="badge neutral">built in</span>');
    expect(text).toContain('<td class="mono strong index-fields">account_id,created_at</td>');
    expect(text).toContain('<span class="badge pink">compound</span>');
    expect(text).toContain('equality on account_id, range or sort on created_at');
    expect(text).toContain('one email for each account_id');
    expect(text).toContain('name="index" value="status"');
    expect(text).not.toContain('name="index" value="_id"');
    expect(text).not.toContain('name="index" value="account_id,email"');
    expect(text).toContain('PUT /orders/_indexes/{field}');
    expect(text).toContain('Which index serves a filter?');
  });

  it('explains a filter with the plan headers', async () => {
    const indexed = await html(`/c/local/explorer?coll=orders&tab=indexes&filter=${encodeURIComponent('{"status":"paid"}')}`);

    expect(indexed).toContain('<dd class="mono">index status</dd>');
    expect(indexed).toContain('1 of 2');
    expect(indexed).toContain('scan of 2');

    const scanned = await html(`/c/local/explorer?coll=orders&tab=indexes&filter=${encodeURIComponent('{"email":"e"}')}`);

    expect(scanned).toContain('<dd class="mono">scan</dd>');
    expect(scanned).not.toContain('without it');

    const bad = await html(`/c/local/explorer?coll=orders&tab=indexes&filter=${encodeURIComponent('{oops')}`);

    expect(bad).toContain('The filter is not valid JSON.');
  });

  it('creates and drops an index', async () => {
    const created = await post('/c/local/explorer/indexes', { coll: 'orders', fields: 'account_id, status' });

    expect(created).toContain('explorer?coll=orders&tab=indexes');
    expect(created).toContain('The index account_id,status is building.');

    const after = await ndb<{ definition: { indexes: string[] } }>('GET', '/orders');

    expect(after.definition.indexes).toContain('account_id,status');

    const dropped = await post('/c/local/explorer/indexes/drop', { coll: 'orders', index: 'account_id,status' });

    expect(dropped).toContain('Dropped the index account_id,status.');
    expect((await ndb<{ definition: { indexes: string[] } }>('GET', '/orders')).definition.indexes).not.toContain('account_id,status');
  });

  it('creates a unique key and refuses a bad list of fields', async () => {
    const created = await post('/c/local/explorer/indexes', { coll: 'orders', fields: 'code', unique: '1' });

    expect(created).toContain('The unique key code is on.');
    expect((await ndb<{ definition: { unique: string[] } }>('GET', '/orders')).definition.unique).toContain('code');

    const refused = await post('/c/local/explorer/indexes', { coll: 'orders', fields: 'a b c d e' });

    expect(refused).toContain('error=An index has at most 4 fields.');
  });
});

describe('collection actions', () => {
  it('refuses a write from another origin', async () => {
    const response = await page('/c/local/explorer/drop', form({ coll: 'orders' }, { origin: 'http://evil.example' }));

    expect(response.status).toBe(403);
    expect((await ndb<{ rows: number }>('GET', '/orders/_stats')).rows).toBe(2);
  });

  it('creates a collection with a definition', async () => {
    const created = await post('/c/local/explorer/create', { collection: 'calls', write: 'engine', time: 'ts', indexes: 'status\naccount_id,ts', unique: 'call_id', ttl: '' });

    expect(created).toContain('explorer?coll=calls');

    const body = await ndb<{ definition: { time: string; indexes: string[]; unique: string[] } }>('GET', '/calls');

    expect(body.definition.time).toBe('ts');
    expect(body.definition.indexes).toEqual(['account_id,ts', 'status']);
    expect(body.definition.unique).toEqual(['call_id']);
  });

  it('refuses a bad name and a name that exists', async () => {
    expect(await post('/c/local/explorer/create', { collection: 'Bad Name' })).toContain('dialog=new');
    expect(await post('/c/local/explorer/create', { collection: 'orders', time: 'other' })).toContain('The collection orders exists.');
  });

  it('edits the definition', async () => {
    const saved = await post('/c/local/explorer/definition', { coll: 'calls', durability: 'relaxed', wal_retention: '2d', write: 'direct' });

    expect(saved).toContain('The definition is saved.');

    const body = await ndb<{ definition: { durability: string; wal_retention: string; write: string } }>('GET', '/calls');

    expect(body.definition.durability).toBe('relaxed');
    expect(body.definition.wal_retention).toBe('2d');
    expect(body.definition.write).toBe('engine');

    const text = await html('/c/local/explorer?coll=calls&dialog=edit');

    expect(text).toMatch(/<dialog id="edit-definition"[^>]* open/);
    expect(text).not.toContain('id="edit-time"');
    expect(text).toContain('id="edit-ttl"');
  });

  it('compacts, renames, truncates, and drops', async () => {
    await ndb('POST', '/calls/_bulk', { docs: [{ _id: 'c1', ts: '2026-11-01T00:00:00Z', call_id: '1' }] });

    expect(await post('/c/local/explorer/compact', { coll: 'calls', tab: 'storage' })).toMatch(/tab=storage.*Compacted calls in \d+ ms/);

    const renamed = await post('/c/local/explorer/rename', { coll: 'calls', to: 'calls2' });

    expect(renamed).toContain('explorer?coll=calls2');
    expect(await post('/c/local/explorer/rename', { coll: 'calls2', to: 'orders' })).toContain('dialog=rename');

    expect(await post('/c/local/explorer/truncate', { coll: 'calls2' })).toContain('calls2 is empty');
    expect((await ndb<{ rows: number }>('GET', '/calls2/_stats')).rows).toBe(0);

    const dropped = await post('/c/local/explorer/drop', { coll: 'calls2' });

    expect(dropped).toContain('calls2 is dropped');
    expect(dropped).not.toContain('coll=');
    expect(await html('/c/local/explorer')).not.toContain('data-collection="calls2"');
  });
});

describe('drop of an index', () => {
  const tab = '/c/local/explorer?coll=shop&tab=indexes';

  const definition = async () => (await ndb<{ definition: { indexes?: string[]; unique?: string[] } }>('GET', '/shop')).definition;

  beforeAll(async () => {
    await ndb('PUT', '/shop', { indexes: ['status', 'account_id,created_at', 'email'], unique: ['email', 'account_id,email'] });
    await ndb('POST', '/shop/_bulk', { docs: [{ _id: 'a', status: 'new', account_id: 'x', email: 'e' }] });
  });

  it('gives each index a Drop with a confirm, and a unique key a reason', async () => {
    const text = await html(tab);

    expect(text).toContain('name="index" value="status"');
    expect(text).toContain('name="index" value="account_id,created_at"');
    expect(text).toContain('name="index" value="email"');
    expect(text).not.toContain('name="index" value="account_id,email"');

    expect(text).toContain('Drop the index status?');
    expect(text).toContain('Queries that used it scan the collection again. You can make the index again');
    expect(text).toContain('The unique key email stays.');
    expect(text).toContain('aria-label="Drop the index account_id,created_at"');

    expect(text).toContain('no remove route');
    expect(text).toContain('ndb has no route that removes a unique key. To remove one');
  });

  it('drops a single index and takes it out of the table', async () => {
    const back = await post('/c/local/explorer/indexes/drop', { coll: 'shop', index: 'status' });

    expect(back).toContain('explorer?coll=shop&tab=indexes&notice=Dropped the index status.');
    expect((await definition()).indexes).toEqual(['account_id,created_at', 'email']);

    const text = await html(tab);

    expect(text).not.toContain('name="index" value="status"');
    expect(text).not.toContain('<td class="mono strong index-fields">status</td>');
  });

  it('drops a compound index', async () => {
    const back = await post('/c/local/explorer/indexes/drop', { coll: 'shop', index: 'account_id,created_at' });

    expect(back).toContain('notice=Dropped the index account_id,created_at.');
    expect((await definition()).indexes).toEqual(['email']);
    expect(await html(tab)).not.toContain('<td class="mono strong index-fields">account_id,created_at</td>');
  });

  it('drops the index of a field that is also a unique key, and the key stays', async () => {
    const back = await post('/c/local/explorer/indexes/drop', { coll: 'shop', index: 'email' });

    expect(back).toContain('notice=Dropped the index email. The unique key email stays.');
    expect((await definition()).indexes ?? []).toEqual([]);
    expect((await definition()).unique).toContain('email');

    const text = await html(tab);

    expect(text).toContain('<td class="mono strong index-fields">email</td>');
    expect(text).not.toContain('name="index" value="email"');
  });

  it('does not say that a unique key is dropped', async () => {
    const back = await post('/c/local/explorer/indexes/drop', { coll: 'shop', index: 'account_id,email' });

    expect(back).toContain('tab=indexes&error=account_id,email is a unique key, not an index. ndb has no route that removes a unique key.');
    expect((await definition()).unique).toContain('account_id,email');

    const unknown = await post('/c/local/explorer/indexes/drop', { coll: 'shop', index: 'nope' });

    expect(unknown).toContain('error=The collection has no index nope.');
  });
});

describe('restore from the backups tab of a collection', () => {
  const tab = '/c/local/explorer?coll=books&tab=backups';

  let at = '';

  beforeAll(async () => {
    await ndb('POST', '/books/_bulk', { docs: [{ _id: 'a', title: 'one' }, { _id: 'b', title: 'two' }] });
    await Bun.sleep(1100);

    at = new Date().toISOString().slice(0, 19).replace('T', ' ');

    await Bun.sleep(1100);
  });

  const confirmUrl = (values: Record<string, string>) => `${tab}&${new URLSearchParams({ confirm: '1', at, ...values })}`;

  it('shows the window bar and the restore form', async () => {
    const text = await html(tab);

    expect(text).toContain('class="pitr-bar"');
    expect(text).toContain('id="pitr-restore"');
    expect(text).toContain('data-pitr-slider="pitr-at"');
    expect(text).toContain('value="books_restored"');
    expect(text).toContain('value="copy" checked');
    expect(text).toContain('value="place"');
    expect(text).toContain('name="confirm" value="1"');
    expect(text).toContain('The current books goes to the trash');
    expect(text).not.toContain('id="restore-confirm"');
  });

  it('asks before a restore as a copy', async () => {
    const text = await html(confirmUrl({ where: 'copy', to: 'books_copy' }));

    expect(text).toContain('id="restore-confirm"');
    expect(text).toContain('Restore a copy of books?');
    expect(text).toContain(`${at} UTC`);
    expect(text).toContain(`datetime="${at.replace(' ', 'T')}Z" data-local-time`);
    expect(text).toContain('<span class="mono">books_copy</span>');
    expect(text).toContain('books does not change.');
    expect(text).toContain('action="c/local/backups/restore"');
    expect(text).toContain('name="back" value="explorer"');
    expect(text).toContain('name="where" value="copy"');
    expect(text).not.toContain('type="hidden" name="replace"');
    expect(text).not.toContain('class="btn destructive" type="submit"');
  });

  it('warns in the danger style before a restore in place', async () => {
    const text = await html(confirmUrl({ where: 'place' }));

    expect(text).toContain('Replace books with a restore?');
    expect(text).toContain('This replaces the current books.');
    expect(text).toContain('ndb moves the current books to the trash, where it stays restorable for its PITR window.');
    expect(text).toContain('type="hidden" name="replace" value="1"');
    expect(text).toContain('<button class="btn destructive" type="submit">Replace books</button>');
  });

  it('does not open the confirm step for a bad time or a bad name', async () => {
    const time = await html(`${tab}&confirm=1&at=soon&where=copy&to=books_copy`);

    expect(time).toContain('Give the point in time as YYYY-MM-DD HH:MM:SS.');
    expect(time).not.toContain('id="restore-confirm"');

    const name = await html(confirmUrl({ where: 'copy', to: 'Bad Name' }));

    expect(name).toContain('The name of the copy has lower case letters');
    expect(name).not.toContain('id="restore-confirm"');
  });

  it('restores as a copy and links the copy', async () => {
    const back = await post('/c/local/backups/restore', { collection: 'books', at, to: 'books_copy', where: 'copy', back: 'explorer' });

    expect(back).toContain('/c/local/explorer?coll=books&tab=backups&notice=books as of');
    expect(back).toContain('is in books_copy');
    expect(back).toContain('restored=books_copy');
    expect((await new Ndb({ url }).document('books_copy', 'a'))._id).toBe('a');

    const text = await html(back.replace(/^.*\/c\//, '/c/'));

    expect(text).toContain('books did not change.');
    expect(text).toContain('href="c/local/explorer?coll=books_copy&amp;tab=documents"');
  });

  it('comes back to the form with the error of ndb', async () => {
    const back = await post('/c/local/backups/restore', { collection: 'books', at, to: 'books_copy', where: 'copy', back: 'explorer' });

    expect(back).toContain('/c/local/explorer?coll=books&tab=backups&error=');
    expect(back).toContain('where=copy');
    expect(back).toContain('to=books_copy');
  });

  it('restores in place: the old collection is replaced by the copy', async () => {
    await new Ndb({ url }).remove('books', 'a');

    const back = await post('/c/local/backups/restore', { collection: 'books', at, where: 'place', replace: '1', back: 'explorer' });

    expect(back).toContain('notice=books is now as it was at');
    expect(back).toContain('The old books is in the trash');
    expect(back).toContain('restored=books');
    expect((await new Ndb({ url }).document('books', 'a'))._id).toBe('a');

    const names = (await new Ndb({ url }).stats()).collections.map(item => item.name);

    expect(names.filter(name => name.startsWith('books_r') && name !== 'books_restored')).toEqual([]);
  });

  it('asks on the Backups page too, and keeps its two steps', async () => {
    const text = await html(`/c/local/backups?${new URLSearchParams({ restore: 'books', confirm: '1', at, where: 'place', to: 'books_restored' })}`);

    expect(text).toContain('id="restore-confirm"');
    expect(text).toContain('Restore books, step 1 of 2?');
    expect(text).toContain('This step only makes the copy.');
    expect(text).not.toContain('name="back"');
    expect(text).not.toContain('type="hidden" name="replace"');

    const plain = await html('/c/local/backups?restore=books');

    expect(plain).toContain('name="confirm" value="1"');
    expect(plain).not.toContain('id="restore-confirm"');
  });

  it('names the copy of a restore in place inside the name limit', () => {
    const name = restoreName('a'.repeat(64), 1_700_000_000_000);

    expect(name).toMatch(/^[a-z0-9][a-z0-9_-]{0,63}$/);
    expect(restoreName('books', 1_700_000_000_000)).toBe(`books_r${(1_700_000_000).toString(36)}`);
  });
});
