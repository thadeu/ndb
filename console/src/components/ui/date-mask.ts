// A positional mask for dates and times, written as YYYY-MM-DD and
// YYYY-MM-DD HH:MM:SS. Each digit has its place: typing writes the place at
// the caret and jumps over the separators, typing in the middle overwrites,
// and a digit that cannot start a two-digit part (a 4 as the first digit of a
// month) gets a 0 in front. Complete parts are kept in range: month 01-12,
// day up to the length of the month, hour 00-23, minute and second 00-59.
//
// Pure functions of { value, caret }: the date picker calls them from its
// input events, the tests call them alone.

export type MaskKind = 'date' | 'datetime';

export interface MaskState {
  value: string;
  caret: number;
}

interface Part {
  start: number;
  len: number;
  min: number;
  max: number;
  first: number;
}

const TEMPLATE: Record<MaskKind, string> = { date: 'YYYY-MM-DD', datetime: 'YYYY-MM-DD HH:MM:SS' };

const PARTS: Part[] = [
  { start: 0, len: 4, min: 0, max: 9999, first: 9 },
  { start: 5, len: 2, min: 1, max: 12, first: 1 },
  { start: 8, len: 2, min: 1, max: 31, first: 3 },
  { start: 11, len: 2, min: 0, max: 23, first: 2 },
  { start: 14, len: 2, min: 0, max: 59, first: 5 },
  { start: 17, len: 2, min: 0, max: 59, first: 5 },
];

export function template(kind: MaskKind): string {
  return TEMPLATE[kind];
}

function parts(kind: MaskKind): Part[] {
  return kind === 'date' ? PARTS.slice(0, 3) : PARTS;
}

function isSeparator(tpl: string, pos: number): boolean {
  return pos < tpl.length && !/[YMDHS]/.test(tpl[pos] ?? '');
}

function partAt(kind: MaskKind, pos: number): Part | undefined {
  return parts(kind).find(p => pos >= p.start && pos < p.start + p.len);
}

function daysIn(year: number, month: number): number {
  return new Date(Date.UTC(year, month, 0)).getUTCDate();
}

function maxOf(part: Part, value: string): number {
  if (part.start !== 8 || value.length < 7) return part.max;

  return daysIn(Number(value.slice(0, 4)), Number(value.slice(5, 7)));
}

// clamp keeps every complete part of value in its range.
function clamp(value: string, kind: MaskKind): string {
  let out = value;

  for (const part of parts(kind)) {
    const text = out.slice(part.start, part.start + part.len);

    if (text.length < part.len || part.len === 4) continue;

    const n = Math.min(Math.max(Number(text), part.min), maxOf(part, out));

    out = out.slice(0, part.start) + String(n).padStart(part.len, '0') + out.slice(part.start + part.len);
  }

  return out;
}

function write(value: string, pos: number, text: string, tpl: string): string {
  let out = value;

  while (out.length < pos) out += isSeparator(tpl, out.length) ? tpl[out.length] : '0';

  return out.slice(0, pos) + text + out.slice(pos + text.length);
}

// typeDigit writes one digit at the caret.
export function typeDigit(state: MaskState, digit: string, kind: MaskKind): MaskState {
  const tpl = TEMPLATE[kind];

  if (!/^\d$/.test(digit)) return state;

  let pos = state.caret;

  while (isSeparator(tpl, pos)) pos++;

  if (pos >= tpl.length) return state;

  const part = partAt(kind, pos);
  const text = part && part.len === 2 && pos === part.start && Number(digit) > part.first ? `0${digit}` : digit;

  let value = clamp(write(state.value, pos, text, tpl), kind);
  let caret = pos + text.length;

  if (caret === value.length && isSeparator(tpl, caret)) {
    value += tpl[caret];
    caret++;
  }

  return { value, caret };
}

function trimSeparators(value: string, tpl: string): string {
  let out = value;

  while (out.length > 0 && isSeparator(tpl, out.length - 1)) out = out.slice(0, -1);

  return out;
}

