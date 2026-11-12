import { afterAll, beforeAll, describe, expect, it } from 'bun:test';
import { mkdirSync, mkdtempSync, rmSync, writeFileSync } from 'node:fs';
import { tmpdir } from 'node:os';
import { join } from 'node:path';

import { type Stage, chartOf, columnsOf, countRequest, formatPipeline, parsePipeline, requestBody, aggregatePath, validTarget } from '@/aggregate';
import { createApp } from '@/app';
import { Connections } from '@/connections';
import { Ndb } from '@/ndb';
import { type Group, type StreamInfo, deadName, formatMs, payloadOf, progressOf, stateOf, streamNames, totalsOf } from '@/streams';

const BIN = process.env.NDB_BIN ?? '/tmp/ndb-console-test/ndb';
const dir = mkdtempSync(join(tmpdir(), 'nc-c-'));
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

async function ndb<T>(method: string, path: string, body?: unknown): Promise<T> {
  const response = await fetch(`http://ndb${path}`, { method, body: body === undefined ? undefined : JSON.stringify(body), unix: sock } as RequestInit);

  if (!response.ok) throw new Error(`${method} ${path}: ${await response.text()}`);

  return (await response.json()) as T;
}

// post sends a form of the Streams page and returns the notice or the error
// of the redirect.
async function post(route: string, values: Record<string, string>): Promise<URLSearchParams> {
  const response = await page(`/c/local/streams/${route}`, form({ stream: 'jobs', ...values }));

  expect(response.status).toBe(303);

  return new URL(response.headers.get('location') ?? '', 'http://ui').searchParams;
}

const PIPELINE: Stage[] = [
  { op: '$match', body: { status: 'paid' } },
  { op: '$unwind', body: '$items' },
  { op: '$group', body: { _id: '$items.sku', sold: { $sum: '$items.qty' } } },
  { op: '$sort', body: { sold: -1 } },
];

beforeAll(async () => {
  mkdirSync(join(dir, 'data'));
  writeFileSync(join(dir, 'ndb.yml'), `data_dir: ${join(dir, 'data')}\nlisten:\n  unix: ${sock}\n`);

  daemon = Bun.spawn([BIN, '-config', join(dir, 'ndb.yml'), '-tcp', ''], { stdout: 'ignore', stderr: 'ignore' });

  for (let i = 0; i < 200; i++) {
    const ready = await new Ndb({ url }).health().then(() => true).catch(() => false);

    if (ready) break;

    await Bun.sleep(25);
  }

  const docs = [
    { _id: 'o1', status: 'paid', items: [{ sku: 'sku-a', qty: 2 }, { sku: 'sku-b', qty: 1 }] },
    { _id: 'o2', status: 'paid', items: [{ sku: 'sku-a', qty: 5 }] },
    { _id: 'o3', status: 'new', items: [{ sku: 'sku-c', qty: 9 }] },
  ];

  await ndb('POST', '/orders/_bulk', { docs });

  store.add({ name: 'local', url });
});

afterAll(() => {
  daemon?.kill();
  rmSync(dir, { recursive: true, force: true });
});

