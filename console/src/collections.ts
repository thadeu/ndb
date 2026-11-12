// The collection routes of ndb that the explorer page uses, and the pure
// logic of its forms. The browser script of the page imports the pure part,
// so this file has no server-only import.
import type { CollectionStats, Definition, IndexState, Ndb } from '@/ndb';

// CollectionDefinition is the definition of GET /_collections, with the keys
// that the Definition type of the client does not list.
export interface CollectionDefinition extends Definition {
  stream?: boolean;
  pitr?: string;
  partition_by?: string;
  partition_of?: string;
  max_line_size?: number;
  blob_threshold?: number;
  ops_from_yaml?: boolean;
}

export type CollectionInfo = CollectionStats & { asleep?: boolean };

export const COLLECTION_NAME = /^[a-z0-9][a-z0-9_-]{0,63}$/;

export const MAX_INDEX_FIELDS = 4;

export type IndexKind = 'built in' | 'single' | 'compound' | 'path';

export interface IndexRow {
  fields: string;
  kind: IndexKind;
  unique: boolean;
  state: string;
  ready: boolean;
  serves: string;
  droppable: boolean;
  keyOnly: boolean;
}

export interface Request {
  method: string;
  path: string;
  body?: Record<string, unknown>;
}

export interface CompactReport {
  before: { segments: number; bytes: number };
  after: { segments: number; bytes: number };
  duration_ms: number;
}

export interface QuarantineRow {
  ts: string;
  file: string;
  offset: number;
  error: string;
  line: string;
}

export interface Explain {
  plan: string;
  examined?: number;
}

// isPattern tells if the text of the search box is a collection pattern
// (logs-2026-*), which the daemon resolves.
export function isPattern(text: string): boolean {
  return text.includes('*');
}

// parseFields reads the fields of a new index, in order. Commas, spaces and
// new lines split them. A field that comes twice counts once.
export function parseFields(text: string): string[] {
  const fields = text
    .split(/[\s,]+/)
    .map(field => field.trim())
    .filter(Boolean);

  return [...new Set(fields)];
}

// fieldsError is the reason a list of fields cannot be an index, or
// undefined when it can.
export function fieldsError(fields: string[]): string | undefined {
  if (fields.length === 0) return 'Add one field or more.';

  if (fields.length > MAX_INDEX_FIELDS) return `An index has at most ${MAX_INDEX_FIELDS} fields.`;

  if (fields.includes('_id')) return 'The _id index always exists.';

  return undefined;
}

// indexRequest is the call that makes an index, or a unique key. ndb has no
// index route for a unique key: it is a key of the definition.
export function indexRequest(collection: string, fields: string[], unique: boolean): Request {
  const name = fields.join(',');

  if (unique) return { method: 'PUT', path: `/${collection}`, body: { unique: [name] } };

  return { method: 'PUT', path: `/${collection}/_indexes/${name}` };
}

export function requestText(request: Request): string {
  const line = `${request.method} ${request.path}`;

  return request.body ? `${line}\n${JSON.stringify(request.body)}` : line;
}

export function indexKind(name: string): IndexKind {
  if (name === '_id') return 'built in';

  if (name.includes(',')) return 'compound';

  return name.includes('.') ? 'path' : 'single';
}

// indexServes says in a few words which filters the index answers.
export function indexServes(name: string, unique = false): string {
  const fields = name.split(',');
  const last = fields[fields.length - 1] ?? name;

  switch (indexKind(name)) {
    case 'built in':
      return 'get by _id, bloom filter per segment';
    case 'compound':
      return unique ? `one ${last} for each ${fields.slice(0, -1).join(', ')}` : `equality on ${fields.slice(0, -1).join(', ')}, range or sort on ${last}`;
    case 'path':
      return unique ? `one document for each ${name}` : `equality or range on the path ${name}, also inside arrays`;
    default:
      return unique ? `one document for each ${name}` : `equality, range or sort on ${name}`;
  }
}

// indexState is the state text of one index: ready, or building with the
// percent done when ndb gives the totals.
export function indexState(index: IndexState): string {
  if (index.state === 'ready') return 'ready';

  if (index.total > 0) return `${index.state} ${Math.floor((index.done / index.total) * 100)}%`;

  return index.state;
}

// NO_KEY_ROUTE says why a unique key has no button to remove it.
export const NO_KEY_ROUTE = 'ndb has no route that removes a unique key.';

// dropRefusal is the reason the console does not send the drop of an index,
// or undefined when it sends it. ndb answers 200 for a name that is not an
// index, so the console checks the definition first.
export function dropRefusal(definition: Pick<Definition, 'indexes' | 'unique'> | undefined, index: string): string | undefined {
  if (index === '') return 'Give the name of the index.';

  if (index === '_id') return 'The _id index always exists.';

  if (!definition || (definition.indexes ?? []).includes(index)) return undefined;

  if ((definition.unique ?? []).includes(index)) return `${index} is a unique key, not an index. ${NO_KEY_ROUTE}`;

  return `The collection has no index ${index}.`;
}

// dropNotice is the text after the drop of an index. A field that is also a
// unique key stays in the table as a key.
export function dropNotice(definition: Pick<Definition, 'unique'> | undefined, index: string): string {
  const stays = (definition?.unique ?? []).includes(index);

  return stays ? `Dropped the index ${index}. The unique key ${index} stays.` : `Dropped the index ${index}.`;
}

