// Pure logic of the Aggregate page: the pipeline as stages, the request, the
// row count of each stage, and the chart data. The server and the browser
// script both use it, so it imports nothing from the server.
import { type KeyContext, operatorKeyAt } from '@/components/code-editor-model';
import { isSealed } from '@/components/json-model';

export interface Stage {
  op: string;
  body: unknown;
}

export interface StageInfo {
  op: string;
  what: string;
  sample: unknown;
}

// The stages of POST /{coll}/_aggregate (docs/dev/aggregate.md), in the
// order a pipeline uses them.
export const STAGES: StageInfo[] = [
  { op: '$match', what: 'keep the documents that pass a filter', sample: {} },
  { op: '$unwind', what: 'one row for each element of an array', sample: '$items' },
  { op: '$group', what: 'group the rows and compute values', sample: { _id: null, n: { $count: {} } } },
  { op: '$having', what: 'keep the groups that pass a filter', sample: { n: { $gt: 1 } } },
  { op: '$sort', what: 'order the rows', sample: { n: -1 } },
  { op: '$skip', what: 'skip the first rows', sample: 10 },
  { op: '$limit', what: 'keep the first rows', sample: 10 },
  { op: '$project', what: 'keep or remove fields', sample: { n: 1 } },
  { op: '$lookup', what: 'add documents of another collection', sample: { from: 'customers', localField: '_id', foreignField: '_id', as: 'customer' } },
];

export const DEFAULT_PIPELINE: Stage[] = [{ op: '$group', body: { _id: null, n: { $count: {} } } }];

// Over this number of stages the page does not count the rows of each stage:
// each count is one more request to ndb.
export const COUNT_STAGES_MAX = 6;

export function whatOf(op: string): string {
  return STAGES.find(stage => stage.op === op)?.what ?? 'not a stage that ndb knows';
}

export type Parsed = { stages: Stage[]; error?: undefined } | { stages?: undefined; error: string };

// parsePipeline reads the JSON of a pipeline: an array of stages, or a body
// with the key pipeline. Each stage is an object with one key.
export function parsePipeline(text: string): Parsed {
  let value: unknown;

  try {
    value = JSON.parse(text);
  } catch {
    return { error: 'The pipeline is not valid JSON.' };
  }

  if (value !== null && typeof value === 'object' && !Array.isArray(value) && 'pipeline' in value) value = (value as { pipeline: unknown }).pipeline;

  if (!Array.isArray(value)) return { error: 'The pipeline must be a JSON array of stages.' };

  const stages: Stage[] = [];

  for (const [index, item] of value.entries()) {
    const keys = item !== null && typeof item === 'object' && !Array.isArray(item) ? Object.keys(item) : [];

    if (keys.length !== 1) return { error: `Stage ${index + 1} must be an object with one key, such as {"$match": {}}.` };

    stages.push({ op: keys[0]!, body: (item as Record<string, unknown>)[keys[0]!] });
  }

  return { stages };
}

export function toPipeline(stages: Stage[]): Record<string, unknown>[] {
  return stages.map(stage => ({ [stage.op]: stage.body }));
}

// formatBody is the text of one stage body in its card.
export function formatBody(body: unknown): string {
  return JSON.stringify(body, null, 2) ?? 'null';
}

// formatPipeline writes one stage on each line, so the text stays short in
// the URL and easy to read in the plain form.
export function formatPipeline(stages: Stage[]): string {
  if (stages.length === 0) return '[]';

  return `[\n${toPipeline(stages)
    .map(stage => `  ${JSON.stringify(stage)}`)
    .join(',\n')}\n]`;
}

export function requestBody(stages: Stage[]): string {
  return `{ "pipeline": ${formatPipeline(stages)} }`;
}