// erase is Backspace (or Delete with forward): at the end it removes the last
// digit; in the middle it sets the digit to 0, so the other places stay.
export function erase(state: MaskState, kind: MaskKind, forward = false): MaskState {
  const tpl = TEMPLATE[kind];
  const { value } = state;

  if (!forward && state.caret >= value.length) {
    const cut = trimSeparators(trimSeparators(value, tpl).slice(0, -1), tpl);

    return { value: cut, caret: cut.length };
  }

  let pos = forward ? state.caret : state.caret - 1;

  while (pos >= 0 && pos < value.length && isSeparator(tpl, pos)) pos += forward ? 1 : -1;

  if (pos < 0 || pos >= value.length) return state;

  if (pos === value.length - 1) {
    const cut = trimSeparators(value.slice(0, pos), tpl);

    return { value: cut, caret: cut.length };
  }

  return { value: value.slice(0, pos) + '0' + value.slice(pos + 1), caret: forward ? state.caret : pos };
}

// step adds delta to the complete part at the caret (ArrowUp, ArrowDown),
// wrapping inside its range.
export function step(state: MaskState, delta: number, kind: MaskKind): MaskState {
  const tpl = TEMPLATE[kind];
  const pos = isSeparator(tpl, state.caret) || state.caret >= tpl.length ? state.caret - 1 : state.caret;
  const part = partAt(kind, pos);

  if (!part || state.value.length < part.start + part.len) return state;

  const max = maxOf(part, state.value);
  const span = max - part.min + 1;
  const n = ((((Number(state.value.slice(part.start, part.start + part.len)) - part.min + delta) % span) + span) % span) + part.min;
  const value = clamp(state.value.slice(0, part.start) + String(n).padStart(part.len, '0') + state.value.slice(part.start + part.len), kind);

  return { value, caret: state.caret };
}

// normalize reads a pasted or typed text: groups of digits fill the parts in
// order (2026-1-5 9:04 works), and one long run of digits fills the places.
export function normalize(raw: string, kind: MaskKind): string {
  const groups = raw.match(/\d+/g) ?? [];

  if (groups.length > 1) {
    let value = '';

    parts(kind).forEach((part, i) => {
      const group = groups[i];

      if (group === undefined) return;

      const text = part.len === 4 ? group.slice(0, 4).padStart(4, '0') : group.slice(-2).padStart(2, '0');

      value = write(value, part.start, text, TEMPLATE[kind]);
    });

    return clamp(value, kind);
  }

  let state: MaskState = { value: '', caret: 0 };

  for (const digit of groups.join('')) state = typeDigit(state, digit, kind);

  return state.value;
}

export function complete(value: string, kind: MaskKind): boolean {
  return value.length === TEMPLATE[kind].length;
}

// parse reads a complete value as a UTC time; anything else is null.
export function parse(value: string, kind: MaskKind): Date | null {
  if (!complete(value, kind)) return null;

  const n = (start: number, len: number) => Number(value.slice(start, start + len));
  const time = kind === 'datetime' ? [n(11, 2), n(14, 2), n(17, 2)] : [0, 0, 0];

  return new Date(Date.UTC(n(0, 4), n(5, 2) - 1, n(8, 2), time[0], time[1], time[2]));
}

// format writes a time in UTC in the shape of kind.
export function format(date: Date, kind: MaskKind): string {
  const iso = date.toISOString();

  return kind === 'date' ? iso.slice(0, 10) : `${iso.slice(0, 10)} ${iso.slice(11, 19)}`;
}

// pattern is the regexp of a complete value, for the input pattern attribute
// (the form check without the script).
export function pattern(kind: MaskKind): string {
  return kind === 'date' ? '\\d{4}-\\d{2}-\\d{2}' : '\\d{4}-\\d{2}-\\d{2}[ T]\\d{2}:\\d{2}:\\d{2}';
}
