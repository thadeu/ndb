// Pure key handling for the Dev Tools editor: auto-indent, closing pairs and
// Tab indent. Each function takes the text and the selection, and returns the
// new text and selection, or null when the browser should handle the key.

export interface Edit {
  text: string;
  start: number;
  end: number;
}

export interface KeyInput {
  key: string;
  shift: boolean;
}

const INDENT = '  ';
const PAIRS: Record<string, string> = { '{': '}', '[': ']', '"': '"' };
const CLOSERS = new Set(['}', ']']);

function lineStart(text: string, offset: number): number {
  return text.lastIndexOf('\n', offset - 1) + 1;
}

function indentOf(line: string): string {
  return /^[ \t]*/.exec(line)?.[0] ?? '';
}

function replace(text: string, start: number, end: number, insert: string, caret: number): Edit {
  return { text: text.slice(0, start) + insert + text.slice(end), start: caret, end: caret };
}

// enter keeps the indent of the line. After { or [ it adds one level, and
// between a pair like {} it puts the closer on its own line.
function enter(text: string, start: number, end: number): Edit {
  const from = lineStart(text, start);
  const base = indentOf(text.slice(from, start));
  const before = text.slice(from, start).trimEnd().slice(-1);
  const after = text.slice(end, end + 1);

  if (before === '{' || before === '[') {
    const inner = `\n${base}${INDENT}`;

    if (after === PAIRS[before]) return replace(text, start, end, `${inner}\n${base}`, start + inner.length);

    return replace(text, start, end, inner, start + inner.length);
  }

  return replace(text, start, end, `\n${base}`, start + base.length + 1);
}

// closer removes one indent level when } or ] is typed on a blank line, and
// steps over the same closer when it is already the next character.
function closer(text: string, start: number, end: number, key: string): Edit | null {
  if (start === end && text[start] === key) return { text, start: start + 1, end: start + 1 };

  const from = lineStart(text, start);
  const head = text.slice(from, start);

  if (start !== end || head.trim() !== '' || head.length < INDENT.length) return null;

  const cut = head.slice(0, -INDENT.length);

  return replace(text, from, start, cut + key, from + cut.length + 1);
}

// pair closes { [ and ", wraps a selection, and steps over a closing quote.
function pair(text: string, start: number, end: number, key: string): Edit | null {
  const close = PAIRS[key];

  if (!close) return null;

  if (start !== end) {
    const inner = text.slice(start, end);

    return { text: text.slice(0, start) + key + inner + close + text.slice(end), start: start + 1, end: end + 1 };
  }

  if (key === '"' && text[start] === '"') return { text, start: start + 1, end: start + 1 };

  const next = text[start] ?? '';

  if (next !== '' && !/[\s,:}\]]/.test(next)) return null;

  return replace(text, start, end, key + close, start + 1);
}

// backspace between an empty pair removes both characters.
function backspace(text: string, start: number, end: number): Edit | null {
  if (start !== end || start === 0) return null;

  const open = text[start - 1] ?? '';

  if (PAIRS[open] === undefined || text[start] !== PAIRS[open]) return null;

  return replace(text, start - 1, start + 1, '', start - 1);
}

// tab indents, and shift+tab unindents, every line of the selection. A tab
// with no selection inserts two spaces.
function tab(text: string, start: number, end: number, shift: boolean): Edit {
  if (start === end && !shift) return replace(text, start, end, INDENT, start + INDENT.length);

  const from = lineStart(text, start);
  const to = text.indexOf('\n', end === start ? end : end - 1);
  const stop = to === -1 ? text.length : to;
  const lines = text.slice(from, stop).split('\n');

  const changed = lines.map(line => {
    if (!shift) return INDENT + line;

    return line.startsWith(INDENT) ? line.slice(INDENT.length) : line.replace(/^ /, '');
  });

  const firstDelta = changed[0]!.length - lines[0]!.length;
  const totalDelta = changed.join('\n').length - lines.join('\n').length;
  const next = text.slice(0, from) + changed.join('\n') + text.slice(stop);

  return { text: next, start: Math.max(from, start + firstDelta), end: Math.max(from, end + totalDelta) };
}

export function editKey(text: string, start: number, end: number, input: KeyInput): Edit | null {
  switch (input.key) {
    case 'Enter':
      return input.shift ? null : enter(text, start, end);

    case 'Tab':
      return tab(text, start, end, input.shift);

    case 'Backspace':
      return backspace(text, start, end);

    default:
      if (CLOSERS.has(input.key)) {
        const edit = closer(text, start, end, input.key);

        if (edit) return edit;
      }

      return pair(text, start, end, input.key);
  }
}
