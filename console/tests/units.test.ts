import { describe, expect, it } from 'bun:test';

import { buildCurl } from '@/components/curl';
import { editKey } from '@/components/editor-keys';
import { blockAt, formatBlock, lineAt, parseBlocks, tokenize } from '@/components/devtools-blocks';
import { containerIds, diffDocs, flatten, parseScalar, removeIn, setIn, addField, updateBody, describeDiff } from '@/components/json-model';
import { addEvent, MAX_EVENTS, type WatchEvent } from '@/components/watch';
import { checkRequest } from '@/routes/devtools';
import { readWhere } from '@/routes/browser';

describe('diffDocs', () => {
  const before = { _id: 'a', _v: 2, status: 'new', qty: 1, customer: { tier: 'silver', email: 'a@b.c' }, tags: ['x'], gone: true };

  it('gives dot paths for changed and added fields, and unset for removed ones', () => {
    const after = { ...before, status: 'paid', customer: { tier: 'gold', email: 'a@b.c', vip: true }, gone: undefined };

    delete (after as Record<string, unknown>).gone;

    const diff = diffDocs(before, after);

    expect(diff.set).toEqual({ status: 'paid', 'customer.tier': 'gold', 'customer.vip': true });
    expect(diff.unset).toEqual(['gone']);
  });

  it('writes an array whole and ignores _id and _v', () => {
    const diff = diffDocs(before, { ...before, _id: 'zzz', _v: 9, tags: ['x', 'y'] });

    expect(diff.set).toEqual({ tags: ['x', 'y'] });
    expect(diff.unset).toEqual([]);
  });

  it('is empty when nothing changed', () => {
    expect(diffDocs(before, structuredClone(before))).toEqual({ set: {}, unset: [], blocked: [] });
  });

  it('writes the parent whole when a nested key has a dot, and blocks a root key with a dot', () => {
    const odd = { _id: 'a', meta: { 'a.b': 1 }, 'x.y': 1 };
    const diff = diffDocs(odd, { _id: 'a', meta: { 'a.b': 2 }, 'x.y': 2 });

    expect(diff.set).toEqual({ meta: { 'a.b': 2 } });
    expect(diff.blocked).toEqual(['x.y']);
  });

  it('builds the update body with the guard and the operators', () => {
    const diff = diffDocs({ a: 1, b: 2 }, { a: 5 });

    expect(updateBody(3, diff)).toEqual({ query: { _v: 3 }, update: { $set: { a: 5 }, $unset: { b: true } } });
    expect(describeDiff(diff)).toBe('$set a · $unset b');
  });
});

describe('json model edits', () => {
  it('sets, removes, and adds without changing the input', () => {
    const doc = { a: { b: 1 }, list: [1, 2, 3] };

    expect(setIn(doc, ['a', 'b'], 2)).toEqual({ a: { b: 2 }, list: [1, 2, 3] });
    expect(removeIn(doc, ['list', '1'])).toEqual({ a: { b: 1 }, list: [1, 3] });
    expect(addField(doc, ['a'], 'c', true)).toEqual({ a: { b: 1, c: true }, list: [1, 2, 3] });
    expect(addField(doc, ['list'], '', 4)).toEqual({ a: { b: 1 }, list: [1, 2, 3, 4] });
    expect(doc).toEqual({ a: { b: 1 }, list: [1, 2, 3] });
  });

  it('parses a scalar as JSON and falls back to a string', () => {
    expect(parseScalar('12')).toBe(12);
    expect(parseScalar('"gold"')).toBe('gold');
    expect(parseScalar('gold')).toBe('gold');
    expect(parseScalar('null')).toBe(null);
  });

  it('flattens rows and hides the children of a closed container', () => {
    const doc = { a: { b: 1 }, c: 2 };

    expect(flatten(doc, new Set()).map(row => row.key)).toEqual(['a', 'b', 'c']);
    expect(flatten(doc, new Set(containerIds(doc))).map(row => row.key)).toEqual(['a', 'c']);
  });
});

describe('devtools blocks', () => {
  const text = ['# note', 'POST /o/_search', '{', '  "limit": 1', '}', '', 'GET /_stats', 'GET /_health'].join('\n');

  it('splits blocks at blank lines and method lines', () => {
    const blocks = parseBlocks(text);

    expect(blocks.map(block => [block.method, block.path, block.start, block.end])).toEqual([
      ['POST', '/o/_search', 1, 4],
      ['GET', '/_stats', 6, 6],
      ['GET', '/_health', 7, 7],
    ]);
    expect(blocks[0]?.body).toBe('{\n  "limit": 1\n}');
  });

  it('adds the leading slash when the path omits it', () => {
    const blocks = parseBlocks('POST orders/_search\n{"limit":1}\n\nGET _stats\n\nGET /_health');

    expect(blocks.map(block => block.path)).toEqual(['/orders/_search', '/_stats', '/_health']);
  });

  it('finds the block under the cursor, or the next one', () => {
    const blocks = parseBlocks(text);

    expect(blockAt(blocks, 3)?.path).toBe('/o/_search');
    expect(blockAt(blocks, 5)?.path).toBe('/_stats');
    expect(blockAt(blocks, 0)?.path).toBe('/o/_search');
  });

  it('formats the JSON body of a block and leaves bad JSON alone', () => {
    const block = parseBlocks('POST /x/_search\n{"a":1,"b":[1,2]}')[0]!;

    expect(formatBlock('POST /x/_search\n{"a":1,"b":[1,2]}', block)).toBe('POST /x/_search\n{\n  "a": 1,\n  "b": [\n    1,\n    2\n  ]\n}');
    expect(formatBlock('POST /x/_search\n{oops', parseBlocks('POST /x/_search\n{oops')[0]!)).toBeUndefined();
  });

  it('maps an offset to a line and colors tokens', () => {
    expect(lineAt('a\nb\nc', 4)).toBe(2);
    expect(tokenize('  "$inc": { "qty": -1 }').map(token => token.tone)).toContain('op');
    expect(tokenize('GET /_stats')[0]?.tone).toBe('method');
  });
});

