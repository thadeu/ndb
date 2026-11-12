// Pure logic of the Dev Tools page that is not about blocks: the undo
// history of the editor (also used by the JSON editor), the error marks of
// the bodies and of one JSON value, format, find, the
// autocomplete of routes and operators, the split ratio, and Copy as Go,
// Ruby or TypeScript.

import { FIELD_OPERATORS, GROUP_OPERATORS, LOGICAL_OPERATORS, STAGES } from '@/aggregate';
import { operatorKeyAt } from '@/components/code-editor-model';
import type { Block } from '@/components/devtools-blocks';

// part: undo history

export interface Snap {
  text: string;
  start: number;
  end: number;
}

export interface History {
  past: Snap[];
  future: Snap[];
  // typing is true while plain typing goes into the last entry.
  typing: boolean;
  at: number;
}

export const HISTORY_CAP = 200;
export const TYPING_PAUSE = 500;

export function emptyHistory(): History {
  return { past: [], future: [], typing: false, at: 0 };
}

export interface RecordInput {
  // before is the state before the change.
  before: Snap;
  // typed is the text of a plain typing change; undefined for any other change.
  typed?: string;
  now: number;
}

// record keeps the state before a change. Plain typing joins the last entry,
// until a pause of TYPING_PAUSE ms or a space or a new line.
export function record(history: History, input: RecordInput): History {
  const typing = input.typed !== undefined;
  const joins = typing && history.typing && input.now - history.at < TYPING_PAUSE && history.past.length > 0;
  const breaks = typing && /[\s]/.test(input.typed ?? '');

  if (joins) return { ...history, future: [], typing: !breaks, at: input.now };

  const past = [...history.past, input.before].slice(-HISTORY_CAP);

  return { past, future: [], typing: typing && !breaks, at: input.now };
}

export interface Step {
  history: History;
  snap: Snap;
}

// undo gives the state to restore, and keeps the current one for redo.
export function undo(history: History, current: Snap): Step | undefined {
  const snap = history.past[history.past.length - 1];

  if (!snap) return undefined;

  return { snap, history: { past: history.past.slice(0, -1), future: [...history.future, current], typing: false, at: 0 } };
}

export function redo(history: History, current: Snap): Step | undefined {
  const snap = history.future[history.future.length - 1];

  if (!snap) return undefined;

  return { snap, history: { past: [...history.past, current].slice(-HISTORY_CAP), future: history.future.slice(0, -1), typing: false, at: 0 } };
}

// part: error marks

export interface Mark {
  line: number;
  message: string;
}

function isJsonLines(body: string): boolean {
  const rows = body.split('\n').filter(row => row.trim() !== '' && !row.trimStart().startsWith('#'));

  if (rows.length < 2) return false;

  return rows.every(row => {
    try {
      JSON.parse(row);

      return true;
    } catch {
      return false;
    }
  });
}

// errorMarks gives one mark for each block whose body is not JSON (nor
// NDJSON): on the line of the error when the parser says a position, else on
// the first line of the body.
export function errorMarks(text: string, blocks: Block[]): Mark[] {
  const lines = text.split('\n');
  const marks: Mark[] = [];

  for (const block of blocks) {
    if (block.body === '' || isJsonLines(block.body)) continue;

    const rows = lines.slice(block.start + 1, block.end + 1);
    const kept = rows.map((row, index) => ({ row, line: block.start + 1 + index })).filter(item => !item.row.trimStart().startsWith('#'));
    const body = kept.map(item => item.row).join('\n');

    try {
      JSON.parse(body);
    } catch (error) {
      const message = error instanceof Error ? error.message : 'The body is not valid JSON.';
      const line = lineOfError(kept, message, block.start + 1);

      marks.push({ line, message: `The body is not valid JSON: ${message}` });
    }
  }

  return marks;
}

// lineOfError is the line of the position that a JSON.parse message says,
// or the first line when the message has no position.
function lineOfError(kept: { row: string; line: number }[], message: string, fallback: number): number {
  const position = /position (\d+)/.exec(message);
  const at = position ? Number(position[1]) : -1;

  let line = kept[0]?.line ?? fallback;

  if (at < 0) return line;

  let sum = 0;

  for (const item of kept) {
    line = item.line;
    sum += item.row.length + 1;

    if (sum > at) break;
  }

  return line;
}

