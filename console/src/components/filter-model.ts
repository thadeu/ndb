// The filter builder of the Documents tab. Each row is a field, an operator,
// a value type, and a value. buildWhere turns the rows into the MongoDB
// filter that ndb takes, and searchBody shows the full _search body, so the
// page can show the JSON before it is sent.

export interface FilterRow {
  field: string;
  op: string;
  type: string;
  value: string;
}

export interface FindQuery {
  mode: 'builder' | 'json';
  rows: FilterRow[];
  json: string;
  sort: string;
  dir: 'asc' | 'desc';
  fields: string;
  limit: number;
  per: string;
  perLimit: number;
}

export interface Chip {
  index: number;
  field: string;
  op: string;
  value: string;
  label: string;
}

export const OPERATORS: { id: string; label: string }[] = [
  { id: 'eq', label: 'Equal to' },
  { id: 'ne', label: 'Not equal to' },
  { id: 'gt', label: 'Greater than' },
  { id: 'gte', label: 'Greater than or equal to' },
  { id: 'lt', label: 'Less than' },
  { id: 'lte', label: 'Less than or equal to' },
  { id: 'in', label: 'In list' },
  { id: 'nin', label: 'Not in list' },
  { id: 'prefix', label: 'Begins with' },
  { id: 'regex', label: 'Matches regex' },
  { id: 'exists', label: 'Exists' },
  { id: 'missing', label: 'Does not exist' },
];

export const TYPES: { id: string; label: string }[] = [
  { id: 'auto', label: 'Auto' },
  { id: 'string', label: 'String' },
  { id: 'number', label: 'Number' },
  { id: 'boolean', label: 'Boolean' },
  { id: 'date', label: 'Date or date math' },
  { id: 'null', label: 'Null' },
];

export const LIMITS = [25, 50, 100, 250];
export const DEFAULT_LIMIT = 25;
export const DEFAULT_PER_LIMIT = 5;

// The short form of each operator, for the chip of a filter.
const SYMBOLS: Record<string, string> = {
  eq: '=',
  ne: '≠',
  gt: '>',
  gte: '≥',
  lt: '<',
  lte: '≤',
  in: 'in',
  nin: 'not in',
  prefix: 'begins with',
  regex: 'matches',
  exists: 'exists',
  missing: 'does not exist',
};

const NO_VALUE = new Set(['exists', 'missing']);

export function needsValue(op: string): boolean {
  return !NO_VALUE.has(op);
}

export function emptyRow(): FilterRow {
  return { field: '', op: 'eq', type: 'auto', value: '' };
}

// readFind reads the find form from the query string. The arrays f, op, t
// and v hold one entry for each filter row.
export function readFind(params: URLSearchParams): FindQuery {
  const fields = params.getAll('f');
  const ops = params.getAll('op');
  const types = params.getAll('t');
  const values = params.getAll('v');

  const rows = fields.map((field, i) => ({ field, op: ops[i] ?? 'eq', type: types[i] || 'auto', value: values[i] ?? '' }));

  const limit = Number(params.get('limit'));
  const perLimit = Number(params.get('per_limit'));

  return {
    mode: params.get('mode') === 'json' ? 'json' : 'builder',
    rows: rows.length > 0 ? rows : [emptyRow()],
    json: params.get('where') ?? '',
    sort: params.get('sort') ?? '',
    dir: params.get('dir') === 'asc' ? 'asc' : 'desc',
    fields: params.get('fields') ?? '',
    limit: LIMITS.includes(limit) ? limit : DEFAULT_LIMIT,
    per: (params.get('per') ?? '').trim(),
    perLimit: Number.isInteger(perLimit) && perLimit >= 1 ? perLimit : DEFAULT_PER_LIMIT,
  };
}

// findParams writes a find query back to the query string. Empty rows and
// default values stay out, so a link stays short.
export function findParams(query: FindQuery): URLSearchParams {
  const params = new URLSearchParams();

  if (query.mode === 'json') {
    params.set('mode', 'json');

    if (query.json) params.set('where', query.json);
  } else {
    for (const row of query.rows.filter(item => item.field.trim() !== '')) {
      params.append('f', row.field);
      params.append('op', row.op);
      params.append('t', row.type);
      params.append('v', row.value);
    }
  }

  if (query.sort) params.set('sort', query.sort);

  if (query.sort && query.dir === 'asc') params.set('dir', 'asc');

  if (query.fields) params.set('fields', query.fields);

  if (query.limit !== DEFAULT_LIMIT) params.set('limit', String(query.limit));

  if (query.per) params.set('per', query.per);

  if (query.per && query.perLimit !== DEFAULT_PER_LIMIT) params.set('per_limit', String(query.perLimit));

  return params;
}

