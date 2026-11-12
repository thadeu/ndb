import { afterAll, beforeAll, describe, expect, it } from 'bun:test';
import { mkdirSync, mkdtempSync, rmSync, writeFileSync } from 'node:fs';
import { tmpdir } from 'node:os';
import { join } from 'node:path';

import { createApp } from '@/app';
import { parseBlocks } from '@/components/devtools-blocks';
import { exampleGroups, findExamples, placeExample } from '@/components/devtools-examples';
import { exampleOf, metaOf } from '@/components/devtools';
import { toggleComment } from '@/components/editor-keys';
import { agoText, checkSettings, preview, readFrom, settingsQuery, watchBody, watchSnippet, type WatchSettings } from '@/components/watch-model';
import { Connections } from '@/connections';
import { Ndb } from '@/ndb';
import { barLeft, seconds, shortWhen, sliderRange, windowMs } from '@/pitr';
import { BackupsPage } from '@/views/backups';

const BIN = process.env.NDB_BIN ?? '/tmp/ndb-console-test/ndb';
const dir = mkdtempSync(join(tmpdir(), 'nd-'));
const sock = join(dir, 'n.sock');
const url = `unix://${sock}`;

let daemon: ReturnType<typeof Bun.spawn> | undefined;

const store = new Connections(join(dir, 'cfg', 'console.json'));
const app = createApp(store, { css: () => new Response('body{}'), libCss: () => new Response('.datepicker{}'), ui: () => new Response('//'), favicon: () => new Response('<svg/>') }, { allowedHosts: ['ui'] });

function page(path: string, init?: RequestInit): Promise<Response> {
  return Promise.resolve(app.fetch(new Request(`http://ui${path}`, init)));
}

function form(values: Record<string, string>): RequestInit {
  return { method: 'POST', body: new URLSearchParams(values), headers: { 'content-type': 'application/x-www-form-urlencoded', origin: 'http://ui' } };
}

function json(value: unknown): RequestInit {
  return { method: 'POST', body: JSON.stringify(value), headers: { 'content-type': 'application/json', origin: 'http://ui' } };
}

interface Answer {
  status: number;
  body: string;
  headers: Record<string, string>;
}

async function send(method: string, path: string, body = ''): Promise<Answer> {
  return (await (await page('/c/local/devtools/send', json({ method, path, body }))).json()) as Answer;
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

  await new Ndb({ url }).insert('orders', [{ _id: 'a', status: 'new' }, { _id: 'b', status: 'paid' }]);

  store.add({ name: 'local', url });
  store.add({ name: 'remote', url: 'tcp://127.0.0.1:1' });
});

afterAll(() => {
  daemon?.kill();
  rmSync(dir, { recursive: true, force: true });
});

describe('watch model', () => {
  const base: WatchSettings = { coll: 'orders', where: '', from: 'now', consumer: '' };

  it('keeps the settings in a short query string', () => {
    expect(settingsQuery(base)).toBe('coll=orders');
    expect(settingsQuery({ ...base, where: '{"a":1}', from: 'beginning' })).toBe('coll=orders&where=%7B%22a%22%3A1%7D&from=beginning');
    expect(settingsQuery({ ...base, from: 'consumer', consumer: 'relay' })).toBe('coll=orders&from=consumer&consumer=relay');
    expect(settingsQuery({ ...base, consumer: 'relay' })).toBe('coll=orders');
    expect(readFrom('nope')).toBe('now');
  });

  it('builds the body of the watch request', () => {
    expect(watchBody(base, {}, 'ndb-ui-1')).toEqual({ consumer: 'ndb-ui-1', from: 'now' });
    expect(watchBody({ ...base, from: 'beginning' }, { status: 'paid' }, 'x')).toEqual({ consumer: 'x', from: 'beginning', query: { status: 'paid' } });
    expect(watchSnippet({ ...base, where: '{"status":"paid"}' })).toBe('POST /orders/_watch\n{\n  "consumer": "my-consumer",\n  "from": "now",\n  "query": {\n    "status": "paid"\n  }\n}');
    expect(watchSnippet({ ...base, from: 'consumer', consumer: 'relay' })).toContain('"consumer": "relay"');
  });

  it('refuses a named consumer with a filter, and a bad name', () => {
    const named: WatchSettings = { ...base, from: 'consumer', consumer: 'relay' };

    expect(checkSettings(base, { a: 1 })).toBeUndefined();
    expect(checkSettings(named, {})).toBeUndefined();
    expect(checkSettings(named, { a: 1 })).toContain('Remove the filter');
    expect(checkSettings({ ...named, consumer: '' }, {})).toContain('name of the consumer');
    expect(checkSettings({ ...named, consumer: 'a b' }, {})).toContain('name of the consumer');
  });

  it('writes the preview and the age of a heartbeat', () => {
    expect(preview({ _id: 'a', _v: 2, status: 'paid' })).toBe('{"status":"paid"}');
    expect(preview(undefined)).toBe('');
    expect(preview({ text: 'x'.repeat(400) }).length).toBe(160);
    expect(agoText(1000, 4200)).toBe('3 s ago');
    expect(agoText(0, 125_000)).toBe('2 min ago');
  });
});

