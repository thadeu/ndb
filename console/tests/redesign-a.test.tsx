import { afterAll, beforeAll, describe, expect, it } from 'bun:test';
import { mkdirSync, mkdtempSync, rmSync, writeFileSync } from 'node:fs';
import { tmpdir } from 'node:os';
import { join } from 'node:path';

import { createApp } from '@/app';
import { Connections } from '@/connections';
import { Ndb } from '@/ndb';
import { logSettings, parseCount, parseMetrics } from '@/operations';

const BIN = process.env.NDB_BIN ?? '/tmp/ndb-console-test/ndb';
const dir = mkdtempSync(join(tmpdir(), 'nra-'));
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

async function daemonCall(method: string, path: string, body?: unknown): Promise<Response> {
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

  const insert = await daemonCall('POST', '/orders/_bulk', { docs: [{ _id: 'a', status: 'new' }, { _id: 'b', status: 'paid' }] });

  if (!insert.ok) throw new Error(`insert failed: ${await insert.text()}`);

  const stream = await daemonCall('PUT', '/jobs', { stream: true });

  if (!stream.ok) throw new Error(`stream failed: ${await stream.text()}`);

  store.add({ name: 'local', url });
});

afterAll(() => {
  daemon?.kill();
  rmSync(dir, { recursive: true, force: true });
});

describe('metrics parser', () => {
  it('sums a series over its labels and skips comments', () => {
    const metrics = parseMetrics('# TYPE ndb_requests_total counter\nndb_requests_total 12\nndb_memtable_bytes{collection="a"} 100\nndb_memtable_bytes{collection="b"} 50\nbad line here\n');

    expect(metrics.get('ndb_requests_total')).toBe(12);
    expect(metrics.get('ndb_memtable_bytes')).toBe(150);
    expect(metrics.get('nope')).toBeUndefined();
  });

  it('reads a count of zero or more', () => {
    expect(parseCount('2048')).toBe(2048);
    expect(parseCount('0')).toBe(0);
    expect(parseCount('-1')).toBeUndefined();
    expect(parseCount('1.5')).toBeUndefined();
    expect(parseCount('')).toBeUndefined();
  });
});

describe('dashboard', () => {
  it('shows the head, the health chips and the totals', async () => {
    const response = await page('/c/local');
    const html = await response.text();

    expect(response.status).toBe(200);
    expect(html).toContain('Dashboard');
    expect(html).toMatch(/Updated \d{1,2}:\d{2}/);
    expect(html).toContain('aria-label="Refresh"');
    expect(html).toContain('Healthy');
    expect(html).toContain('0 quarantined lines');
    expect(html).toContain('1 collection · 1 stream');
    expect(html).not.toContain('TCP has no token');
  });

  it('shows the traffic of /_metrics and the disk parts', async () => {
    const html = await (await page('/c/local')).text();

    expect(html).toContain('Traffic');
    expect(html).toContain('Requests');
    expect(html).toContain('Writes');
    expect(html).toContain('indexes and meta');
    expect(html).toContain('open Backups');
    expect(html).toContain('href="c/local/backups"');
  });

  it('links a collection to the explorer and a stream to the streams page', async () => {
    const html = await (await page('/c/local')).text();

    expect(html).toContain('href="c/local/explorer?coll=orders"');
    expect(html).toContain('href="c/local/streams?stream=jobs"');
    expect(html).toContain('href="c/local/explorer"');
    expect(html).toContain('ready');
  });
});