// indexRows is the table of the Indexes tab: the _id index, each secondary
// index, and each unique key. A unique key that is not also an index has no
// build state and no drop route.
export function indexRows(indexes: IndexState[], unique: string[] = []): IndexRow[] {
  const keys = new Set(unique);
  const names = new Set(indexes.map(index => index.field));

  const builtIn: IndexRow = { fields: '_id', kind: 'built in', unique: true, state: 'ready', ready: true, serves: indexServes('_id'), droppable: false, keyOnly: false };

  const secondary = indexes.map(index => ({
    fields: index.field,
    kind: indexKind(index.field),
    unique: keys.has(index.field),
    state: indexState(index),
    ready: index.state === 'ready',
    serves: indexServes(index.field),
    droppable: true,
    keyOnly: false,
  }));

  const keysOnly = unique
    .filter(name => !names.has(name))
    .map(name => ({ fields: name, kind: indexKind(name), unique: true, state: 'enforced', ready: true, serves: indexServes(name, true), droppable: false, keyOnly: true }));

  return [builtIn, ...secondary, ...keysOnly];
}

// planText turns the X-Ndb-Plan header (index:status, scan) into words.
export function planText(plan: string): string {
  return plan.startsWith('index:') ? `index ${plan.slice('index:'.length)}` : plan;
}

// lines reads a text area with one entry in each line. A compound index has
// commas inside its entry, so a comma does not split.
export function lines(text: string): string[] {
  return text
    .split(/\r?\n/)
    .map(line => line.replace(/\s+/g, ''))
    .filter(Boolean);
}

// definitionBody builds the body of PUT /{coll} from the fields of a form.
// It sends only the keys that have a value.
export function definitionBody(form: Record<string, unknown>): Record<string, unknown> {
  const body: Record<string, unknown> = {};

  for (const key of ['write', 'path', 'time', 'ttl', 'expires_field', 'durability', 'retention', 'wal_retention', 'pitr']) {
    const value = String(form[key] ?? '').trim();

    if (value) body[key] = value;
  }

  for (const key of ['indexes', 'unique']) {
    const values = lines(String(form[key] ?? ''));

    if (values.length > 0) body[key] = values;
  }

  return body;
}

// toggleFavorite adds the name to the list, or removes it when it is there.
export function toggleFavorite(list: string[], name: string): string[] {
  return list.includes(name) ? list.filter(item => item !== name) : [...list, name];
}

export function favoritesKey(connection: string): string {
  return `ndb-console:favorites:${connection}`;
}

function path(collection: string, rest = ''): string {
  return `/${encodeURIComponent(collection)}${rest}`;
}

export async function definitions(ndb: Ndb): Promise<CollectionDefinition[]> {
  const body = await ndb.call<{ collections: CollectionDefinition[] }>('GET', '/_collections');

  return body.collections;
}

// matchPattern is GET /{pattern}: the names that a collection pattern matches.
export async function matchPattern(ndb: Ndb, pattern: string): Promise<string[]> {
  const body = await ndb.call<{ collections: string[] }>('GET', `/${pattern.split('/').map(encodeURIComponent).join('/').replaceAll('%2A', '*')}`);

  return body.collections;
}

export function putDefinition(ndb: Ndb, collection: string, body: Record<string, unknown>): Promise<{ definition: CollectionDefinition }> {
  return ndb.call('PUT', path(collection), body, 30_000);
}

export function ensureIndex(ndb: Ndb, collection: string, fields: string[], unique: boolean): Promise<unknown> {
  const request = indexRequest(collection, fields, unique);

  if (request.body) return putDefinition(ndb, collection, request.body);

  return ndb.call('PUT', path(collection, `/_indexes/${fields.map(encodeURIComponent).join(',')}`));
}

export function dropIndex(ndb: Ndb, collection: string, name: string): Promise<unknown> {
  return ndb.call('DELETE', path(collection, `/_indexes/${name.split(',').map(encodeURIComponent).join(',')}`));
}

// compact waits for the merge of every segment, so it gets a long timeout.
export function compact(ndb: Ndb, collection: string): Promise<CompactReport> {
  return ndb.call('POST', path(collection, '/_compact'), undefined, 600_000);
}

export function truncate(ndb: Ndb, collection: string): Promise<unknown> {
  return ndb.call('POST', path(collection, `/_truncate?confirm=${encodeURIComponent(collection)}`), undefined, 60_000);
}

export function drop(ndb: Ndb, collection: string): Promise<unknown> {
  return ndb.call('DELETE', path(collection, `?confirm=${encodeURIComponent(collection)}`), undefined, 60_000);
}

export async function quarantine(ndb: Ndb, collection: string, limit = 20): Promise<{ total: number; rows: QuarantineRow[] }> {
  return ndb.call('GET', path(collection, `/_quarantine?limit=${limit}`));
}

// explain runs the filter with a limit of 1 and returns the plan headers of
// the search. It reads no more than one document.
export async function explain(ndb: Ndb, collection: string, filter: Record<string, unknown>): Promise<Explain> {
  const result = await ndb.search(collection, { query: filter, limit: 1 });

  return { plan: result.plan ?? 'unknown', examined: result.examined };
}