describe('watch page (redesign)', () => {
  it('renders the settings from the url, the live card and the request', async () => {
    const html = await (await page(`/c/local/watch?coll=orders&from=beginning&where=${encodeURIComponent('{"status":"paid"}')}`)).text();

    expect(html).toContain('The change feed of a collection, live.');
    expect(html).toContain('Start from');
    expect(html).toContain('Consumer (optional)');
    expect(html).toContain('<option value="beginning" selected');
    expect(html).toContain('Pause scroll');
    expect(html).toContain('Use this watch in code');
    expect(html).toContain('/orders/_watch');
    expect(html).toContain('href="c/local/streams"');
    expect(html).not.toContain('streams open');
    expect(html).not.toContain('Consumers of');
  });

  it('reads from the position of a named consumer and never moves it', async () => {
    const ndb = new Ndb({ url });

    await ndb.call('POST', '/orders/_watch', { consumer: 'relay', from: 'now', limit: 1 }, 200).catch(() => undefined);
    await ndb.insert('orders', [{ _id: 'n1', status: 'named' }]);

    const read = async () => {
      const abort = new AbortController();
      const response = await page('/c/local/watch/stream?coll=orders&from=consumer&consumer=relay', { signal: abort.signal });
      const reader = response.body!.getReader();
      const decoder = new TextDecoder();

      let seen = '';

      while (!seen.includes('"_id":"n1"')) {
        const { done, value } = await reader.read();

        if (done) break;

        seen += decoder.decode(value);
      }

      abort.abort();

      return seen;
    };

    expect(await read()).toContain('event: ready\ndata: relay');
    expect(await read()).toContain('"_id":"n1"');
  });

  it('refuses a named consumer with a filter', async () => {
    const text = await (await page(`/c/local/watch/stream?coll=orders&from=consumer&consumer=relay&where=${encodeURIComponent('{"a":1}')}`)).text();

    expect(text).toContain('event: problem');
    expect(text).toContain('Remove the filter');
  });
});

describe('dev tools examples', () => {
  const groups = exampleGroups('orders');
  const all = groups.flatMap(group => group.items);

  it('has the four groups and the new examples', () => {
    expect(groups.map(group => group.label)).toEqual(['Read', 'Write', 'Streams', 'System']);
    expect(all.filter(example => example.fresh).map(example => example.label)).toEqual([
      'Aggregate with $unwind',
      'Transaction (_tx)',
      'Lock and unlock',
      'Append and read',
      'Nack with a delay',
      'Claim idle entries',
      'Change the log level',
      'Create a compound index',
    ]);
  });

  it('finds examples by text, and places one in the editor once', () => {
    expect(findExamples(groups, 'unwind').flatMap(group => group.items).map(example => example.id)).toEqual(['unwind']);
    expect(findExamples(groups, 'zzz')).toEqual([]);

    const first = placeExample('GET /_stats\n', 'GET /_health');

    expect(first).toEqual({ text: 'GET /_stats\n\nGET /_health\n', line: 2 });
    expect(placeExample(first.text, 'GET /_health')).toEqual({ text: first.text, line: 2 });
    expect(placeExample('', 'GET /_health')).toEqual({ text: 'GET /_health\n', line: 0 });
  });

  it('knows the example of a block, and the headers under the body', () => {
    const block = parseBlocks('POST /devtools_demo/_tx\n{}')[0];

    expect(exampleOf(groups, block)?.id).toBe('tx');
    expect(metaOf({ 'content-type': 'application/json', 'x-ndb-plan': 'scan', 'x-request-id': 'r1' })).toEqual([
      { label: 'X-Request-Id', value: 'r1' },
      { label: 'X-Ndb-Plan', value: 'scan' },
    ]);
  });

  it('comments and uncomments lines, and a comment is never part of a body', () => {
    const edit = toggleComment('GET /_stats\nGET /_health', 0, 0);

    expect(edit.text).toBe('# GET /_stats\nGET /_health');
    expect(toggleComment(edit.text, 2, 2).text).toBe('GET /_stats\nGET /_health');
    expect(parseBlocks('POST /o/_search\n# note\n{"limit":1}')[0]?.body).toBe('{"limit":1}');
  });

  it('sends every request of every example to ndb with no error', async () => {
    const order = ['bulk', ...all.map(example => example.id).filter(id => id !== 'bulk')];

    let token = '';
    let entry = '';

    for (const id of order) {
      const example = all.find(item => item.id === id)!;

      for (const block of parseBlocks(example.text)) {
        const body = block.body.replace('TOKEN', token).replace('0-1', entry || '0-1');
        const answer = await send(block.method, block.path, body);
        const label = `${id}: ${block.method} ${block.path} -> ${answer.status} ${answer.body}`;

        expect(label).toContain(`-> 2`);

        if (block.path.endsWith('/_lock/a1')) token = JSON.parse(answer.body).token;

        if (block.path.endsWith('/groups/workers/read')) entry = JSON.parse(answer.body)[0]?._id ?? '';
      }
    }

    expect(token).not.toBe('');
    expect(entry).not.toBe('');
  });
});