// chipsOf is the short text of each filter row that has a field, such as
// "status in paid, new". index is the place of the row in rows.
export function chipsOf(rows: FilterRow[]): Chip[] {
  const chips: Chip[] = [];

  rows.forEach((row, index) => {
    const field = row.field.trim();

    if (field === '') return;

    const op = SYMBOLS[row.op] ?? row.op;
    const value = chipValue(row);

    chips.push({ index, field, op, value, label: [field, op, value].filter(Boolean).join(' ') });
  });

  return chips;
}

function chipValue(row: FilterRow): string {
  if (!needsValue(row.op)) return '';

  if (row.type === 'null') return 'null';

  if (row.op === 'in' || row.op === 'nin') {
    return row.value
      .split(',')
      .map(item => item.trim())
      .join(', ');
  }

  return row.value;
}

// withoutRow is the same find query without one filter row. The link of the
// × of a chip uses it.
export function withoutRow(query: FindQuery, index: number): FindQuery {
  return { ...query, rows: query.rows.filter((_, i) => i !== index) };
}

// autoTyped reads the value as the user means it: a number, true or false,
// null, or else a string. Quotes force a string, such as "123".
export function autoTyped(text: string): unknown {
  const value = text.trim();

  if (/^".*"$/.test(value)) return value.slice(1, -1);

  if (value === 'true' || value === 'false') return value === 'true';

  if (value === 'null') return null;

  if (/^-?(\d+\.?\d*|\.\d+)(e[+-]?\d+)?$/i.test(value)) return Number(value);

  return text;
}

function typed(text: string, type: string): unknown {
  if (type === 'auto') return autoTyped(text);

  if (type === 'null') return null;

  if (type === 'boolean') {
    if (text === 'true' || text === 'false') return text === 'true';

    throw new Error(`"${text}" is not true or false.`);
  }

  if (type === 'number') {
    const number = Number(text);

    if (text.trim() === '' || Number.isNaN(number)) throw new Error(`"${text}" is not a number.`);

    return number;
  }

  return text;
}

function escapeRegex(text: string): string {
  return text.replace(/[.*+?^${}()|[\]\\]/g, '\\$&');
}

function condition(row: FilterRow): unknown {
  if (row.op === 'exists') return { $exists: true };

  if (row.op === 'missing') return { $exists: false };

  if (row.op === 'in' || row.op === 'nin') {
    const list = row.value.split(',').map(item => typed(item.trim(), row.type));

    return { [`$${row.op}`]: list };
  }

  if (row.op === 'prefix') return { $regex: `^${escapeRegex(row.value)}` };

  if (row.op === 'regex') return { $regex: row.value };

  const value = typed(row.value, row.type);

  if (row.op === 'eq') return value;

  return { [`$${row.op}`]: value };
}

// buildWhere turns the rows into one filter. Rows on the same field join in
// one object, such as {"ts":{"$gte":1,"$lt":9}}. A row without a field is
// left out.
export function buildWhere(rows: FilterRow[]): { where: Record<string, unknown>; error?: string } {
  const where: Record<string, unknown> = {};

  try {
    for (const row of rows) {
      const field = row.field.trim();

      if (field === '') continue;

      const next = condition(row);
      const before = where[field];
      const both = isOperators(before) && isOperators(next);

      where[field] = both ? { ...before, ...next } : next;
    }
  } catch (error) {
    return { where, error: error instanceof Error ? error.message : 'The filter is not valid.' };
  }

  return { where };
}

function isOperators(value: unknown): value is Record<string, unknown> {
  return value !== null && typeof value === 'object' && !Array.isArray(value) && Object.keys(value).every(key => key.startsWith('$'));
}

// searchBody is the body of POST /{coll}/_search for this find query. per
// gives the first rows of each value, so it takes the place of limit: ndb
// refuses the two together.
export function searchBody(query: FindQuery, where: Record<string, unknown>): Record<string, unknown> {
  const body: Record<string, unknown> = { query: where };

  if (query.sort) body.sort = { [query.sort]: query.dir === 'asc' ? 1 : -1 };

  const fields = query.fields
    .split(',')
    .map(item => item.trim())
    .filter(Boolean);

  if (fields.length > 0) body.fields = fields.includes('_id') ? fields : ['_id', ...fields];

  if (query.per) {
    body.per = { field: query.per, limit: query.perLimit };

    return body;
  }

  body.limit = query.limit;

  return body;
}

// columnsOf picks the table columns: _id first, then the top-level keys in
// the order they first show up, at most max of them.
export function columnsOf(rows: Record<string, unknown>[], max = 12): string[] {
  const seen = new Set<string>();

  for (const row of rows) {
    for (const key of Object.keys(row)) {
      if (key !== '_id' && key !== '_v') seen.add(key);
    }
  }

  return ['_id', ...[...seen].slice(0, max)];
}

// cellText is the short text of one value in a table cell.
export function cellText(value: unknown, max = 60): string {
  if (value === undefined) return '';

  const text = typeof value === 'string' ? value : JSON.stringify(value);

  return text.length > max ? `${text.slice(0, max - 1)}…` : text;
}
