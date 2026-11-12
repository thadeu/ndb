import { describe, expect, it } from 'bun:test';

import { parseBlocks } from '@/components/devtools-blocks';
import { clampSplit, completeAt, emptyHistory, errorMarks, findAll, formatJson, jsonErrorAt, jsonMark, HISTORY_CAP, record, redo, snippetOf, undo } from '@/components/devtools-model';

const snap = (text: string, at = text.length) => ({ text, start: at, end: at });

describe('dev tools undo history', () => {
  it('restores the text and the cursor, and redoes', () => {
    let history = record(emptyHistory(), { before: snap('a'), now: 0 });

    const back = undo(history, snap('a\n  ', 4))!;

    expect(back.snap).toEqual(snap('a'));

    history = back.history;

    expect(redo(history, back.snap)!.snap).toEqual(snap('a\n  ', 4));
    expect(undo(emptyHistory(), snap(''))).toBeUndefined();
  });

  it('groups plain typing until a pause or a space', () => {
    let history = emptyHistory();

    history = record(history, { before: snap(''), typed: 'a', now: 0 });
    history = record(history, { before: snap('a'), typed: 'b', now: 100 });

    expect(history.past).toHaveLength(1);

    history = record(history, { before: snap('ab'), typed: 'c', now: 900 });

    expect(history.past).toHaveLength(2);

    history = record(history, { before: snap('abc'), typed: ' ', now: 950 });
    history = record(history, { before: snap('abc '), typed: 'd', now: 960 });

    expect(history.past).toHaveLength(3);
  });

  it('caps the stack and clears redo on a new change', () => {
    let history = emptyHistory();

    for (let index = 0; index < HISTORY_CAP + 10; index++) history = record(history, { before: snap(String(index)), now: index });

    expect(history.past).toHaveLength(HISTORY_CAP);

    const back = undo(history, snap('x'))!;

    expect(record(back.history, { before: snap('y'), now: 0 }).future).toEqual([]);
  });
});

describe('dev tools marks, find, split', () => {
  it('marks a body that is not JSON, and not an NDJSON body', () => {
    const text = 'POST /o/_search\n{\n  "a": 1,,\n}\n\nPOST /o/_bulk\n{"a":1}\n{"b":2}';
    const marks = errorMarks(text, parseBlocks(text));

    expect(marks).toHaveLength(1);
    expect(marks[0]!.line).toBeGreaterThanOrEqual(1);
    expect(marks[0]!.line).toBeLessThanOrEqual(3);
    expect(marks[0]!.message).toContain('not valid JSON');
  });

  it('finds every match and clamps the split', () => {
    expect(findAll('Get get GET', 'get')).toEqual([0, 4, 8]);
    expect(findAll('abc', '')).toEqual([]);
    expect(clampSplit(10)).toBe(25);
    expect(clampSplit(90)).toBe(75);
    expect(clampSplit(Number.NaN)).toBe(50);
  });
});

describe('dev tools autocomplete', () => {
  it('completes a route after the method with the collection names', () => {
    const text = 'POST /ord';
    const found = completeAt(text, text.length, ['orders']);

    expect(found?.from).toBe(5);
    expect(found?.items.map(item => item.label)).toContain('/orders/_search');

    expect(completeAt('GET /_st', 8, [])?.items.map(item => item.label)).toContain('/_stats');
  });

  it('completes a $operator key in a body', () => {
    const text = '{ "query": { "n": { "$g';
    const found = completeAt(text, text.length, []);

    expect(found?.items.map(item => item.label)).toEqual(expect.arrayContaining(['$gt', '$gte']));
  });
});

describe('dev tools copy as', () => {
  const search = { method: 'POST', path: '/orders/_search', body: '{ "limit": 1 }', socket: '/tmp/ndb.sock' };

  it('builds a search in Go, Ruby and TypeScript', () => {
    expect(snippetOf('go', search)).toContain('coll.Search(ctx, []byte(`{"limit":1}`))');
    expect(snippetOf('ruby', search)).toContain('db.collection("orders")');
    expect(snippetOf('ts', search)).toContain(`ndb.collection('orders').search({"limit":1})`);
    expect(snippetOf('ts', { ...search, socket: undefined, base: 'http://127.0.0.1:7070' })).toContain(`connect('tcp://127.0.0.1:7070', { token: process.env.NDB_TOKEN })`);
  });

  it('falls back to the raw call in TypeScript only', () => {
    const stats = { method: 'GET', path: '/_stats', socket: '/tmp/ndb.sock' };

    expect(snippetOf('go', stats)).toBeUndefined();
    expect(snippetOf('ruby', stats)).toBeUndefined();
    expect(snippetOf('ts', stats)).toContain(`ndb.stream('GET', '/_stats')`);
  });
});

describe('jsonMark', () => {
  it('gives no mark for valid JSON', () => {
    expect(jsonMark('{\n  "a": 1\n}')).toBeUndefined();
  });

  it('marks the line of the parse error', () => {
    const mark = jsonMark('{\n  "a": 1,\n  "b": ,\n  "c": 3\n}');

    expect(mark?.line).toBe(2);
    expect(mark?.message).toStartWith('The JSON is not valid: ');
  });

  it('marks the first line when the message has no position', () => {
    expect(jsonMark('{\n  "a": 1')?.line).toBeGreaterThanOrEqual(0);
    expect(jsonMark('')?.line).toBe(0);
  });
});

describe('formatJson', () => {
  it('indents with two spaces', () => {
    expect(formatJson('{"a":[1,2]}')).toBe('{\n  "a": [\n    1,\n    2\n  ]\n}');
  });

  it('gives undefined for text that is not JSON', () => {
    expect(formatJson('{"a":')).toBeUndefined();
  });
});

describe('jsonErrorAt', () => {
  it('finds the first bad character', () => {
    expect(jsonErrorAt('{"a": ,}')).toBe(6);
    expect(jsonErrorAt('[1, 2,]')).toBe(6);
    expect(jsonErrorAt('{"a": tru}')).toBe(6);
    expect(jsonErrorAt('{"a": 1} x')).toBe(9);
  });

  it('gives the length for valid JSON or a cut text', () => {
    expect(jsonErrorAt('{"a": [1, "b\\"c", null]}')).toBe(24);
    expect(jsonErrorAt('{"a": 1')).toBe(7);
  });
});

describe('next page body', () => {
  it('adds the cursor of the answer to the body as after', async () => {
    const { nextBody } = await import('@/components/devtools');
    const block = { method: 'POST', path: '/orders/_search', body: '{"limit": 2}', start: 0, end: 0 };

    expect(nextBody(block, { headers: { 'x-ndb-next': 'abc' } })).toBe('{"limit":2,"after":"abc"}');
    expect(nextBody(block, { headers: {} })).toBeUndefined();
    expect(nextBody({ ...block, body: '[1]' }, { headers: { 'x-ndb-next': 'abc' } })).toBeUndefined();
  });
});

describe('history time', () => {
  it('shows the clock time today and the day before it on other days', async () => {
    const { whenOf } = await import('@/components/devtools');
    const now = new Date(2026, 10, 11, 23, 40).getTime();

    expect(whenOf(new Date(2026, 10, 11, 23, 37, 31).getTime(), now)).toBe('23:37');
    expect(whenOf(new Date(2026, 10, 10, 9, 5).getTime(), now)).toBe('10 Nov 09:05');
  });
});
