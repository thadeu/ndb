// Pure logic of the JSON tree: the kind of a value, the flat rows of a tree,
// edits that do not change the input, and the diff that becomes $set and
// $unset. No DOM here, so bun test can run it.

export type JsonKind = 'object' | 'array' | 'string' | 'number' | 'boolean' | 'null';
export type Path = string[];

export interface Row {
  id: string;
  path: Path;
  key: string;
  depth: number;
  kind: JsonKind;
  value: unknown;
  summary: string;
  open: boolean;
  expandable: boolean;
}

export interface Diff {
  set: Record<string, unknown>;
  unset: string[];
  blocked: string[];
}

const SEPARATOR = '\u0001';

export function kindOf(value: unknown): JsonKind {
  if (value === null || value === undefined) return 'null';

  if (Array.isArray(value)) return 'array';

  if (typeof value === 'object') return 'object';

  if (typeof value === 'number') return 'number';

  if (typeof value === 'boolean') return 'boolean';

  return 'string';
}

export function isContainer(value: unknown): value is Record<string, unknown> | unknown[] {
  const kind = kindOf(value);

  return kind === 'object' || kind === 'array';
}

export const pathId = (path: Path): string => path.join(SEPARATOR);

export function childrenOf(value: unknown): [string, unknown][] {
  if (Array.isArray(value)) return value.map((item, index) => [String(index), item]);

  if (kindOf(value) === 'object') return Object.entries(value as Record<string, unknown>);

  return [];
}

// scalarText is how a scalar shows in a row: strings keep their quotes.
export function scalarText(value: unknown): string {
  if (typeof value === 'string') return JSON.stringify(value);

  return value === undefined ? 'null' : JSON.stringify(value);
}

// summaryOf is the text of a container row. A closed container lists its
// first keys, an open one counts them.
export function summaryOf(value: unknown, open: boolean): string {
  const kind = kindOf(value);
  const entries = childrenOf(value);

  if (kind === 'array') return `[ ${entries.length} ${entries.length === 1 ? 'item' : 'items'} ]`;

  if (kind !== 'object') return scalarText(value);

  if (open) return `{ ${entries.length} ${entries.length === 1 ? 'key' : 'keys'} }`;

  const names = entries.slice(0, 4).map(([key]) => key);
  const more = entries.length > names.length ? ', …' : '';

  return `{ ${names.join(', ')}${more} }`;
}

// flatten lists the visible rows of a tree, depth first. A container is open
// unless its id is in closed. A container at the root has no row of its own.
export function flatten(value: unknown, closed: ReadonlySet<string>): Row[] {
  const rows: Row[] = [];

  const visit = (key: string, item: unknown, path: Path, depth: number) => {
    const id = pathId(path);
    const expandable = isContainer(item);
    const open = expandable && !closed.has(id);

    rows.push({ id, path, key, depth, kind: kindOf(item), value: item, summary: expandable ? summaryOf(item, open) : scalarText(item), open, expandable });

    if (!open) return;

    for (const [childKey, child] of childrenOf(item)) visit(childKey, child, [...path, childKey], depth + 1);
  };

  if (!isContainer(value)) {
    visit('', value, [], 0);

    return rows;
  }

  for (const [key, child] of childrenOf(value)) visit(key, child, [key], 0);

  return rows;
}

// containerIds lists the id of every container below the root, to collapse
// the whole tree.
export function containerIds(value: unknown): string[] {
  const ids: string[] = [];

  const visit = (item: unknown, path: Path) => {
    for (const [key, child] of childrenOf(item)) {
      if (!isContainer(child)) continue;

      ids.push(pathId([...path, key]));
      visit(child, [...path, key]);
    }
  };

  visit(value, []);

  return ids;
}

// parseScalar reads the text of an edited value as JSON, and keeps the text
// as a string when it is not JSON. A container is not a scalar: `{}` and `[]`
// are accepted, so a value can become a container.
export function parseScalar(text: string): unknown {
  try {
    return JSON.parse(text);
  } catch {
    return text;
  }
}

export function getIn(value: unknown, path: Path): unknown {
  let current = value;

  for (const key of path) {
    if (!isContainer(current)) return undefined;

    current = (current as Record<string, unknown>)[key];
  }

  return current;
}

function copyOf<T>(value: T): T {
  return structuredClone(value);
}

// setIn returns a copy of value with next at path.
export function setIn(value: unknown, path: Path, next: unknown): unknown {
  if (path.length === 0) return next;

  const root = copyOf(value) as Record<string, unknown>;
  let current: Record<string, unknown> = root;

  for (const key of path.slice(0, -1)) current = current[key] as Record<string, unknown>;

  current[path[path.length - 1] as string] = next;

  return root;
}

