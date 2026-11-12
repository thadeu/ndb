import { afterAll, beforeAll, describe, expect, it } from 'bun:test';
import { mkdirSync, mkdtempSync, rmSync, writeFileSync } from 'node:fs';
import { tmpdir } from 'node:os';
import { join } from 'node:path';

import { createApp } from '@/app';
import { blindName, definitionBody, encryptionDraft, encryptionError, fieldNames, indexFieldsError, indexRows, reencryptBody } from '@/collections';
import { Connections } from '@/connections';
import { Ndb } from '@/ndb';

const BIN = process.env.NDB_BIN ?? '/tmp/ndb-console-test/ndb';
const dir = mkdtempSync(join(tmpdir(), 'ne-'));
const sock = join(dir, 'n.sock');
const url = `unix://${sock}`;

let daemon: ReturnType<typeof Bun.spawn> | undefined;

const store = new Connections(join(dir, 'cfg', 'console.json'));
const app = createApp(store, { css: () => new Response('body{}'), libCss: () => new Response('.datepicker{}'), ui: () => new Response('//'), favicon: () => new Response('<svg/>') }, { allowedHosts: ['ui'] });

function page(path: string, init?: RequestInit): Promise<Response> {
  return Promise.resolve(app.fetch(new Request(`http://ui${path}`, init)));
}

async function html(path: string): Promise<string> {
  return (await page(path)).text();
}

async function post(path: string, values: Record<string, string>, origin = 'http://ui'): Promise<string> {
  const init = { method: 'POST', body: new URLSearchParams(values), headers: { 'content-type': 'application/x-www-form-urlencoded', origin } };
  const response = await page(path, init);

  expect(response.status).toBe(303);

  return decodeURIComponent((response.headers.get('location') ?? '').replaceAll('+', ' '));
}

async function ndb<T>(method: string, path: string, body?: unknown): Promise<T> {
  const response = await fetch(`http://ndb${path}`, { method, body: body === undefined ? undefined : JSON.stringify(body), unix: sock } as RequestInit);

  return (await response.json()) as T;
}

type Def = { definition: { encrypted?: string[]; blind?: Record<string, string>; unique?: string[]; indexes?: string[] } };

beforeAll(async () => {
  mkdirSync(join(dir, 'data'));
  writeFileSync(join(dir, 'ndb.yml'), `data_dir: ${join(dir, 'data')}\nlisten:\n  unix: ${sock}\n`);

  daemon = Bun.spawn([BIN, '-config', join(dir, 'ndb.yml'), '-tcp', ''], { stdout: 'ignore', stderr: 'ignore' });

  for (let i = 0; i < 200; i++) {
    const ready = await new Ndb({ url }).health().then(() => true).catch(() => false);

    if (ready) break;

    await Bun.sleep(25);
  }

  store.add({ name: 'local', url });
});

afterAll(() => {
  daemon?.kill();
  rmSync(dir, { recursive: true, force: true });
});

describe('encryption logic', () => {
  it('reads the encrypted field and the blind rows of a form', () => {
    const form = { encrypted: 'pem, card', blind_field_0: 'cpf', blind_mode_0: 'numeric', blind_field_1: '', blind_mode_1: 'exact' };

    expect(encryptionDraft(form)).toEqual({ encrypted: ['pem', 'card'], blind: { cpf: 'numeric' } });
    expect(definitionBody({ ...form, unique: 'cpf' })).toEqual({ unique: ['cpf'], encrypted: ['pem', 'card'], blind: { cpf: 'numeric' } });
  });

  it('refuses what ndb refuses', () => {
    const draft = (encrypted: string[], blind: Record<string, string> = {}) => ({ encrypted, blind });

    expect(encryptionError(draft(['_id']), {})).toContain('_id');
    expect(encryptionError(draft(['ts']), { time: 'ts' })).toContain('time field');
    expect(encryptionError(draft(['exp']), { expires_field: 'exp' })).toContain('expires_field');
    expect(encryptionError(draft(['a.b']), {})).toContain('top-level');
    expect(encryptionError(draft(['x']), { stream: true })).toContain('stream');
    expect(encryptionError(draft(['x']), { write: 'direct' })).toContain('engine mode');
    expect(encryptionError(draft(['cpf']), { unique: ['cpf'] })).toContain('blind');
    expect(encryptionError(draft([], { cpf: 'numeric' }), { unique: ['cpf'] })).toBeUndefined();
  });

  it('shows an index on a blind field with its mode and equality only', () => {
    expect(blindName('org,cpf_bidx', { cpf: 'numeric' })).toEqual({ fields: 'org,cpf', blind: 'numeric' });

    const [, key] = indexRows([], ['cpf_bidx'], { cpf: 'numeric' });

    expect(key).toMatchObject({ fields: 'cpf', index: 'cpf_bidx', blind: 'numeric', unique: true });
    expect(key?.serves).toContain('$eq, $in, $ne, $nin');
  });

  it('refuses an index on a field that is only encrypted', () => {
    const definition = { encrypted: ['cpf', 'pem'], blind: { cpf: 'numeric' } };

    expect(indexFieldsError(['pem'], definition)).toContain('Only a blind field');
    expect(indexFieldsError(['cpf'], definition)).toBeUndefined();
  });

  it('lists the fields of a sample and of the definition', () => {
    expect(fieldNames([{ _id: 'a', name: 'x', addr: { city: 'y' } }], { name: 'u', encrypted: ['cpf'], blind: { cpf: 'numeric' }, unique: ['cpf_bidx'] })).toEqual(['addr', 'addr.city', 'cpf', 'name']);
  });

  it('gives the update of the docs to encrypt old documents', () => {
    expect(reencryptBody('2026-11-12')).toEqual({ query: { _id: { $exists: true } }, update: { $set: { encrypted_at: '2026-11-12' } } });
  });
});