// jsonMark gives the mark of one JSON value that is not valid, or undefined
// when the text parses.
export function jsonMark(text: string): Mark | undefined {
  try {
    JSON.parse(text);

    return undefined;
  } catch (error) {
    const message = error instanceof Error ? error.message : 'The JSON is not valid.';
    const at = jsonErrorAt(text);
    const line = text.slice(0, at).split('\n').length - 1;

    return { line, message: `The JSON is not valid: ${message}` };
  }
}

// jsonErrorAt is the offset of the first character that is not valid JSON.
// JSON.parse in Safari and Bun does not say a position, so the mark needs
// its own scan. It gives text.length when the text parses.
export function jsonErrorAt(text: string): number {
  let at = 0;

  const space = () => {
    while (at < text.length && /\s/.test(text[at] as string)) at++;
  };

  const fail = (): never => {
    throw at;
  };

  const literal = (word: string) => {
    if (text.startsWith(word, at)) at += word.length;
    else fail();
  };

  const string = () => {
    at++;

    while (at < text.length && text[at] !== '"') {
      if (text[at] === '\n') fail();

      at += text[at] === '\\' ? 2 : 1;
    }

    if (at >= text.length) fail();

    at++;
  };

  const number = () => {
    const match = /^-?(0|[1-9]\d*)(\.\d+)?([eE][+-]?\d+)?/.exec(text.slice(at));

    if (!match) fail();

    at += (match as RegExpExecArray)[0].length;
  };

  const list = (close: string, item: () => void) => {
    at++;
    space();

    if (text[at] === close) {
      at++;

      return;
    }

    for (;;) {
      item();
      space();

      if (text[at] === close) {
        at++;

        return;
      }

      if (text[at] !== ',') fail();

      at++;
      space();
    }
  };

  const value = (): void => {
    space();

    const char = text[at];

    if (char === '{') {
      list('}', () => {
        space();

        if (text[at] !== '"') fail();

        string();
        space();

        if (text[at] !== ':') fail();

        at++;
        value();
      });

      return;
    }

    if (char === '[') return list(']', value);

    if (char === '"') return string();

    if (char === 't') return literal('true');

    if (char === 'f') return literal('false');

    if (char === 'n') return literal('null');

    number();
  };

  try {
    value();
    space();

    return at < text.length ? at : text.length;
  } catch (offset) {
    return Math.min(offset as number, text.length);
  }
}

// formatJson gives the text with an indent of two spaces, or undefined when
// it is not valid JSON.
export function formatJson(text: string): string | undefined {
  try {
    return JSON.stringify(JSON.parse(text), null, 2);
  } catch {
    return undefined;
  }
}

// part: find

// findAll gives the offsets of every match of the query, without case.
export function findAll(text: string, query: string): number[] {
  if (query === '') return [];

  const haystack = text.toLowerCase();
  const needle = query.toLowerCase();
  const found: number[] = [];

  for (let at = haystack.indexOf(needle); at !== -1; at = haystack.indexOf(needle, at + needle.length)) found.push(at);

  return found;
}

// offsetOfLine is the offset of the first character of a zero-based line.
export function offsetOfLine(text: string, line: number): number {
  return text
    .split('\n')
    .slice(0, line)
    .reduce((sum, row) => sum + row.length + 1, 0);
}

// part: split

export const SPLIT_MIN = 25;

export function clampSplit(percent: number): number {
  if (!Number.isFinite(percent)) return 50;

  return Math.min(100 - SPLIT_MIN, Math.max(SPLIT_MIN, Math.round(percent)));
}

// part: autocomplete

