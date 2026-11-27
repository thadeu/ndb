import { afterAll, beforeAll, describe, expect, it } from 'bun:test';
import { mkdirSync, mkdtempSync, rmSync, writeFileSync } from 'node:fs';
import { tmpdir } from 'node:os';
import { join } from 'node:path';

import { createApp } from '@/app';
import { buildCurl } from '@/components/curl';
import { snippetOf } from '@/components/devtools-model';
import { Connections, splitName } from '@/connections';
import { databaseOf } from '@/databases';
import { Ndb, target } from '@/ndb';

const BIN = process.env.NDB_BIN ?? '/tmp/ndb-console-test/ndb';
const dir = mkdtempSync(join(tmpdir(), 'nd-'));
const sock = join(dir, 'n.sock');
const url = `unix://${sock}`;

let daemon: ReturnType<typeof Bun.spawn> | undefined;

const store = new Connections(join(dir, 'cfg', 'console.json'));
const app = createApp(store, { css: () => new Response('body{}'), libCss: () => new Response('.datepicker{}'), ui: () => new Response('//'), favicon: () => new Response('<svg/>') }, { allowedHosts: ['ui'] });

function page(path: string): Promise<Response> {
  return Promise.resolve(app.fetch(new Request(`http://ui${path}`)));
}

async function insert(database: string | undefined, collection: string, doc: Record<string, unknown>): Promise<void> {
  const headers: Record<string, string> = database ? { 'ndb-database': database } : {};
  const response = await fetch(`http://ndb/${collection}/_doc`, { method: 'POST', body: JSON.stringify(doc), headers, unix: sock } as RequestInit);

  if (!response.ok) throw new Error(`insert failed: ${await response.text()}`);
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

  await insert(undefined, 'orders', { _id: 'o1' });
  await insert('billion', 'mails', { _id: 'm1', to: 'ana@x.io' });

  store.add({ name: 'local', url });
});

afterAll(() => {
  daemon?.kill();
  rmSync(dir, { recursive: true, force: true });
});

describe('database of a connection URL', () => {
  it('reads the path of tcp and https, and ?db= of a unix socket', () => {
    expect(target('tcp://10.0.0.1:7070/billion')).toEqual({ base: 'http://10.0.0.1:7070', database: 'billion' });
    expect(target('https://ndb.example.com/billion/')).toEqual({ base: 'https://ndb.example.com', database: 'billion' });
    expect(target('tcp://10.0.0.1:7070?db=billion')).toEqual({ base: 'http://10.0.0.1:7070', database: 'billion' });
    expect(target('unix:///run/ndb/ndb.sock?db=billion')).toEqual({ base: 'http://ndb', unix: '/run/ndb/ndb.sock', database: 'billion' });
    expect(target('tcp://10.0.0.1:7070/')).toEqual({ base: 'http://10.0.0.1:7070' });
    expect(target('tcp+tls://:tok@ndb.example.com:7070/billion')).toEqual({ base: 'https://ndb.example.com:7070', database: 'billion' });
    expect(target('unix:///run/ndb/ndb.sock')).toEqual({ base: 'http://ndb', unix: '/run/ndb/ndb.sock' });
  });

  it('keeps the token of the URL out of the address and out of the saved file', () => {
    expect(target('tcp://:s3cret@10.0.0.1:7070/billion')).toEqual({ base: 'http://10.0.0.1:7070', database: 'billion' });

    const saved = new Connections(join(dir, 'cfg2', 'console.json'));

    saved.add({ name: 'remote', url: 'tcp://:s3cret@10.0.0.1:7070/billion' });

    expect(saved.get('remote')).toEqual({ name: 'remote', url: 'tcp://10.0.0.1:7070/billion', token: 's3cret' });

    saved.update('remote', { name: 'remote', url: 'tcp://10.0.0.2:7070', token: 'kept' });

    expect(saved.get('remote')).toEqual({ name: 'remote', url: 'tcp://10.0.0.2:7070', token: 'kept' });
  });

  it('refuses two databases', () => {
    expect(() => target('tcp://h:7070/a/b')).toThrow('one database');
    expect(() => target('tcp://h:7070/a?db=b')).toThrow('two databases');
    expect(() => target('ftp://h')).toThrow('Use unix');
  });

  it('names a page of another database as <connection>@<database>', () => {
    expect(splitName('local')).toEqual({ base: 'local' });
    expect(splitName('local@billion')).toEqual({ base: 'local', database: 'billion' });

    expect(store.get('local@billion')).toEqual({ name: 'local@billion', url, database: 'billion' });
    expect(store.get('local@Bad Name')).toBeUndefined();
    expect(store.get('nope@billion')).toBeUndefined();

    expect(databaseOf({ name: 'x', url: 'tcp://h:7070/cronota' })).toBe('cronota');
    expect(databaseOf({ name: 'x', url: 'tcp://h:7070/cronota', database: 'billion' })).toBe('billion');
    expect(databaseOf({ name: 'x', url: 'bad' })).toBe('default');
  });

  it('puts the database in the curl line and the SDK snippets', () => {
    expect(buildCurl({ method: 'GET', path: '/mails/_doc/m1', socket: '/s.sock', database: 'billion' })).toContain(`-H 'Ndb-Database: billion'`);
    expect(buildCurl({ method: 'GET', path: '/mails/_doc/m1', socket: '/s.sock', database: 'default' })).not.toContain('Ndb-Database');

    const search = { method: 'POST', path: '/mails/_search', body: '{}' };

    expect(snippetOf('ts', { ...search, base: 'http://10.0.0.1:7070', database: 'billion' })).toContain(`connect('tcp://10.0.0.1:7070/billion'`);
    expect(snippetOf('go', { ...search, socket: '/s.sock', database: 'billion' })).toContain('unix:///s.sock?db=billion');
    expect(snippetOf('ruby', { ...search, socket: '/s.sock' })).toContain('"unix:///s.sock"');
  });
});

describe('databases in the console', () => {
  it('reads and writes in the database of the endpoint', async () => {
    const billion = new Ndb({ url, database: 'billion' });

    expect((await billion.collections()).map(def => def.name)).toEqual(['mails']);
    expect((await new Ndb({ url }).collections()).map(def => def.name)).toEqual(['orders']);
    expect(await new Ndb({ url }).databases()).toEqual(['default', 'billion']);
  });

  it('lists the databases in the switcher and opens one', async () => {
    const home = await (await page('/c/local')).text();

    expect(home).toContain('Databases');
    expect(home).toContain('href="c/local%40billion"');

    const other = await (await page('/c/local@billion/explorer')).text();

    expect(other).toContain('mails');
    expect(other).not.toContain('>orders<');
    expect(other).toContain('href="c/local/explorer"');
  });

  it('opens the dashboard of a database', async () => {
    const response = await page('/c/local@billion');
    const html = await response.text();

    expect(response.status).toBe(200);
    expect(html).toContain('mails');
    expect(html).toContain('local@billion');
  });

  it('answers 404 for a database name that is not valid', async () => {
    expect((await page('/c/local@Bad/explorer')).status).toBe(404);
  });
});
