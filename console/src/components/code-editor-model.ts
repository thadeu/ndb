// Pure logic of the code editor (src/components/ui/code-editor.tsx): the
// colors of a JSON text, the format, the parse error, the height in lines and
// the place of an operator that the user is typing. It imports nothing from
// the browser, so it has unit tests.
import { type Token as LineToken, tokenize } from '@/components/devtools-blocks';

import type { Edit } from './editor-keys';

export type Tone = 'key' | 'op' | 'string' | 'number' | 'bool' | 'plain';

export interface Token {
  text: string;
  tone: Tone;
}

export const MIN_ROWS = 3;
export const MAX_ROWS = 16;

const LITERALS = new Set(['true', 'false', 'null']);

function toneOf(token: LineToken): Tone {
  if (token.tone === 'number') return LITERALS.has(token.text) ? 'bool' : 'number';

  if (token.tone === 'key' || token.tone === 'op' || token.tone === 'string') return token.tone;

  return 'plain';
}

// highlightLine colors one line of JSON with the tokenizer of Dev Tools. A
// key that starts with $ is an operator, and true, false and null get their
// own tone.
export function highlightLine(line: string): Token[] {
  return tokenize(line).map(token => ({ text: token.text, tone: toneOf(token) }));
}

export function highlight(text: string): Token[][] {
  return text.split('\n').map(highlightLine);
}

// editorRows is the height of the editor in lines: it grows with the text,
// between a minimum and a maximum. Over the maximum the editor scrolls.
export function editorRows(text: string, min = MIN_ROWS, max = MAX_ROWS): number {
  return Math.min(Math.max(text.split('\n').length, min), max);
}

// formatJson pretty-prints valid JSON, or gives undefined.
export function formatJson(text: string): string | undefined {
  try {
    return JSON.stringify(JSON.parse(text), null, 2);
  } catch {
    return undefined;
  }
}

export interface JsonError {
  message: string;
  line?: number;
  column?: number;
}

function placeOf(text: string, offset: number): { line: number; column: number } {
  const before = text.slice(0, offset).split('\n');

  return { line: before.length, column: before[before.length - 1]!.length + 1 };
}

// describeError reads the message of a JSON.parse error. Each engine writes
// the position in its own way, and JavaScriptCore gives none:
//   V8:      Unexpected token } in JSON at position 12 (line 2 column 3)
//   Firefox: JSON.parse: expected ',' at line 2 column 3 of the JSON data
//   JSC:     JSON Parse error: Expected '}'
export function describeError(raw: string, text: string): JsonError {
  const lineColumn = /line (\d+) column (\d+)/.exec(raw);
  const position = /position (\d+)/.exec(raw);

  const message = raw
    .replace(/^JSON Parse error:\s*/, '')
    .replace(/^JSON\.parse:\s*/, '')
    .replace(/\s*\(line \d+ column \d+\)/, '')
    .replace(/\s*(in JSON )?at position \d+/, '')
    .replace(/\s*at line \d+ column \d+ of the JSON data/, '')
    .replace(/,?\s*"[^"]*"\.{3} is not valid JSON$/, '')
    .trim();

  if (lineColumn) return { message, line: Number(lineColumn[1]), column: Number(lineColumn[2]) };

  if (position) return { message, ...placeOf(text, Number(position[1])) };

  return { message };
}

// jsonError gives undefined for valid JSON, else what the parser says.
export function jsonError(text: string): JsonError | undefined {
  if (text.trim() === '') return { message: 'The text is empty' };

  try {
    JSON.parse(text);

    return undefined;
  } catch (error) {
    return describeError(error instanceof Error ? error.message : String(error), text);
  }
}

export function errorText(error: JsonError): string {
  const message = error.message.replace(/\.$/, '');
  const place = error.line === undefined ? '' : ` (line ${error.line}, column ${error.column})`;

  return `This is not valid JSON: ${message}${place}.`;
}

export interface KeyContext {
  // from is the offset of the $, and to is the caret.
  from: number;
  to: number;
  prefix: string;
  // depth is the number of open objects and arrays around the key.
  depth: number;
}

// operatorKeyAt finds a key that starts with "$ and ends at the caret, such
// as {"n": {"$su|. It gives undefined in a value ("_id": "$status"), in an
// array, or inside another string.
export function operatorKeyAt(text: string, caret: number): KeyContext | undefined {
  const typed = /"(\$[A-Za-z]*)$/.exec(text.slice(0, caret));

  if (!typed) return undefined;

  const quote = typed.index;
  const stack: string[] = [];

  let inString = false;
  let last = '';

  for (let index = 0; index < quote; index++) {
    const char = text[index]!;

    if (inString) {
      if (char === '\\') index++;
      else if (char === '"') inString = false;

      continue;
    }

    if (char === '"') inString = true;
    else if (char === '{' || char === '[') stack.push(char);
    else if (char === '}' || char === ']') stack.pop();

    if (!/\s/.test(char)) last = char;
  }

  if (inString || stack[stack.length - 1] !== '{') return undefined;

  if (last !== '{' && last !== ',') return undefined;

  return { from: quote + 1, to: caret, prefix: typed[1]!, depth: stack.length };
}

// applyCompletion writes the picked name over the typed prefix. The caret
// goes after the closing quote, and a colon is added when there is none.
export function applyCompletion(text: string, context: KeyContext, name: string): Edit {
  const head = text.slice(0, context.from) + name;
  const rest = text.slice(context.to).replace(/^[$A-Za-z]*/, '');

  const closed = rest.startsWith('"') ? rest.slice(1) : rest;
  const colon = closed.startsWith(' ') ? ':' : ': ';
  const tail = /^\s*:/.test(closed) ? closed : colon + closed;

  const next = `${head}"${tail}`;
  const caret = head.length + 1 + (/^\s*: ?/.exec(tail)?.[0].length ?? 0);

  return { text: next, start: caret, end: caret };
}