describe('aggregate logic', () => {
  it('parses a pipeline as an array or as a body', () => {
    expect(parsePipeline('[{"$match":{"a":1}},{"$limit":3}]').stages).toEqual([{ op: '$match', body: { a: 1 } }, { op: '$limit', body: 3 }]);
    expect(parsePipeline('{"pipeline":[{"$unwind":"$items"}]}').stages).toEqual([{ op: '$unwind', body: '$items' }]);
  });

  it('refuses text that is not a pipeline', () => {
    expect(parsePipeline('{').error).toContain('not valid JSON');
    expect(parsePipeline('{"a":1}').error).toContain('array');
    expect(parsePipeline('[{"$match":{},"$sort":{}}]').error).toContain('Stage 1');
    expect(parsePipeline('[3]').error).toContain('Stage 1');
  });

  it('formats a pipeline that parses back to the same stages', () => {
    const text = formatPipeline(PIPELINE);

    expect(text.split('\n')).toHaveLength(PIPELINE.length + 2);
    expect(parsePipeline(text).stages).toEqual(PIPELINE);
    expect(parsePipeline(requestBody(PIPELINE)).stages).toEqual(PIPELINE);
    expect(formatPipeline([])).toBe('[]');
  });

  it('builds the path of a collection, a list and a pattern', () => {
    expect(aggregatePath('orders')).toBe('/orders/_aggregate');
    expect(aggregatePath('a,b')).toBe('/a,b/_aggregate');
    expect(aggregatePath('logs-2026-*')).toBe('/logs-2026-*/_aggregate');
    expect(validTarget('orders-2026-*,calls')).toBe(true);
    expect(validTarget('a/b')).toBe(false);
    expect(validTarget('a b')).toBe(false);
    expect(validTarget('')).toBe(false);
  });

  it('counts a stage before $group with a counting group, and after it with count', () => {
    const before = countRequest(PIPELINE, 1);
    const after = countRequest(PIPELINE, 3);

    expect(before.from).toBe('n');
    expect(before.body.pipeline).toEqual([{ $match: { status: 'paid' } }, { $unwind: '$items' }, { $group: { _id: null, n: { $count: {} } } }]);
    expect(after).toEqual({ from: 'total', body: { pipeline: [PIPELINE[0], PIPELINE[1], PIPELINE[2], PIPELINE[3]].map(stage => ({ [stage!.op]: stage!.body })), count: true, limit: 1 } });
  });

  it('makes bars only from a group result with one number', () => {
    const chart = chartOf([{ _id: 'a', sold: 14 }, { _id: 'b', sold: 7 }, { _id: null, sold: 0 }]);

    expect(chart?.field).toBe('sold');
    expect(chart?.bars).toEqual([{ label: 'a', value: 14, width: 100 }, { label: 'b', value: 7, width: 50 }, { label: 'null', value: 0, width: 0 }]);
    expect(chartOf([{ _id: { s: 'x' }, n: 2 }])?.bars[0]?.label).toBe('{"s":"x"}');

    expect(chartOf([])).toBeUndefined();
    expect(chartOf([{ _id: 'a', n: 1, avg: 2 }])).toBeUndefined();
    expect(chartOf([{ _id: 'a', n: 'x' }])).toBeUndefined();
    expect(chartOf([{ _id: 'a', n: 1 }, { _id: 'b', m: 1 }])).toBeUndefined();
  });

  it('lists the columns of the rows in first-seen order', () => {
    expect(columnsOf([{ _id: 1, a: 1 }, { _id: 2, b: 1 }])).toEqual(['_id', 'a', 'b']);
  });
});

describe('streams logic', () => {
  const info: StreamInfo = { length: 10, entries_added: 10, groups: 1 };
  const group = (lag: number | null, pending = 0, consumers = 1): Group => ({ name: 'g', consumers, pending, last_delivered_id: '0-0', entries_read: null, lag });

  it('names the dead letter and strips the fields of ndb', () => {
    expect(deadName('jobs')).toBe('jobs_dead');
    expect(payloadOf({ _id: '1-0', _ts: 'x', task: 'send' })).toEqual({ task: 'send' });
  });

  it('computes the progress of a group from its lag', () => {
    expect(progressOf(group(0), info)).toBe(100);
    expect(progressOf(group(2), info)).toBe(80);
    expect(progressOf(group(10), info)).toBe(0);
    expect(progressOf(group(null), info)).toBeUndefined();
    expect(progressOf(group(0), { ...info, entries_added: 0 })).toBe(100);
  });

  it('sums the groups, with an unknown lag when one is unknown', () => {
    expect(totalsOf([group(2, 3, 2), group(1, 1, 1)])).toEqual({ consumers: 3, pending: 4, lag: 3 });
    expect(totalsOf([group(2), group(null)]).lag).toBeNull();
    expect(totalsOf([])).toEqual({ consumers: 0, pending: 0, lag: 0 });
  });

  it('says the state of a pending entry', () => {
    const entry = { _id: '1-0', consumer: 'w1', idle_ms: 4000, deliveries: 1, retry_at: null };

    expect(stateOf(entry, 1000)).toEqual({ label: 'in work', tone: 'neutral' });
    expect(stateOf({ ...entry, idle_ms: 360_000 }, 1000)).toEqual({ label: 'idle, can be claimed', tone: 'bad' });
    expect(stateOf({ ...entry, consumer: '', retry_at: 31_000 }, 1000)).toEqual({ label: 'retry in 30 s', tone: 'warn' });
    expect(stateOf({ ...entry, consumer: '', retry_at: 500 }, 1000).tone).toBe('bad');
  });

  it('formats a time span', () => {
    expect(formatMs(4000)).toBe('4 s');
    expect(formatMs(360_000)).toBe('6 min');
    expect(formatMs(7_200_000)).toBe('2 h');
    expect(formatMs(172_800_000)).toBe('2 d');
  });
});

