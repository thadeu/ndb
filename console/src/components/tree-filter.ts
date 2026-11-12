// Pure logic of the field filter of the JSON tree: which rows stay when the
// reader types in "Filter fields". No DOM here, so bun test can run it.
import { flatten, kindOf, pathId, type Row, scalarText, summaryOf } from './json-model';

// rowMatches tells if a row has the text in its key or in its scalar value.
// The index of an array item is not a key, so it never matches.
export function rowMatches(row: Row, needle: string, inArray: boolean): boolean {
  if (!inArray && row.key.toLowerCase().includes(needle)) return true;

  return !row.expandable && scalarText(row.value).toLowerCase().includes(needle);
}

// filterRows lists the rows of the tree for a filter text. A row stays when
// it matches, when a row below it matches (so the path to a match shows), or
// when a container above it matches by its key (so its content shows).
// Containers are open, except those in closed. Without a text, it is flatten.
export function filterRows(value: unknown, closed: ReadonlySet<string>, query: string): Row[] {
  const needle = query.trim().toLowerCase();

  if (needle === '') return flatten(value, closed);

  const all = flatten(value, new Set());
  const kinds = new Map(all.map(row => [row.id, row.kind]));
  const keep = new Set<string>();
  const whole: string[] = [];

  for (const row of all) {
    const parent = row.path.length > 1 ? kinds.get(pathId(row.path.slice(0, -1))) : kindOf(value);
    const below = whole.some(id => row.id.startsWith(`${id}\u0001`));

    if (below) {
      keep.add(row.id);

      continue;
    }

    if (!rowMatches(row, needle, parent === 'array')) continue;

    for (let length = 1; length <= row.path.length; length++) keep.add(pathId(row.path.slice(0, length)));

    if (row.expandable) whole.push(row.id);
  }

  const shut = [...closed];
  const hidden = (row: Row) => shut.some(id => row.id.startsWith(`${id}\u0001`));

  return all
    .filter(row => keep.has(row.id) && !hidden(row))
    .map(row => (closed.has(row.id) ? { ...row, open: false, summary: summaryOf(row.value, false) } : row));
}
