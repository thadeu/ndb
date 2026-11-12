import { afterAll, beforeAll, describe, expect, it } from 'bun:test';
import { mkdirSync, mkdtempSync, rmSync, writeFileSync } from 'node:fs';
import { tmpdir } from 'node:os';
import { join } from 'node:path';

import { aggregatePath, hasSealed, maskDeep } from '@/aggregate';
import { createApp } from '@/app';
import { Connections } from '@/connections';
import { Ndb } from '@/ndb';
import { clusterSettings, keyInDataDir, logSettings } from '@/operations';

const BIN = process.env.NDB_BIN ?? '/tmp/ndb-console-test/ndb';
const dir = mkdtempSync(join(tmpdir(), 'nenc-'));
const sock = join(dir, 'n.sock');
const url = `unix://${sock}`;

let daemon: ReturnType<typeof Bun.spawn> | undefined;

// The proxy stands for a daemon in front of the real one: it answers
// /_cluster/settings with the status of `mode` (404 as an older daemon, 403
// as a token with no admin of *) and sends every other request on, with no
// decrypt=1, as for a token with no decrypt right.
let mode: 404 | 403 = 404;
let proxy: ReturnType<typeof Bun.serve> | undefined;

const store = new Connections(join(dir, 'cfg', 'console.json'));
const app = createApp(store, { css: () => new Response('body{}'), libCss: () => new Response('.datepicker{}'), ui: () => new Response('//'), favicon: () => new Response('<svg/>') }, { allowedHosts: ['ui'] });

function page(path: string, init?: RequestInit): Promise<Response> {
  return Promise.resolve(app.fetch(new Request(`http://ui${path}`, init)));
}

function form(values: Record<string, string>): RequestInit {
  return { method: 'POST', body: new URLSearchParams(values), headers: { 'content-type': 'application/x-www-form-urlencoded', origin: 'http://ui' } };
}

function daemonCall(method: string, path: string, body?: unknown): Promise<Response> {
  return fetch(`http://ndb${path}`, { method, body: body === undefined ? undefined : JSON.stringify(body), unix: sock } as RequestInit);
}

beforeAll(async () => {
  mkdirSync(join(dir, 'data'));
  writeFileSync(join(dir, 'ndb.yml'), `data_dir: ${join(dir, 'data')}\nslow_ms: 1000\nlisten:\n  unix: ${sock}\n`);

  daemon = Bun.spawn([BIN, '-config', join(dir, 'ndb.yml'), '-tcp', ''], { stdout: 'ignore', stderr: 'ignore' });

  for (let i = 0; i < 200; i++) {
    const ready = await new Ndb({ url }).health().then(() => true).catch(() => false);

    if (ready) break;

    await Bun.sleep(25);
  }

  const vault = await daemonCall('PUT', '/vault', { blind: { cpf: 'numeric' }, encrypted: ['card_token'], unique: ['cpf'] });

  if (!vault.ok) throw new Error(`vault failed: ${await vault.text()}`);

  const insert = await daemonCall('POST', '/vault/_bulk', {
    docs: [
      { _id: 'u1', cpf: '390.533.447-05', card_token: 'tok_1' },
      { _id: 'u2', cpf: '111.222.333-44', card_token: 'tok_2' },
    ],
  });

  if (!insert.ok) throw new Error(`insert failed: ${await insert.text()}`);

  proxy = Bun.serve({
    port: 0,
    hostname: '127.0.0.1',
    async fetch(request) {
      const target = new URL(request.url);

      target.searchParams.delete('decrypt');

      const path = target.pathname + target.search;

      if (path.startsWith('/_cluster/settings')) {
        const code = mode === 404 ? 'unknown_route' : 'forbidden';

        return Response.json({ error: { code, message: code } }, { status: mode });
      }

      const body = request.method === 'GET' ? undefined : await request.text();

      return fetch(`http://ndb${path}`, { method: request.method, body, unix: sock } as RequestInit);
    },
  });

  store.add({ name: 'local', url });
  store.add({ name: 'proxy', url: `http://127.0.0.1:${proxy.port}` });
});

afterAll(() => {
  proxy?.stop(true);
  daemon?.kill();
  rmSync(dir, { recursive: true, force: true });
});