describe('dev tools page (redesign)', () => {
  it('renders the header and the fallback', async () => {
    const html = await (await page('/c/local/devtools')).text();

    expect(html).toContain('Send any request of the HTTP API.');
    expect(html).toContain('c/local/explorer?coll=orders&amp;tab=documents');
    expect(html).not.toContain('Log lines');
  });

  it('returns the request id of ndb', async () => {
    const answer = await send('GET', '/_health');

    expect(answer.status).toBe(200);
    expect(answer.headers['x-request-id']).toBeTruthy();
  });
});

describe('pitr bar and slider helpers', () => {
  const now = Date.parse('2026-11-11T20:00:00Z');

  it('reads a window and places the bar', () => {
    expect(windowMs('1d')).toBe(86_400_000);
    expect(windowMs('90m')).toBe(5_400_000);
    expect(windowMs('1h30m0s')).toBeUndefined();
    expect(barLeft('1d', '2026-11-11T14:00:00Z', now)).toBe(75);
    expect(barLeft('1d', '2026-11-09T00:00:00Z', now)).toBe(0);
    expect(barLeft('1d', undefined, now)).toBe(100);
  });

  it('writes a short time and the range of the slider', () => {
    expect(shortWhen('2026-11-10T20:19:42Z')).toBe('Nov 10, 20:19');
    expect(shortWhen(undefined)).toBe('—');
    expect(seconds('2026-11-11 19:40:00')).toBe(Date.parse('2026-11-11T19:40:00Z') / 1000);
    expect(seconds('oops')).toBeUndefined();
    expect(sliderRange('2026-11-11T14:00:00.5Z', now)).toEqual({ min: Date.parse('2026-11-11T14:00:01Z') / 1000 + 1, max: now / 1000 });
  });
});

describe('backups page (redesign)', () => {
  it('lists each collection with its window bar, snapshots and bytes', async () => {
    const html = await (await page('/c/local/backups')).text();

    expect(html).toContain('Point-in-time recovery');
    expect(html).toContain('class="pitr-bar"');
    expect(html).toMatch(/\d+ snapshots? · [\d.]+ (B|KB|MB)/);
    expect(html).toContain('c/local/backups?restore=orders#pitr-restore');
    expect(html).toContain('Full backup');
    expect(html).toContain('From a terminal');
    expect(html).toContain('ndb backup /backups/daily -gzip');
    expect(html).toContain('-data-dir /var/lib/ndb');
    expect(html).not.toContain('This connection is TCP');
    expect(html).not.toContain('id="pitr-restore"');
  });

  it('opens step 1 of the restore with the slider and the two places', async () => {
    const html = await (await page('/c/local/backups?restore=orders')).text();

    expect(html).toContain('id="pitr-restore"');
    expect(html).toContain('step 1 of 2');
    expect(html).toContain('type="range"');
    expect(html).toContain('data-pitr-slider="pitr-at"');
    expect(html).toContain('value="copy" checked');
    expect(html).toContain('value="place"');
    expect(html).toContain('class="pitr-row on"');
    expect(html).toMatch(/has <strong>\d+<\/strong> documents now/);
  });

  it('restores as a copy with no rename step, and in place with step 2', async () => {
    await Bun.sleep(1100);

    const at = new Date().toISOString().slice(0, 19);

    await Bun.sleep(1100);

    const copy = await page('/c/local/backups/restore', form({ collection: 'orders', at, to: 'orders_copy', where: 'copy' }));
    const copyLocation = decodeURIComponent(copy.headers.get('location') ?? '');

    expect(copyLocation).toContain('notice=');
    expect(copyLocation).not.toContain('from=');
    expect((await new Ndb({ url }).document('orders_copy', 'a'))._id).toBe('a');

    const place = await page('/c/local/backups/restore', form({ collection: 'orders', at, to: 'orders_restored', where: 'place' }));
    const location = place.headers.get('location') ?? '';

    expect(location).toContain('from=orders_restored');

    const next = await (await page(location.replace(/^.*\/c\//, '/c/'))).text();

    expect(next).toContain('id="pitr-rename"');
    expect(next).toContain('step 2 of 2');
    expect(next).toContain('Put it in place');
  });

  it('shows the notice of a TCP connection and disables the download', () => {
    const html = String(<BackupsPage current={{ name: 'remote', url: 'tcp://10.0.0.1:7070', online: true }} online={false} pitr={{ default: '1d', collections: [] }} />);

    expect(html).toContain('This connection is TCP: use the command below.');
    expect(html).toContain('disabled');
    expect(html).toContain('Nothing to restore yet');
  });
});