// A target is one collection, a list (a,b) or a pattern (logs-*).
export function validTarget(target: string): boolean {
  return /^[^\s/?#%\\]+$/.test(target);
}

// aggregatePath is the path of the request. With decrypt, it asks for the
// plain values of the encrypted fields (?decrypt=1 in the URL).
export function aggregatePath(target: string, decrypt = false): string {
  const path = `/${target.split(',').map(encodeURIComponent).join(',')}/_aggregate`;

  return decrypt ? `${path}?decrypt=1` : path;
}

// hasSealed says if a value, at any depth, is a stored encrypted value.
export function hasSealed(value: unknown): boolean {
  if (isSealed(value)) return true;

  if (Array.isArray(value)) return value.some(hasSealed);

  if (value !== null && typeof value === 'object') return Object.values(value).some(hasSealed);

  return false;
}

// maskDeep shows each stored encrypted value, at any depth, as "[encrypted]":
// the page never shows the long ndb:e1: text.
export function maskDeep(value: unknown): unknown {
  if (isSealed(value)) return '[encrypted]';

  if (Array.isArray(value)) return value.map(maskDeep);

  if (value !== null && typeof value === 'object') return Object.fromEntries(Object.entries(value).map(([key, item]) => [key, maskDeep(item)]));

  return value;
}

export interface CountRequest {
  body: Record<string, unknown>;
  from: 'total' | 'n';
}

// countRequest builds the request that counts the rows that leave stage
// `index`. ndb needs one $group in a pipeline. So before the $group the
// prefix gets a $group that counts its rows (the answer is in `n`), and from
// the $group on the prefix runs with count (the answer is X-Ndb-Total).
export function countRequest(stages: Stage[], index: number): CountRequest {
  const prefix = stages.slice(0, index + 1);

  if (prefix.some(stage => stage.op === '$group')) return { body: { pipeline: toPipeline(prefix), count: true, limit: 1 }, from: 'total' };

  const counter: Stage = { op: '$group', body: { _id: null, n: { $count: {} } } };

  return { body: { pipeline: toPipeline([...prefix.filter(stage => stage.op !== '$sort'), counter]) }, from: 'n' };
}

export interface Bar {
  label: string;
  value: number;
  width: number;
}

export interface Chart {
  field: string;
  bars: Bar[];
}

function labelOf(id: unknown): string {
  if (id === null || id === undefined) return 'null';

  return typeof id === 'string' ? id : JSON.stringify(id);
}

// chartOf makes horizontal bars from a group result that has one number for
// each group: every row has the same single field besides _id, and it is a
// number. Any other result has no chart.
export function chartOf(rows: Record<string, unknown>[]): Chart | undefined {
  const first = rows[0];

  if (!first) return undefined;

  const fields = Object.keys(first).filter(key => key !== '_id' && key !== '_coll');

  if (fields.length !== 1) return undefined;

  const field = fields[0]!;
  const same = rows.every(row => typeof row[field] === 'number' && Object.keys(row).every(key => key === '_id' || key === '_coll' || key === field));

  if (!same) return undefined;

  const max = Math.max(...rows.map(row => Math.abs(row[field] as number)));

  const bars = rows.map(row => {
    const value = row[field] as number;

    return { label: labelOf(row._id), value, width: max > 0 ? Math.round((Math.abs(value) / max) * 100) : 0 };
  });

  return { field, bars };
}

// columnsOf is the union of the keys of the rows, in first-seen order.
export function columnsOf(rows: Record<string, unknown>[]): string[] {
  const columns: string[] = [];

  for (const row of rows) {
    for (const key of Object.keys(row)) {
      if (!columns.includes(key)) columns.push(key);
    }
  }

  return columns;
}

export function cellText(value: unknown): string {
  if (value === undefined) return '';

  return typeof value === 'string' ? value : JSON.stringify(value);
}

// moveStage moves one stage up (-1) or down (1). A move over an end changes
// nothing.
export function moveStage<T>(stages: T[], index: number, delta: 1 | -1): T[] {
  const to = index + delta;

  if (index < 0 || index >= stages.length || to < 0 || to >= stages.length) return stages;

  const next = [...stages];

  [next[index], next[to]] = [next[to]!, next[index]!];

  return next;
}

export interface Operator {
  name: string;
  what: string;
}

// The logical operators of a filter: keys at the top of a filter object
// (docs/dev/query.md, Filters).
export const LOGICAL_OPERATORS: Operator[] = [
  { name: '$and', what: 'every filter of the array matches' },
  { name: '$or', what: 'one filter of the array matches' },
  { name: '$nor', what: 'no filter of the array matches' },
  { name: '$not', what: 'the filter does not match' },
];

// The operators of one field of a filter.
export const FIELD_OPERATORS: Operator[] = [
  { name: '$eq', what: 'equal' },
  { name: '$ne', what: 'not equal, or no field' },
  { name: '$gt', what: 'greater than' },
  { name: '$gte', what: 'greater than or equal' },
  { name: '$lt', what: 'less than' },
  { name: '$lte', what: 'less than or equal' },
  { name: '$in', what: 'in an array of values' },
  { name: '$nin', what: 'not in an array of values' },
  { name: '$exists', what: 'the field is there: true or false' },
  { name: '$not', what: 'the operators do not match' },
  { name: '$regex', what: 'the string matches a pattern (RE2)' },
  { name: '$options', what: 'flags of $regex: i, m, s, x' },
  { name: '$elemMatch', what: 'one element of the array matches' },
];

// The accumulators of $group, and $dateTrunc for a key that is a time bucket
// (docs/dev/aggregate.md).
export const GROUP_OPERATORS: Operator[] = [
  { name: '$count', what: 'number of rows; the argument is {}' },
  { name: '$sum', what: 'sum of the numbers' },
  { name: '$avg', what: 'average of the numbers' },
  { name: '$min', what: 'lowest value' },
  { name: '$max', what: 'highest value' },
  { name: '$first', what: 'value of the first row of the group' },
  { name: '$last', what: 'value of the last row of the group' },
  { name: '$distinct', what: 'number of distinct values' },
  { name: '$addToSet', what: 'the distinct values, as an array' },
  { name: '$dateTrunc', what: 'time bucket: date, unit, timezone' },
];

// operatorsOf lists the operators that a key can be, in the body of a stage
// at a depth of open brackets. $match and $having take a filter: the logical
// operators at the top, every operator deeper (a field object, or a filter
// in $or). In $group the keys at the top are the names of the output fields,
// and the operators are in the objects under them. Other stages have none.
export function operatorsOf(op: string, depth: number): Operator[] {
  if (op === '$match' || op === '$having') return depth <= 1 ? LOGICAL_OPERATORS : [...FIELD_OPERATORS, ...LOGICAL_OPERATORS.filter(item => item.name !== '$not')];

  if (op === '$group') return depth <= 1 ? [] : GROUP_OPERATORS;

  return [];
}

export interface Suggestion {
  context: KeyContext;
  items: Operator[];
}

// suggestOperators gives the operators to offer at the caret, in the body of
// a stage, or undefined when the list must be closed.
export function suggestOperators(op: string, text: string, caret: number): Suggestion | undefined {
  const context = operatorKeyAt(text, caret);

  if (!context) return undefined;

  const items = operatorsOf(op, context.depth).filter(item => item.name.startsWith(context.prefix));

  if (items.length === 0 || (items.length === 1 && items[0]!.name === context.prefix)) return undefined;

  return { context, items };
}

// A target with a comma or a * names more than one collection.
export function isMultiTarget(target: string): boolean {
  return /[*,]/.test(target);
}

export function matchText(count: number): string {
  if (count === 0) return 'No collection matches this target.';

  return count === 1 ? '1 collection matches this target.' : `${count} collections match this target.`;
}