describe('aggregate page', () => {
  const href = (params: Record<string, string>) => `/c/local/aggregate?${new URLSearchParams(params)}`;

  it('renders the form without a run', async () => {
    const response = await page('/c/local/aggregate');
    const html = await response.text();

    expect(response.status).toBe(200);
    expect(html).toContain('Build a pipeline stage by stage.');
    expect(html).toContain('name="pipeline"');
    expect(html).toContain('value="orders"');
    expect(html).toContain('No result yet');
    expect(html).toContain('data-op="$unwind"');
    expect(html).toContain('/orders/_aggregate');
    expect(html).not.toContain('{/*');
  });

  it('runs the pipeline and shows the rows that leave each stage', async () => {
    const response = await page(href({ on: 'orders', pipeline: formatPipeline(PIPELINE), run: '1' }));
    const html = await response.text();

    expect(response.status).toBe(200);
    expect(html).toContain('2 groups');
    expect(html).toContain('examined');
    expect(html).toMatch(/2 rows[\s\S]*3 rows[\s\S]*2 rows[\s\S]*2 rows/);
    expect(html).toContain('agg-bar-fill" style="width:100%"');
    expect(html).toContain('sku-a');
    expect(html).toContain('width:14%');
  });

  it('shows the result as a table and as JSON', async () => {
    const table = await (await page(href({ on: 'orders', pipeline: formatPipeline(PIPELINE), run: '1', view: 'table' }))).text();
    const json = await (await page(href({ on: 'orders', pipeline: formatPipeline(PIPELINE), run: '1', view: 'json' }))).text();

    expect(table).toContain('<th>sold</th>');
    expect(table).toContain('<td class="num">7</td>');
    expect(json).toContain('&quot;sold&quot;: 7');
  });

  it('shows the error of ndb and the error of a bad pipeline', async () => {
    const refused = await (await page(href({ on: 'orders', pipeline: '[{"$nope":{}}]', run: '1' }))).text();
    const broken = await (await page(href({ on: 'orders', pipeline: '[{', run: '1' }))).text();
    const target = await (await page(href({ on: 'a/b', pipeline: '[]', run: '1' }))).text();

    expect(refused).toContain('class="note bad"');
    expect(broken).toContain('The pipeline is not valid JSON.');
    expect(target).toContain('The target must be');
  });

  it('renders each stage body and the pipeline as a code editor', async () => {
    const html = await (await page(href({ on: 'orders', pipeline: formatPipeline(PIPELINE) }))).text();

    expect(html.match(/data-agg-body/g)?.length).toBe(PIPELINE.length + 1);
    expect(html).toMatch(/<textarea class="ce-ta"[^>]*readonly[^>]*data-agg-body/);
    expect(html).toMatch(/<textarea class="ce-ta"[^>]*name="pipeline"/);
    expect(html).toContain('data-ce-format');
  });

  it('moves a stage with a link that changes the pipeline', async () => {
    const full = await (await page(href({ on: 'orders', pipeline: formatPipeline(PIPELINE) }))).text();
    const html = full.split('<template')[0]!;
    const links = [...html.matchAll(/<a class="[^"]*agg-move[^"]*" href="([^"]*)"[^>]*>/g)];
    const pipelineOf = (link: RegExpMatchArray) => new URL(link[1]!.replace(/&amp;/g, '&'), 'http://ui/').searchParams.get('pipeline') ?? '';

    expect(links).toHaveLength(PIPELINE.length * 2);
    expect(links[0]![0]).toContain('aria-disabled="true"');
    expect(links[1]![0]).not.toContain('aria-disabled');
    expect(links[links.length - 1]![0]).toContain('aria-disabled="true"');

    expect(parsePipeline(pipelineOf(links[1]!)).stages?.map(stage => stage.op)).toEqual(['$unwind', '$match', '$group', '$sort']);
    expect(parsePipeline(pipelineOf(links[6]!)).stages?.map(stage => stage.op)).toEqual(['$match', '$unwind', '$sort', '$group']);
  });

  it('lists the collections for the target and counts the matches of a pattern', async () => {
    const html = await (await page(href({ on: 'ord*', pipeline: '[]' }))).text();
    const plain = await (await page(href({ on: 'orders', pipeline: '[]' }))).text();

    expect(html).toContain('<datalist id="agg-on-list">');
    expect(html).toContain('<option value="orders" label="3 documents">');
    expect(html).toContain('1 collection matches this target.');
    expect(plain).not.toContain('matches this target');

    const match = async (on: string) => (await (await page(`/c/local/aggregate/match?${new URLSearchParams({ on })}`)).json()) as { text: string; bad?: boolean };

    expect(await match('ord*')).toEqual({ text: '1 collection matches this target.' });
    expect(await match('zzz-*')).toEqual({ text: 'No collection matches this target.' });
    expect(await match('orders')).toEqual({ text: '' });
    expect((await match('orders,nope')).bad).toBe(true);
    expect((await match('a b,c')).bad).toBe(true);
  });

  it('writes the curl line without a token', async () => {
    const html = await (await page('/c/local/aggregate')).text();

    expect(html).toContain('curl --unix-socket');
    expect(html).not.toContain('Bearer');
  });
});