describe('curl', () => {
  it('builds a unix line without a token', () => {
    expect(buildCurl({ method: 'POST', path: '/o/_search', body: '{ "limit": 1 }', socket: '/run/ndb/ndb.sock' })).toBe(
      `curl --unix-socket '/run/ndb/ndb.sock' -X POST 'http://ndb/o/_search' -d '{"limit":1}'`,
    );
  });

  it('builds a tcp line with the token placeholder', () => {
    const line = buildCurl({ method: 'GET', path: '/_stats', base: 'http://10.0.0.1:7070' });

    expect(line).toBe(`curl -H "Authorization: Bearer $NDB_TOKEN" 'http://10.0.0.1:7070/_stats'`);
  });

  it('escapes a single quote in the body', () => {
    expect(buildCurl({ method: 'POST', path: '/x/_search', body: `{"a":"it's"}`, socket: '/s' })).toContain(`'{"a":"it'\\''s"}'`);
  });
});

describe('routes helpers', () => {
  it('refuses paths that Dev Tools must not send', () => {
    expect(checkRequest({ method: 'POST', path: '/_backup' })).toContain('Backups');
    expect(checkRequest({ method: 'POST', path: '/o/_watch' })).toContain('Watch');
    expect(checkRequest({ method: 'GET', path: '//evil/x/_search' })).toBeDefined();
    expect(checkRequest({ method: 'GET', path: '/x/../etc' })).toBeDefined();
    expect(checkRequest({ method: 'PATCH', path: '/_stats' })).toBeDefined();
    expect(checkRequest({ method: 'GET', path: '/_stats' })).toBeUndefined();
    expect(checkRequest({ method: 'GET', path: '/_health' })).toBeUndefined();
  });

  it('reads the where box', () => {
    expect(readWhere('').where).toEqual({});
    expect(readWhere('{"a":1}').where).toEqual({ a: 1 });
    expect(readWhere('{oops').error).toContain('not valid JSON');
    expect(readWhere('[1]').error).toContain('object');
  });

  it('keeps at most 500 watch events, newest first', () => {
    let events: WatchEvent[] = [];

    for (let i = 0; i < MAX_EVENTS + 5; i++) events = addEvent(events, { key: i, at: 0, op: 'put', id: String(i) });

    expect(events).toHaveLength(MAX_EVENTS);
    expect(events[0]?.id).toBe(String(MAX_EVENTS + 4));
  });
});

describe('editor keys', () => {
  const at = (text: string) => text.indexOf('|');
  const press = (marked: string, key: string, shift = false) => {
    const start = at(marked);
    const text = marked.replace('|', '');
    const edit = editKey(text, start, start, { key, shift });

    return edit && edit.text.slice(0, edit.start) + '|' + edit.text.slice(edit.start);
  };

  it('keeps the indent on Enter', () => {
    expect(press('  "a": 1,|', 'Enter')).toBe('  "a": 1,\n  |');
  });

  it('adds a level after { and splits an empty pair', () => {
    expect(press('{|', 'Enter')).toBe('{\n  |');
    expect(press('  {|}', 'Enter')).toBe('  {\n    |\n  }');
  });

  it('dedents a closer typed on a blank line', () => {
    expect(press('{\n    |', '}')).toBe('{\n  }|');
  });

  it('closes pairs and steps over the closer', () => {
    expect(press('|', '{')).toBe('{|}');
    expect(press('{|}', '}')).toBe('{}|');
    expect(press('|', '"')).toBe('"|"');
    expect(press('"a|"', '"')).toBe('"a"|');
    expect(press('|abc', '"')).toBeNull();
  });

  it('removes an empty pair on Backspace', () => {
    expect(press('[|]', 'Backspace')).toBe('|');
  });

  it('indents and unindents the selected lines with Tab', () => {
    const text = 'a\nb';
    const right = editKey(text, 0, 3, { key: 'Tab', shift: false });
    const left = editKey('  a\n  b', 0, 7, { key: 'Tab', shift: true });

    expect(right?.text).toBe('  a\n  b');
    expect(left?.text).toBe('a\nb');
  });
});