// The routes of docs/dev/http.md. {coll} and {s} take the collection names.
// The reads with ?decrypt=1 ask for the plain values of encrypted fields.
export const ROUTES: [string, string][] = [
  ['GET', '/_health'],
  ['GET', '/_metrics'],
  ['GET', '/_collections'],
  ['GET', '/_stats'],
  ['GET', '/_size'],
  ['POST', '/_restore'],
  ['GET', '/_pitr'],
  ['POST', '/_blobs'],
  ['GET', '/_blobs/{ref}'],
  ['GET', '/_log'],
  ['PUT', '/_log'],
  ['GET', '/_cluster/settings'],
  ['PUT', '/_cluster/settings'],
  ['PUT', '/{coll}'],
  ['GET', '/{coll}'],
  ['DELETE', '/{coll}?confirm={coll}'],
  ['POST', '/{coll}/_truncate?confirm={coll}'],
  ['POST', '/{coll}/_rename'],
  ['POST', '/{coll}/_restore'],
  ['GET', '/{coll}/_pitr'],
  ['PUT', '/{coll}/_indexes/{field}'],
  ['DELETE', '/{coll}/_indexes/{field}'],
  ['GET', '/{coll}/_stats'],
  ['POST', '/{coll}/_compact'],
  ['GET', '/{coll}/_quarantine'],
  ['POST', '/{coll}/_doc'],
  ['POST', '/{coll}/_bulk'],
  ['GET', '/{coll}/_doc/{id}'],
  ['GET', '/{coll}/_doc/{id}?decrypt=1'],
  ['DELETE', '/{coll}/_doc/{id}'],
  ['POST', '/{coll}/_update/{id}'],
  ['POST', '/{coll}/_expire/{id}'],
  ['POST', '/{coll}/_persist/{id}'],
  ['GET', '/{coll}/_ttl/{id}'],
  ['POST', '/{coll}/_lock/{id}'],
  ['POST', '/{coll}/_unlock/{id}'],
  ['POST', '/{coll}/_lock'],
  ['POST', '/{coll}/_unlock'],
  ['POST', '/{coll}/_tx'],
  ['POST', '/{coll}/_search'],
  ['POST', '/{coll}/_count'],
  ['POST', '/{coll}/_aggregate'],
  ['POST', '/{coll}/_search?decrypt=1'],
  ['POST', '/{coll}/_count?decrypt=1'],
  ['POST', '/{coll}/_aggregate?decrypt=1'],
  ['POST', '/{coll}/_update_by_query'],
  ['POST', '/{coll}/_delete_by_query'],
  ['POST', '/{coll}/_ack'],
  ['GET', '/{s}/_streams'],
  ['POST', '/{s}/_streams/append'],
  ['POST', '/{s}/_streams/trim'],
  ['POST', '/{s}/_streams/read'],
  ['GET', '/{s}/_streams/groups'],
  ['PUT', '/{s}/_streams/groups/{g}'],
  ['DELETE', '/{s}/_streams/groups/{g}'],
  ['POST', '/{s}/_streams/groups/{g}/setid'],
  ['GET', '/{s}/_streams/groups/{g}/consumers'],
  ['PUT', '/{s}/_streams/groups/{g}/consumers/{c}'],
  ['DELETE', '/{s}/_streams/groups/{g}/consumers/{c}'],
  ['POST', '/{s}/_streams/groups/{g}/read'],
  ['POST', '/{s}/_streams/groups/{g}/ack'],
  ['POST', '/{s}/_streams/groups/{g}/nack'],
  ['POST', '/{s}/_streams/groups/{g}/pending'],
  ['POST', '/{s}/_streams/groups/{g}/claim'],
];

const UPDATE_OPERATORS = ['$set', '$unset', '$inc', '$push', '$pull', '$addToSet', '$setOnInsert'].map(name => ({ name, what: 'update' }));

export const BODY_OPERATORS: { name: string; what: string }[] = (() => {
  const all = [...LOGICAL_OPERATORS, ...FIELD_OPERATORS, ...UPDATE_OPERATORS, ...STAGES.map(stage => ({ name: stage.op, what: stage.what })), ...GROUP_OPERATORS];
  const byName = new Map<string, { name: string; what: string }>();

  for (const item of all) {
    if (!byName.has(item.name)) byName.set(item.name, item);
  }

  return [...byName.values()];
})();

export interface Completion {
  from: number;
  to: number;
  items: { label: string; detail: string }[];
}

const COMPLETE_LIMIT = 40;

export function routePaths(method: string, collections: string[]): string[] {
  const names = collections.length > 0 ? collections : ['{coll}'];
  const paths: string[] = [];

  for (const [verb, route] of ROUTES) {
    if (verb !== method) continue;

    if (!route.includes('{coll}') && !route.includes('{s}')) {
      paths.push(route);

      continue;
    }

    for (const name of names) paths.push(route.replaceAll('{coll}', name).replaceAll('{s}', name));
  }

  return paths;
}