// removeIn returns a copy of value without the entry at path. An array entry
// is cut out, so the next items move up.
export function removeIn(value: unknown, path: Path): unknown {
  if (path.length === 0) return value;

  const root = copyOf(value) as Record<string, unknown>;
  let current: unknown = root;

  for (const key of path.slice(0, -1)) current = (current as Record<string, unknown>)[key];

  const last = path[path.length - 1] as string;

  if (Array.isArray(current)) current.splice(Number(last), 1);
  else delete (current as Record<string, unknown>)[last];

  return root;
}

export function deepEqual(a: unknown, b: unknown): boolean {
  if (a === b) return true;

  const kind = kindOf(a);

  if (kind !== kindOf(b) || !isContainer(a) || !isContainer(b)) return false;

  const left = childrenOf(a);
  const right = childrenOf(b);

  if (left.length !== right.length) return false;

  return left.every(([key, item]) => deepEqual(item, (b as Record<string, unknown>)[key]));
}

// addField adds key to the container at path. In an array the key is
// ignored and the value goes to the end.
export function addField(value: unknown, path: Path, key: string, next: unknown): unknown {
  const target = getIn(value, path);

  if (Array.isArray(target)) return setIn(value, path, [...target, next]);

  return setIn(value, [...path, key], next);
}

const IMMUTABLE = new Set(['_id', '_v']);

// A key can be a part of a dot path only when it has no dot, does not start
// with $, and is not empty.
function plainKey(key: string): boolean {
  return key !== '' && !key.includes('.') && !key.startsWith('$');
}

function plainObject(value: unknown): value is Record<string, unknown> {
  return kindOf(value) === 'object';
}

// diffDocs compares two documents and gives the update for the difference:
// changed and added paths go to set (dot paths), removed paths go to unset.
// An array is always written whole. A key that cannot be a dot path makes
// its parent object be written whole; at the root it is blocked, because
// there is no parent. _id and _v are never part of the diff.
export function diffDocs(before: Record<string, unknown>, after: Record<string, unknown>): Diff {
  const diff: Diff = { set: {}, unset: [], blocked: [] };

  const walk = (prefix: string[], left: Record<string, unknown>, right: Record<string, unknown>) => {
    const keys = new Set([...Object.keys(left), ...Object.keys(right)]);

    for (const key of keys) {
      if (prefix.length === 0 && IMMUTABLE.has(key)) continue;

      const path = [...prefix, key];
      const name = path.join('.');
      const had = key in left;
      const has = key in right;

      if (!plainKey(key)) {
        if (!deepEqual(left[key], right[key]) || had !== has) diff.blocked.push(name);

        continue;
      }

      if (!has) {
        diff.unset.push(name);

        continue;
      }

      if (!had) {
        diff.set[name] = right[key];

        continue;
      }

      const a = left[key];
      const b = right[key];

      if (plainObject(a) && plainObject(b)) {
        nested(path, a, b);

        continue;
      }

      if (!deepEqual(a, b)) diff.set[name] = b;
    }
  };

  const nested = (path: string[], a: Record<string, unknown>, b: Record<string, unknown>) => {
    const keys = [...Object.keys(a), ...Object.keys(b)];

    if (!keys.every(plainKey)) {
      if (!deepEqual(a, b)) diff.set[path.join('.')] = b;

      return;
    }

    walk(path, a, b);
  };

  walk([], before, after);

  return diff;
}

export function isEmptyDiff(diff: Diff): boolean {
  return Object.keys(diff.set).length === 0 && diff.unset.length === 0;
}

// updateBody is the body of POST /{coll}/_update/{_id}: the version guard in
// query, the operators in update. An operator with no field is left out.
export function updateBody(version: number, diff: Diff): Record<string, unknown> {
  const update: Record<string, unknown> = {};

  if (Object.keys(diff.set).length > 0) update.$set = diff.set;

  if (diff.unset.length > 0) update.$unset = Object.fromEntries(diff.unset.map(name => [name, true]));

  return { query: { _v: version }, update };
}

// describeDiff is the label of the Save button.
export function describeDiff(diff: Diff): string {
  const parts: string[] = [];
  const set = Object.keys(diff.set);

  if (set.length > 0) parts.push(`$set ${set.length === 1 ? set[0] : `${set.length} fields`}`);

  if (diff.unset.length > 0) parts.push(`$unset ${diff.unset.length === 1 ? diff.unset[0] : `${diff.unset.length} fields`}`);

  return parts.join(' · ');
}

// byteSize is the size of the document as JSON text.
export function byteSize(value: unknown): number {
  return new TextEncoder().encode(JSON.stringify(value)).length;
}