describe('connections', () => {
  it('shows the card with its transport, token badge, Edit and Open', async () => {
    const html = await (await page('/')).text();

    expect(html).toContain('Pick an ndb to open.');
    expect(html).toContain('New connection');
    expect(html).toContain('1 collection · 1 stream');
    expect(html).toContain('unix socket');
    expect(html).toContain('no token needed');
    expect(html).toContain('href="?edit=local"');
    expect(html).toContain('>Open</a>');
  });

  it('shows the Discover card and the one command card', async () => {
    const html = await (await page('/')).text();

    expect(html).toContain('Discover');
    expect(html).toContain('id="discover-host"');
    expect(html).toContain('Try ndb in one command');
    expect(html).toContain('ghcr.io/thadeu/ndb-stack');
  });

  it('keeps Remove in the edit drawer', async () => {
    const html = await (await page('/?edit=local')).text();

    expect(html).toContain('action="connections/local/delete"');
    expect(html).toContain('action="connections/local/edit"');
  });

  it('says that a TCP connection has no token, and never shows a token', async () => {
    store.add({ name: 'far', url: 'tcp://127.0.0.1:1' });
    store.add({ name: 'locked', url: 'tcp://127.0.0.1:1', token: 'hunter2-token' });

    const html = await (await page('/')).text();

    store.remove('far');
    store.remove('locked');

    expect(html).toContain('TCP · no token');
    expect(html).toContain('TCP · bearer token');
    expect(html).toContain('token set');
    expect(html).toContain('offline');
    expect(html).not.toContain('hunter2-token');
  });
});

describe('operations', () => {
  it('shows the log settings of the daemon and the cards', async () => {
    const response = await page('/c/local/operations');
    const html = await response.text();

    expect(response.status).toBe(200);
    expect(html).toContain('Operations');
    expect(html).toMatch(/value="info"\s+checked/);
    expect(html).toContain('name="body_max"');
    expect(html).toContain('value="2048"');
    expect(html).toContain('name="slow_ms"');
    expect(html).toContain('Security');
    expect(html).toContain('Unix socket');
    expect(html).toContain('nothing to check');
    expect(html).toContain('Tokens');
    expect(html).toContain('no token needed');
    expect(html).toContain('Limits');
    expect(html).toContain('class="side-link on" href="c/local/operations"');
  });

  it('applies the log form with PUT /_log', async () => {
    const response = await page('/c/local/operations/log', form({ level: 'debug', body_max: '512', slow_ms: '250' }));

    expect(response.status).toBe(303);
    expect(response.headers.get('location')).toContain('/c/local/operations?notice=');

    const settings = await logSettings(new Ndb({ url }));

    expect(settings.level).toBe('debug');
    expect(settings.body_max).toBe(512);
    expect(settings.slow_ms).toBe(250);

    const html = await (await page('/c/local/operations')).text();

    expect(html).toMatch(/value="debug"\s+checked/);
    expect(html).toContain('1 thing to check');
    expect(html).toContain('on, up to 512 bytes');

    await page('/c/local/operations/log', form({ level: 'info', body_max: '2048', slow_ms: '1000' }));
  });

  it('refuses a bad level and a bad number', async () => {
    const level = await page('/c/local/operations/log', form({ level: 'loud', body_max: '1', slow_ms: '1' }));
    const number = await page('/c/local/operations/log', form({ level: 'warn', body_max: '-4', slow_ms: '1' }));

    expect(level.headers.get('location')).toContain('error=');
    expect(number.headers.get('location')).toContain('error=');
    expect((await logSettings(new Ndb({ url }))).level).toBe('info');
  });

  it('rejects the log form from another origin', async () => {
    const wrong = await page('/c/local/operations/log', form({ level: 'quiet', body_max: '0', slow_ms: '0' }, { origin: 'http://evil.example' }));
    const missing = await page('/c/local/operations/log', { method: 'POST', body: new URLSearchParams({ level: 'quiet', body_max: '0', slow_ms: '0' }) });

    expect(wrong.status).toBe(403);
    expect(missing.status).toBe(403);
    expect((await logSettings(new Ndb({ url }))).level).toBe('info');
  });

  it('answers 404 for an unknown connection', async () => {
    expect((await page('/c/nope/operations')).status).toBe(404);
  });
});
