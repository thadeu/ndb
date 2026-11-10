import { afterAll, describe, expect, it } from 'bun:test';
import { mkdtempSync, rmSync } from 'node:fs';
import { tmpdir } from 'node:os';
import { join } from 'node:path';

import { createApp } from '@/app';
import { Connections } from '@/connections';
import { hostAllowed, parseAllowedHosts } from '@/guard';

const dir = mkdtempSync(join(tmpdir(), 'ng-'));
const store = new Connections(join(dir, 'console.json'));
const assets = { css: () => new Response('body{}'), libCss: () => new Response('.datepicker{}'), ui: () => new Response('//'), favicon: () => new Response('<svg/>') };
const app = createApp(store, assets, { allowedHosts: ['ndb.example.com', '*.internal'] });

afterAll(() => rmSync(dir, { recursive: true, force: true }));

function get(url: string, headers: Record<string, string> = {}): Promise<Response> {
  return Promise.resolve(app.fetch(new Request(url, { headers })));
}

function send(url: string, method: string, headers: Record<string, string>): Promise<Response> {
  return Promise.resolve(app.fetch(new Request(url, { method, body: '{}', headers: { 'content-type': 'application/json', ...headers } })));
}

describe('allowed hosts', () => {
  it('reads NDB_UI_ALLOWED_HOSTS as a comma list', () => {
    expect(parseAllowedHosts(' ndb.example.com, ,*.Internal., ')).toEqual(['ndb.example.com', '*.internal']);
    expect(parseAllowedHosts(undefined)).toEqual([]);
  });

  it('allows IP addresses, localhost, and the names of the list', () => {
    const allowed = ['ndb.example.com', '*.internal'];

    for (const host of ['127.0.0.1', '10.0.0.7', '[::1]', 'localhost', 'LOCALHOST.', 'ndb.example.com', 'ui.internal']) {
      expect(hostAllowed(host, allowed)).toBe(true);
    }

    for (const host of ['evil.example', 'internal', 'ndb.example.com.evil.example', 'localhost.evil.example', '127.0.0.1.nip.io']) {
      expect(hostAllowed(host, allowed)).toBe(false);
    }
  });
});

describe('host guard', () => {
  it('refuses a page for a host name that is not allowed (DNS rebinding)', async () => {
    const response = await get('http://evil.example:4321/ui');

    expect(response.status).toBe(403);
    expect(await response.text()).toContain('NDB_UI_ALLOWED_HOSTS');
  });

  it('refuses a write for a host name that is not allowed, even with its own Origin', async () => {
    const response = await send('http://evil.example/connections', 'POST', { origin: 'http://evil.example' });

    expect(response.status).toBe(403);
  });

  it('serves localhost, an IP address, and an allowed name', async () => {
    for (const url of ['http://localhost:4321/ui', 'http://127.0.0.1:4321/ui', 'http://[::1]:4321/ui', 'https://ndb.example.com/ui', 'http://console.internal/ui']) {
      expect((await get(url)).status).toBe(200);
    }
  });

  it('keeps /healthz open for any host', async () => {
    expect((await get('http://ndb-ui:4321/healthz')).status).toBe(200);
  });
});

describe('origin guard', () => {
  it('refuses a JSON write from another site on every route and method', async () => {
    for (const method of ['POST', 'PUT', 'PATCH', 'DELETE']) {
      for (const path of ['/connections', '/connections/local/delete', '/anything']) {
        const response = await send(`http://localhost:4321${path}`, method, { origin: 'http://evil.example' });

        expect(response.status).toBe(403);
      }
    }
  });

  it('refuses the null Origin of a sandboxed frame and a cross-site fetch', async () => {
    expect((await send('http://localhost:4321/connections', 'POST', { origin: 'null' })).status).toBe(403);
    expect((await send('http://localhost:4321/connections', 'POST', { 'sec-fetch-site': 'cross-site' })).status).toBe(403);
  });

  it('refuses another port of the same host: it is another origin', async () => {
    const before = store.list().length;
    const response = await send('http://localhost:4321/connections', 'POST', { origin: 'http://localhost:8080' });

    expect(response.status).toBe(403);
    expect(store.list().length).toBe(before);
  });

  it('lets a write from the console page through', async () => {
    const body = new URLSearchParams({ name: 'mine', url: 'unix:///tmp/ng-none.sock' });
    const response = await app.fetch(new Request('http://localhost:4321/connections', { method: 'POST', body, headers: { 'content-type': 'application/x-www-form-urlencoded', origin: 'http://localhost:4321' } }));

    expect(response.status).toBe(303);
    expect(store.get('mine')).toBeDefined();
  });
});
