// Pure logic of the encrypted fields in the Documents tab: which fields of a
// collection are encrypted or blind, the mask that takes the place of a
// sealed value in the page, and the conditions that a filter row may use on
// such a field. No DOM here, so bun test can run it.

import type { Diff } from './json-model';
import { isSealed } from './json-model';

// SEALED_MASK is the text that the page has in place of a sealed value. The
// stored "ndb:e1:..." text and the plain value never go into the page.
export const SEALED_MASK = '[encrypted]';

// SHOW_SECONDS is how long a value that the user asked to see stays shown.
export const SHOW_SECONDS = 30;

export const DOCS_URL = 'https://ndb.clowk.in/encryption';

export interface Sealing {
  blind: Record<string, string>;
  encrypted: string[];
}

export const NO_SEALING: Sealing = { blind: {}, encrypted: [] };

function isRecord(value: unknown): value is Record<string, unknown> {
  return value !== null && typeof value === 'object' && !Array.isArray(value);
}

// sealingOf reads the encrypted and blind fields of a collection definition
// (GET /{coll}). blind is an object of field and type, or a list of exact
// fields.
export function sealingOf(definition: unknown): Sealing {
  if (!isRecord(definition)) return NO_SEALING;

  const raw = definition.blind;
  const list = Array.isArray(definition.encrypted) ? definition.encrypted : [];

  const blind: Record<string, string> = {};

  if (Array.isArray(raw)) {
    for (const name of raw) blind[String(name)] = 'exact';
  } else if (isRecord(raw)) {
    for (const [name, type] of Object.entries(raw)) blind[name] = String(type || 'exact');
  }

  const encrypted = list.map(String).filter(name => !(name in blind));

  return { blind, encrypted };
}

// sealedNames lists every field that the daemon encrypts: a blind field is
// also encrypted.
export function sealedNames(sealing: Sealing): string[] {
  return [...Object.keys(sealing.blind), ...sealing.encrypted];
}

// maskDoc gives the document with each sealed top-level value replaced by
// SEALED_MASK, and the names of those fields.
export function maskDoc(doc: Record<string, unknown>): { doc: Record<string, unknown>; sealed: string[] } {
  const sealed: string[] = [];
  const masked: Record<string, unknown> = {};

  for (const [key, value] of Object.entries(doc)) {
    if (isSealed(value)) sealed.push(key);

    masked[key] = isSealed(value) ? SEALED_MASK : value;
  }

  return { doc: masked, sealed };
}

// isMasked tells if a top-level value is the mask of a sealed field.
export function isMasked(path: string[], value: unknown, sealed: ReadonlySet<string>): boolean {
  return path.length === 1 && sealed.has(path[0] as string) && value === SEALED_MASK;
}

// keepSealed drops from a diff each sealed field that still has the mask, so
// a save never writes the text "[encrypted]" over a stored value.
export function keepSealed(diff: Diff, sealed: readonly string[]): Diff {
  const set = Object.fromEntries(Object.entries(diff.set).filter(([name, value]) => !(sealed.includes(name) && value === SEALED_MASK)));

  return { ...diff, set };
}

// shownValue is the value of one field of a document read with
// ?decrypt=1. A value that is still sealed means the connection has no
// decrypt right.
export function shownValue(doc: Record<string, unknown>, field: string): { value?: unknown; denied?: boolean; missing?: boolean } {
  if (!(field in doc)) return { missing: true };

  const value = doc[field];

  if (isSealed(value)) return { denied: true };

  return { value };
}

export function deniedText(coll: string): string {
  return `This connection cannot decrypt ${coll}. Use the unix socket, the admin token, or a token with decrypt on ${coll}.`;
}

const BLIND_OPS = ['eq', 'ne', 'in', 'nin', 'exists', 'missing'];
const ENCRYPTED_OPS = ['exists', 'missing'];

export type SealKind = 'blind' | 'encrypted' | undefined;

export function sealKind(field: string, sealing: Sealing): SealKind {
  const name = field.trim();

  if (name in sealing.blind) return 'blind';

  return sealing.encrypted.includes(name) ? 'encrypted' : undefined;
}

// allowedOps lists the conditions of a filter row on this field, or
// undefined when every condition is allowed.
export function allowedOps(field: string, sealing: Sealing): string[] | undefined {
  const kind = sealKind(field, sealing);

  if (kind === 'blind') return BLIND_OPS;

  return kind === 'encrypted' ? ENCRYPTED_OPS : undefined;
}

const NORMAL: Record<string, string> = {
  exact: 'as typed',
  lower: 'in lower case, without spaces at the ends',
  upper: 'in upper case, without spaces at the ends',
  numeric: 'with only the digits, so 390.533.447-05 is 39053344705',
};

// sealHint is the short text under a filter row on an encrypted field.
export function sealHint(field: string, sealing: Sealing): string | undefined {
  const kind = sealKind(field, sealing);

  if (kind === 'encrypted') return `${field.trim()} is encrypted. You can only filter on whether it exists.`;

  if (kind !== 'blind') return undefined;

  const type = sealing.blind[field.trim()] ?? 'exact';

  return `${field.trim()} is a blind field: equal, not equal and lists only. ndb compares the value ${NORMAL[type] ?? 'after it normalizes it'}.`;
}

// checkRows gives a message when a row uses a condition that its field
// does not allow, before ndb refuses the query.
export function checkRows(rows: { field: string; op: string }[], sealing: Sealing): string | undefined {
  for (const row of rows) {
    const allowed = allowedOps(row.field, sealing);

    if (row.field.trim() === '' || !allowed || allowed.includes(row.op)) continue;

    return sealHint(row.field, sealing);
  }

  return undefined;
}