describe('encrypted collections in the explorer', () => {
  it('creates a collection with encrypted and blind fields and a unique blind key', async () => {
    const to = await post('/c/local/explorer/create', { collection: 'users', write: 'engine', encrypted: 'pem', blind_field_0: 'cpf', blind_mode_0: 'numeric', unique: 'cpf' });

    expect(to).toContain('The collection users is ready.');

    const { definition } = await ndb<Def>('GET', '/users');

    expect(definition.encrypted).toEqual(['cpf', 'pem']);
    expect(definition.blind).toEqual({ cpf: 'numeric' });
    expect(definition.unique).toEqual(['cpf_bidx']);

    const body = await html('/c/local/explorer?coll=users&tab=indexes');

    expect(body).toContain('blind · numeric');
    expect(body).toContain('equality only ($eq, $in, $ne, $nin)');
    expect(body).toContain('lock-badge');
    expect(body).toContain('explorer-item-lock');
  });

  it('refuses the time field before ndb and shows the error in the dialog', async () => {
    const to = await post('/c/local/explorer/create', { collection: 'bad1', time: 'ts', encrypted: 'ts' });

    expect(to).toContain('dialog=new');
    expect(to).toContain('time field');
  });

  it('the unique key on a blind field refuses a second equal value through the console', async () => {
    await ndb('POST', '/users/_doc', { _id: 'u1', cpf: '390.533.447-05' });

    const to = await post('/c/local/browser/insert', { coll: 'users', doc: JSON.stringify({ _id: 'u2', cpf: '39053344705' }) });
    const count = await ndb<{ count: number }>('POST', '/users/_count', { query: {} });

    expect(to).toContain('err=');
    expect(to).toContain('cpf_bidx');
    expect(count.count).toBe(1);
  });

  it('refuses an index on a field that is only encrypted, with the message', async () => {
    const to = await post('/c/local/explorer/indexes', { coll: 'users', fields: 'pem' });

    expect(to).toContain('pem is encrypted, not blind');

    const ok = await post('/c/local/explorer/indexes', { coll: 'users', fields: 'cpf' });

    expect(ok).toContain('The index cpf is building.');
  });

  it('edit adds a blind field and offers to encrypt the old documents', async () => {
    const to = await post('/c/local/explorer/definition', { coll: 'users', tab: 'overview', durability: 'strict', blind_field_0: 'email', blind_mode_0: 'lower' });

    expect(to).toContain('reencrypt=1');

    const { definition } = await ndb<Def>('GET', '/users');

    expect(definition.blind).toEqual({ cpf: 'numeric', email: 'lower' });

    const body = await html(`/c/local/explorer?coll=users&reencrypt=1`);

    expect(body).toContain('Encrypt old documents now');
    expect(body).toContain('email · lower');

    const done = await post('/c/local/explorer/reencrypt', { coll: 'users' });

    expect(done).toContain('1 documents written again');
  });

  it('refuses a write from another site', async () => {
    const response = await page('/c/local/explorer/reencrypt', { method: 'POST', body: new URLSearchParams({ coll: 'users' }), headers: { 'content-type': 'application/x-www-form-urlencoded', origin: 'http://evil.example' } });

    expect(response.status).toBe(403);
  });
});