// completeAt gives the choices at the caret: a route after the method of a
// request line, or a $operator key in a body.
export function completeAt(text: string, caret: number, collections: string[]): Completion | undefined {
  const from = text.lastIndexOf('\n', caret - 1) + 1;
  const lineEnd = text.indexOf('\n', caret);
  const head = text.slice(from, caret);
  const tail = text.slice(caret, lineEnd === -1 ? text.length : lineEnd);
  const route = /^\s*(GET|POST|PUT|DELETE)\s+(\S*)$/i.exec(head);

  if (route && tail.trim() === '') {
    const typed = route[2] as string;
    const wanted = (typed.startsWith('/') ? typed : `/${typed}`).toLowerCase();
    const items = routePaths((route[1] as string).toUpperCase(), collections)
      .filter(path => path.toLowerCase().startsWith(wanted) && path !== typed)
      .slice(0, COMPLETE_LIMIT)
      .map(path => ({ label: path, detail: '' }));

    if (items.length === 0) return undefined;

    return { from: caret - typed.length, to: caret, items };
  }

  const context = operatorKeyAt(text, caret);

  if (!context) return undefined;

  const items = BODY_OPERATORS.filter(item => item.name.startsWith(context.prefix) && item.name !== context.prefix)
    .slice(0, COMPLETE_LIMIT)
    .map(item => ({ label: item.name, detail: item.what }));

  if (items.length === 0) return undefined;

  return { from: context.from, to: context.to, items };
}

// part: copy as

export type Language = 'curl' | 'go' | 'ruby' | 'ts';

export interface Target {
  socket?: string;
  base?: string;
}

export interface SnippetInput extends Target {
  method: string;
  path: string;
  body?: string;
}

function address(target: Target): string {
  if (target.socket) return `unix://${target.socket}`;

  return (target.base ?? 'http://127.0.0.1:7070').replace(/^https?:\/\//, 'tcp://').replace(/\/+$/, '');
}

function compact(body: string): string {
  try {
    return JSON.stringify(JSON.parse(body));
  } catch {
    return body;
  }
}

function searchOf(input: SnippetInput): string | undefined {
  if (input.method !== 'POST') return undefined;

  return /^\/([^/?#]+)\/_search$/.exec(input.path)?.[1];
}

function goSnippet(input: SnippetInput): string | undefined {
  const coll = searchOf(input);

  if (!coll) return undefined;

  const token = input.socket ? '' : ', ndb.WithToken(os.Getenv("NDB_TOKEN"))';

  return [
    `db, err := ndb.Connect(${JSON.stringify(address(input))}${token})`,
    '',
    `coll, err := ndb.Collection[map[string]any](db, ${JSON.stringify(coll)})`,
    '',
    `page, err := coll.Search(ctx, []byte(\`${compact(input.body || '{}')}\`))`,
  ].join('\n');
}

function rubySnippet(input: SnippetInput): string | undefined {
  const coll = searchOf(input);

  if (!coll) return undefined;

  const token = input.socket ? '' : ', token: ENV["NDB_TOKEN"]';

  return [`db = Ndb.connect(${JSON.stringify(address(input))}${token})`, `coll = db.collection(${JSON.stringify(coll)})`, '', 'page = coll.search(JSON.parse(<<~JSON))', `  ${compact(input.body || '{}')}`, 'JSON'].join('\n');
}

function tsSnippet(input: SnippetInput): string {
  const token = input.socket ? '' : ', { token: process.env.NDB_TOKEN }';
  const connect = `const ndb = connect('${address(input)}'${token})`;
  const coll = searchOf(input);
  const body = input.body ? compact(input.body) : '';

  if (coll) return [connect, '', `const page = await ndb.collection('${coll}').search(${body || '{}'})`].join('\n');

  const args = body ? `, ${body}` : '';

  return [connect, '', `const res = await ndb.stream('${input.method}', '${input.path}'${args})`].join('\n');
}

// snippetOf builds the code of a request in one language. Go and Ruby have
// a call only for a search; for other routes it gives undefined.
export function snippetOf(language: Exclude<Language, 'curl'>, input: SnippetInput): string | undefined {
  if (language === 'go') return goSnippet(input);

  if (language === 'ruby') return rubySnippet(input);

  return tsSnippet(input);
}
