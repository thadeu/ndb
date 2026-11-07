import { describe, expect, it } from 'bun:test';

import { DiscoverError, parseHost, parsePorts, probe, unixFromConfig } from '@/discover';
import { describe as label, splitTarget, suggestName } from '@/components/discover';

const reply = (status: number, body: unknown) => (async () => new Response(typeof body === 'string' ? body : JSON.stringify(body), { status })) as unknown as typeof fetch;

describe('parseHost', () => {
  it('accepts names and IPs, refuses the rest', () => {
    expect(parseHost('')).toBe('');
    expect(parseHost(' db.internal ')).toBe('db.internal');
    expect(parseHost('10.10.0.1')).toBe('10.10.0.1');
    expect(parseHost('::1')).toBe('::1');
    expect(parseHost('[fe80::1]')).toBe('fe80::1');

    for (const bad of ['http://x', 'a/b', 'a b', '300.1.1.1', 'x:7070:y!']) expect(() => parseHost(bad)).toThrow(DiscoverError);
  });
});

describe('parsePorts', () => {
  it('defaults, dedupes, and limits', () => {
    expect(parsePorts('')).toEqual([7070]);
    expect(parsePorts('7070, 8080,7070')).toEqual([7070, 8080]);
    expect(() => parsePorts('0')).toThrow(DiscoverError);
    expect(() => parsePorts('70000')).toThrow(DiscoverError);
    expect(() => parsePorts('abc')).toThrow(DiscoverError);
    expect(() => parsePorts('1,2,3,4,5,6,7,8,9,10,11')).toThrow(DiscoverError);
  });
});

describe('unixFromConfig', () => {
  it('reads unix under listen', () => {
    expect(unixFromConfig('data_dir: /d\nlisten:\n  tcp: :7070\n  unix: "/run/x.sock" # c\n')).toBe('/run/x.sock');
    expect(unixFromConfig('other:\n  unix: /no\nlisten:\n  tcp: :1\n')).toBeUndefined();
  });
});

describe('probe', () => {
  it('classifies answers', async () => {
    const stats = (async (input: string) => (String(input).endsWith('/_health') ? Response.json({ status: 'ok' }) : Response.json({ version: '0.3.0', totals: { collections: 4 } }))) as unknown as typeof fetch;

    expect(await probe('tcp://h:1', { fetch: stats })).toEqual({ url: 'tcp://h:1', state: 'ok', version: '0.3.0', collections: 4 });
    expect((await probe('tcp://h:1', { fetch: reply(401, { error: { code: 'unauthorized' } }) }))?.state).toBe('needs_token');
    expect((await probe('tcp://h:1', { fetch: reply(503, { status: 'recovering' }) }))?.state).toBe('starting');
    expect((await probe('tcp://h:1', { fetch: reply(503, { error: { code: 'not_ready' } }) }))?.state).toBe('starting');
    expect(await probe('tcp://h:1', { fetch: reply(200, 'hello') })).toBeUndefined();
    expect(await probe('tcp://h:1', { fetch: reply(500, { status: 'ok' }) })).toBeUndefined();
    expect(await probe('tcp://h:1', { fetch: (async () => { throw new Error('refused'); }) as unknown as typeof fetch })).toBeUndefined();
  });

  it('never sends an authorization header', async () => {
    let seen: HeadersInit | undefined;
    const spy = (async (_: string, init: RequestInit) => { seen = init.headers; return Response.json({}, { status: 404 }); }) as unknown as typeof fetch;

    await probe('tcp://h:1', { fetch: spy });

    expect(Object.keys(seen ?? {})).not.toContain('authorization');
  });
});

describe('labels', () => {
  it('describes a result and suggests a name', () => {
    expect(label({ url: 'x', state: 'ok', version: '0.3.0', collections: 4 })).toBe('ok · v0.3.0 · 4 collections');
    expect(label({ url: 'x', state: 'needs_token' })).toBe('needs a token');
    expect(suggestName('unix:///a.sock')).toBe('local');
    expect(suggestName('tcp://10.0.0.1:7070')).toBe('10.0.0.1');
  });
});

describe('splitTarget', () => {
  it('reads the URL field as a Discover target', () => {
    expect(splitTarget('')).toEqual({ host: '', ports: '' });
    expect(splitTarget('unix:///run/ndb/ndb.sock')).toEqual({ host: '', ports: '' });
    expect(splitTarget('localhost')).toEqual({ host: 'localhost', ports: '' });
    expect(splitTarget('tcp://10.10.0.1:7071')).toEqual({ host: '10.10.0.1', ports: '7071' });
    expect(splitTarget('db.internal:7070')).toEqual({ host: 'db.internal', ports: '7070' });
  });

  it('names a connection from its URL', () => {
    expect(suggestName('unix:///run/ndb/ndb.sock')).toBe('local');
    expect(suggestName('tcp://10.10.0.1:7070')).toBe('10.10.0.1');
    expect(suggestName('https://db.example.com/x')).toBe('db.example.com');
  });
});
