import { afterAll, beforeAll, describe, expect, it } from 'bun:test';
import { mkdirSync, mkdtempSync, rmSync, writeFileSync } from 'node:fs';
import { tmpdir } from 'node:os';
import { join } from 'node:path';

import { createApp } from '@/app';
import { diffDocs } from '@/components/json-model';
import { allowedOps, checkRows, deniedText, keepSealed, maskDoc, SEALED_MASK, sealHint, sealingOf, shownValue } from '@/components/sealed-model';
import { Connections } from '@/connections';
import { Ndb } from '@/ndb';

const BIN = process.env.NDB_BIN ?? '/tmp/ndb-console-test/ndb';
const dir = mkdtempSync(join(tmpdir(), 'nce-'));
const sock = join(dir, 'n.sock');
const url = `unix://${sock}`;

const CPF = '390.533.447-05';
const CARD = 'tok_4242_secret';

let daemon: ReturnType<typeof Bun.spawn> | undefined;

const store = new Connections(join(dir, 'cfg', 'console.json'));
const app = createApp(store, { css: () => new Response('body{}'), libCss: () => new Response('.datepicker{}'), ui: () => new Response('//'), favicon: () => new Response('<svg/>') }, { allowedHosts: ['ui'] });

function page(path: string, init?: RequestInit): Promise<Response> {
  return Promise.resolve(app.fetch(new Request(`http://ui${path}`, init)));
}

function post(path: string, body: unknown): Promise<Response> {
  return page(path, { method: 'POST', body: JSON.stringify(body), headers: { 'content-type': 'application/json', accept: 'application/json', origin: 'http://ui' } });
}

function ndb(path: string, method: string, body?: unknown): Promise<Response> {
  return fetch(`http://ndb${path}`, { method, body: body === undefined ? undefined : JSON.stringify(body), unix: sock } as RequestInit);
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

  const made = await ndb('/enc_docs', 'PUT', { blind: { cpf: 'numeric' }, encrypted: ['card_token'] });

  if (!made.ok) throw new Error(`create failed: ${await made.text()}`);

  const insert = await ndb('/enc_docs/_bulk', 'POST', { docs: [{ _id: 'u1', name: 'Ana', cpf: CPF, card_token: CARD }] });

  if (!insert.ok) throw new Error(`insert failed: ${await insert.text()}`);

  store.add({ name: 'local', url });
});

afterAll(() => {
  daemon?.kill();
  rmSync(dir, { recursive: true, force: true });
});

describe('encrypted documents', () => {
  it('shows no sealed text and no plain value in the page', async () => {
    const html = await (await page('/c/local/explorer?coll=enc_docs&tab=documents&id=u1')).text();

    expect(html).toContain('id="doc-editor"');
    expect(html).toContain('class="sealed"');
    expect(html).toContain('2 fields are encrypted.');
    expect(html).toContain('https://ndb.clowk.in/encryption');
    expect(html).toContain('Ana');
    expect(html).not.toContain('ndb:e1:');
    expect(html).not.toContain('ndb:h1:');
    expect(html).not.toContain('cpf_bidx');
    expect(html).not.toContain(CARD);
    expect(html).not.toContain(CPF);
  });

  it('shows one plain value through the show route, with no-store', async () => {
    const answer = await post('/c/local/browser/show', { coll: 'enc_docs', id: 'u1', field: 'card_token' });

    expect(answer.status).toBe(200);
    expect(answer.headers.get('cache-control')).toBe('no-store');
    expect(await answer.json()).toEqual({ value: CARD });

    const html = await (await page('/c/local/explorer?coll=enc_docs&tab=documents&id=u1')).text();

    expect(html).not.toContain(CARD);
  });

  it('refuses a field that the document does not have', async () => {
    const answer = await post('/c/local/browser/show', { coll: 'enc_docs', id: 'u1', field: 'nope' });

    expect(answer.status).toBe(404);
  });

  it('replaces an encrypted value with $set, and the answer stays masked', async () => {
    const current = (await (await ndb('/enc_docs/_doc/u1', 'GET')).json()) as { _v: number };
    const saved = await post('/c/local/browser/save', { coll: 'enc_docs', id: 'u1', _v: current._v, set: { card_token: 'tok_new' }, unset: [] });
    const body = (await saved.json()) as { doc: Record<string, unknown> };

    expect(saved.status).toBe(200);
    expect(body.doc.card_token).toBe(SEALED_MASK);
    expect(JSON.stringify(body)).not.toContain('ndb:e1:');

    const shown = await post('/c/local/browser/show', { coll: 'enc_docs', id: 'u1', field: 'card_token' });

    expect(await shown.json()).toEqual({ value: 'tok_new' });
  });

  it('finds by a blind field and refuses a range on it before ndb', async () => {
    const found = await (await page('/c/local/explorer?coll=enc_docs&tab=documents&f=cpf&op=eq&t=string&v=39053344705')).text();

    expect(found).toContain('1 match');
    expect(found).toContain('cpf is a blind field');

    const range = await (await page('/c/local/explorer?coll=enc_docs&tab=documents&f=cpf&op=gt&t=string&v=1')).text();

    expect(range).toContain('note bad');
    expect(range).not.toContain('>Greater than<');
  });
});

describe('sealed model', () => {
  const sealing = sealingOf({ name: 'x', blind: { cpf: 'numeric' }, encrypted: ['card_token', 'cpf'] });

  it('reads the definition', () => {
    expect(sealing).toEqual({ blind: { cpf: 'numeric' }, encrypted: ['card_token'] });
    expect(sealingOf({ blind: ['tok'] }).blind).toEqual({ tok: 'exact' });
    expect(sealingOf(undefined).encrypted).toEqual([]);
  });

  it('allows equality on a blind field and only exists on an encrypted one', () => {
    expect(allowedOps('cpf', sealing)).toEqual(['eq', 'ne', 'in', 'nin', 'exists', 'missing']);
    expect(allowedOps('card_token', sealing)).toEqual(['exists', 'missing']);
    expect(allowedOps('name', sealing)).toBeUndefined();
    expect(sealHint('cpf', sealing)).toContain('39053344705');
    expect(checkRows([{ field: 'card_token', op: 'eq' }], sealing)).toContain('only filter on whether it exists');
    expect(checkRows([{ field: 'card_token', op: 'exists' }], sealing)).toBeUndefined();
  });

  it('masks sealed values and never saves the mask', () => {
    const masked = maskDoc({ _id: 'a', cpf: 'ndb:e1:k1:abc', name: 'Ana' });

    expect(masked).toEqual({ doc: { _id: 'a', cpf: SEALED_MASK, name: 'Ana' }, sealed: ['cpf'] });

    const diff = keepSealed(diffDocs({ cpf: 'x' }, { cpf: SEALED_MASK, name: 'B' }), ['cpf']);

    expect(diff.set).toEqual({ name: 'B' });
  });

  it('tells a value that is still sealed from a plain one', () => {
    expect(shownValue({ c: 'ndb:e1:k1:abc' }, 'c')).toEqual({ denied: true });
    expect(shownValue({ c: 'plain' }, 'c')).toEqual({ value: 'plain' });
    expect(deniedText('users')).toBe('This connection cannot decrypt users. Use the unix socket, the admin token, or a token with decrypt on users.');
  });
});