describe('operations settings', () => {
  it('shows the masked key, its source and the log settings', async () => {
    const settings = await clusterSettings(new Ndb({ url }));
    const html = await (await page('/c/local/operations')).text();

    expect(settings.encryption?.source).toBe('made');
    expect(keyInDataDir(settings.encryption?.source)).toBe(true);
    expect(html).toContain('Encryption key');
    expect(html).toContain(settings.encryption!.key);
    expect(html).toContain(settings.encryption!.fingerprint);
    expect(html).toContain('data-key-warning');
    expect(html).toContain('NDB_ENCRYPTION_KEY');
    expect(html).toContain('https://ndb.clowk.in/security#the-encryption-key');
    expect(html).toMatch(/slow_ms<\/span><span class="ops-check-value mono">1000<\/span><span class="badge pink">yml/);
  });

  it('saves the log form with PUT /_cluster/settings and shows the warnings', async () => {
    const response = await page('/c/local/operations/log', form({ level: 'warn', body_max: '512', slow_ms: '250' }));
    const location = decodeURIComponent((response.headers.get('location') ?? '').replaceAll('+', ' '));

    expect(response.status).toBe(303);
    expect(location).toContain('hold across a restart');
    expect(location).toContain('slow_ms is set by');

    const settings = await clusterSettings(new Ndb({ url }));

    expect(settings.log?.level).toEqual({ value: 'warn', source: 'stored' });
    expect(settings.log?.body_max?.value).toBe(512);

    const back = new URL(response.headers.get('location')!, 'http://ui');
    const html = await (await page(`${back.pathname}${back.search}`)).text();

    expect(html).toContain('class="note warn"');
    expect(html).toContain('it wins at the next start');

    await page('/c/local/operations/log', form({ level: 'info', body_max: '2048', slow_ms: '1000' }));
  });

  it('falls back to PUT /_log on a daemon with no /_cluster/settings', async () => {
    mode = 404;

    const html = await (await page('/c/proxy/operations')).text();

    expect(html).toContain('older than 1.21');
    expect(html).not.toContain('data-key-warning');

    const response = await page('/c/proxy/operations/log', form({ level: 'debug', body_max: '100', slow_ms: '5' }));
    const location = decodeURIComponent((response.headers.get('location') ?? '').replaceAll('+', ' '));

    expect(location).toContain('This daemon is older than 1.21: the change lasts until a restart.');
    expect((await logSettings(new Ndb({ url }))).level).toBe('debug');

    await daemonCall('PUT', '/_log', { level: 'info', body_max: 2048, slow_ms: 1000 });
  });

  it('says that a token with no admin of * cannot read the settings', async () => {
    mode = 403;

    const response = await page('/c/proxy/operations');
    const html = await response.text();

    expect(response.status).toBe(200);
    expect(html).toContain('This token has no admin of *');
  });
});

describe('dashboard encryption chips', () => {
  it('shows the key chip and the encrypted count', async () => {
    const html = await (await page('/c/local')).text();

    expect(html).toContain('Encryption key in the data dir');
    expect(html).toContain('href="c/local/operations"');
    expect(html).toContain('1 encrypted');
  });
});

describe('aggregate decrypt', () => {
  const pipeline = '[{"$group":{"_id":"$cpf","n":{"$count":{}}}}]';

  it('builds the path with ?decrypt=1, and masks sealed values', () => {
    expect(aggregatePath('vault', true)).toBe('/vault/_aggregate?decrypt=1');
    expect(aggregatePath('vault')).toBe('/vault/_aggregate');
    expect(hasSealed([{ _id: { a: 'ndb:e1:k1:x' } }])).toBe(true);
    expect(maskDeep([{ _id: 'ndb:e1:k1:x', n: 1 }])).toEqual([{ _id: '[encrypted]', n: 1 }]);
  });

  it('shows [encrypted] for a group by a blind field without decrypt', async () => {
    const html = await (await page(`/c/local/aggregate?on=vault&run=1&pipeline=${encodeURIComponent(pipeline)}`)).text();

    expect(html).toContain('[encrypted]');
    expect(html).not.toContain('ndb:e1:');
    expect(html).toContain('Select Decrypt');
    expect(html).toContain('/vault/_aggregate</span>');
  });

  it('sends decrypt=1, shows plain group values, and puts it in the request and curl', async () => {
    const html = await (await page(`/c/local/aggregate?on=vault&run=1&decrypt=1&view=table&pipeline=${encodeURIComponent(pipeline)}`)).text();

    expect(html).toMatch(/name="decrypt"\s+value="1"\s+checked/);
    expect(html).toContain('390.533.447-05');
    expect(html).toContain('111.222.333-44');
    expect(html).not.toContain('[encrypted]');
    expect(html).toContain('/vault/_aggregate?decrypt=1</span>');
    expect(html).toContain('/vault/_aggregate?decrypt=1&#39;');
  });

  it('says that the connection cannot decrypt when the values come back sealed', async () => {
    const html = await (await page(`/c/proxy/aggregate?on=vault&run=1&decrypt=1&pipeline=${encodeURIComponent(pipeline)}`)).text();

    expect(html).toContain('This connection cannot decrypt these values.');
    expect(html).toContain('[encrypted]');
  });
});