describe('streams page', () => {
  it('says how to make a stream when there is none', async () => {
    const html = await (await page('/c/local/streams')).text();

    expect(html).toContain('No stream yet');
    expect(html).toContain('&quot;stream&quot;: true');
  });

  it('refuses a write from another origin', async () => {
    const response = await page('/c/local/streams/append', form({ stream: 'jobs', doc: '{}' }, { origin: 'http://evil.example' }));

    expect(response.status).toBe(403);
  });

  it('shows a stream with its totals, groups and pending entries', async () => {
    await ndb('PUT', '/jobs', { stream: true });

    expect(await streamNames(new Ndb({ url }))).toEqual(['jobs']);

    expect((await post('append', { doc: '{"task":"a"}' })).get('notice')).toContain('Appended');
    expect((await post('append', { doc: '[1]' })).get('error')).toContain('JSON object');
    expect((await post('append', { doc: '{"task":"b"}' })).get('notice')).toContain('Appended');
    expect((await post('append', { doc: '{"task":"c"}' })).get('notice')).toContain('Appended');

    const made = await post('group', { name: 'mailers', start: '0' });

    expect(made.get('notice')).toContain('The group mailers');
    expect(made.get('group')).toBe('mailers');
    expect((await post('group', { name: 'mailers', start: '0' })).get('error')).toBeTruthy();

    await ndb('POST', '/jobs/_streams/groups/mailers/read', { consumer: 'worker-1', count: 2 });

    const html = await (await page('/c/local/streams')).text();

    expect(html).toContain('Streams / jobs');
    expect(html).toContain('append-only log');
    expect(html).toContain('Consumer groups');
    expect(html).toContain('Pending (2)');
    expect(html).toContain('Dead letter (0)');
    expect(html).toContain('worker-1');
    expect(html).toContain('in work');
    expect(html).toContain('Nack with delay');
    expect(html).toContain('width:67%');
    expect(html).not.toContain('in dead letter');
  });

  it('acks, nacks and sends an entry to the dead letter, then appends it again', async () => {
    const client = new Ndb({ url });
    const list = await ndb<{ _id: string }[]>('POST', '/jobs/_streams/groups/mailers/pending', { start: '-', end: '+', count: 10 });
    const [first, second] = [list[0]!._id, list[1]!._id];
    const where = { group: 'mailers', view: 'pending' };

    expect((await post('ack', { ...where, id: first })).get('notice')).toBe(`Acked ${first}.`);
    expect((await post('ack', { ...where, id: first })).get('notice')).toContain('was not pending');

    const dialog = await (await page(`/c/local/streams?stream=jobs&group=mailers&nack=${second}`)).text();

    expect(dialog).toContain('id="stream-nack"');

    expect((await post('nack', { ...where, id: second, delay_s: '30' })).get('notice')).toContain('comes back in 30 s');
    expect((await post('nack', { ...where, id: second, delay_s: '30' })).get('error')).toContain('already waits');
    expect((await post('nack', { ...where, id: second, delay_s: '-1' })).get('error')).toContain('whole number');

    const waiting = await (await page('/c/local/streams?stream=jobs&group=mailers')).text();

    expect(waiting).toContain('retry in');

    expect((await post('claim', { ...where, consumer: 'worker-2', min_idle_s: '0' })).get('notice')).toBe('worker-2 claimed 0 entries.');

    expect((await post('bury', { ...where, id: second })).get('notice')).toContain('jobs_dead');
    expect((await post('bury', { ...where, id: second })).get('error')).toContain('not pending');

    const dead = await client.search('jobs_dead', { query: {} });

    expect(dead.rows[0]).toMatchObject({ stream: 'jobs', group: 'mailers', id: second, deliveries: 1, reason: 'manual', doc: { task: 'b' } });

    const html = await (await page('/c/local/streams?stream=jobs&group=mailers&view=dead')).text();

    expect(html).toContain('1 in dead letter');
    expect(html).toContain('Dead letter (1)');
    expect(html).toContain('Append again');
    expect(html).toContain('Pending (0)');

    const again = await post('replay', { ...where, view: 'dead', id: String(dead.rows[0]!._id) });

    expect(again.get('notice')).toContain('Appended again');
    expect((await client.search('jobs_dead', { query: {} })).rows).toHaveLength(0);
    expect((await ndb<{ length: number }>('GET', '/jobs/_streams')).length).toBe(4);
  });

  it('claims idle entries, trims, and lists entries and consumers', async () => {
    const where = { group: 'mailers', view: 'pending' };

    await ndb('POST', '/jobs/_streams/groups/mailers/read', { consumer: 'worker-1', count: 10 });

    expect((await post('claim', { ...where, consumer: 'worker-2', min_idle_s: '0' })).get('notice')).toBe('worker-2 claimed 2 entries.');
    expect((await post('claim', { ...where, consumer: '' })).get('error')).toContain('Give the consumer');

    const consumers = await (await page('/c/local/streams?stream=jobs&group=mailers&view=consumers')).text();

    expect(consumers).toContain('worker-2');

    const entries = await (await page('/c/local/streams?stream=jobs&view=entries')).text();

    expect(entries).toContain('{&quot;task&quot;:&quot;c&quot;}');

    expect((await post('trim', {})).get('error')).toContain('one of the two');
    expect((await post('trim', {})).get('dialog')).toBe('trim');
    expect((await post('trim', { max_len: '1' })).get('notice')).toBe('Trim deleted 3 entries of jobs.');

    const picker = await (await page('/c/local/streams?stream=jobs_dead')).text();

    expect(picker).toContain('Streams / jobs_dead');
    expect(picker).toContain('aria-label="Streams"');
  });
});
