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
  encrypted?: string[];
  blind?: Record<string, string> | string[];
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
  index: string;
  blind?: string;
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
// build state and no drop route. ndb names an index on a blind field cpf
// cpf_bidx: the row shows cpf, with the mode of the blind field, and keeps
// the name of ndb for the drop.
export function indexRows(indexes: IndexState[], unique: string[] = [], blind: Record<string, string> = {}): IndexRow[] {
  const keys = new Set(unique);
  const names = new Set(indexes.map(index => index.field));

  const builtIn: IndexRow = { fields: '_id', kind: 'built in', unique: true, state: 'ready', ready: true, serves: indexServes('_id'), droppable: false, keyOnly: false, index: '_id' };

  const secondary = indexes.map(index => {
    const shown = blindName(index.field, blind);
    const unique = keys.has(index.field);

    return {
      fields: shown.fields,
      kind: indexKind(shown.fields),
      unique,
      state: indexState(index),
      ready: index.state === 'ready',
      serves: shown.blind ? blindServes(shown.fields, unique) : indexServes(shown.fields),
      droppable: true,
      keyOnly: false,
      index: index.field,
      blind: shown.blind,
    };
  });

  const keysOnly = unique
    .filter(name => !names.has(name))
    .map(name => {
      const shown = blindName(name, blind);

      return {
        fields: shown.fields,
        kind: indexKind(shown.fields),
        unique: true,
        state: 'enforced',
        ready: true,
        serves: shown.blind ? blindServes(shown.fields, true) : indexServes(shown.fields, true),
        droppable: false,
        keyOnly: true,
        index: name,
        blind: shown.blind,
      };
    });

  return [builtIn, ...secondary, ...keysOnly];
}

export const BLIND_SUFFIX = '_bidx';

// BLIND_MODES are the normalizations that ndb takes for a blind field.
export const BLIND_MODES = ['exact', 'lower', 'numeric'] as const;

export const BLIND_MODE_TEXT: Record<string, string> = {
  exact: 'exact (the value as sent)',
  lower: 'lower (lower case, such as an email)',
  numeric: 'numeric (only the digits, such as a CPF)',
};

// EQUALITY_ONLY is what a filter on a blind field can do.
export const EQUALITY_ONLY = 'equality only ($eq, $in, $ne, $nin)';

// blindName turns the ndb name of an index (cpf_bidx, org,cpf_bidx) into
// the fields of the user, and gives the blind modes in it.
export function blindName(name: string, blind: Record<string, string>): { fields: string; blind?: string } {
  const modes: string[] = [];

  const fields = name.split(',').map(part => {
    const field = part.endsWith(BLIND_SUFFIX) ? part.slice(0, -BLIND_SUFFIX.length) : '';
    const mode = field ? blind[field] : undefined;

    if (!mode) return part;

    modes.push(mode);

    return field;
  });

  return { fields: fields.join(','), blind: modes.length > 0 ? modes.join(', ') : undefined };
}

function blindServes(fields: string, unique: boolean): string {
  return unique ? `one document for each ${fields}; ${EQUALITY_ONLY}` : EQUALITY_ONLY;
}

// blindOf reads the blind key of a definition: an object of field to mode,
// or a list of exact fields.
export function blindOf(definition?: Pick<CollectionDefinition, 'blind'>): Record<string, string> {
  const blind = definition?.blind;

  if (!blind) return {};

  if (Array.isArray(blind)) return Object.fromEntries(blind.map(field => [field, 'exact']));

  return { ...blind };
}

// encryptedOnly is the encrypted fields that are not blind: ndb finds them
// only with $exists.
export function encryptedOnly(definition?: Pick<CollectionDefinition, 'encrypted' | 'blind'>): string[] {
  const blind = blindOf(definition);

  return (definition?.encrypted ?? []).filter(field => !(field in blind));
}

export function hasEncryption(definition?: Pick<CollectionDefinition, 'encrypted' | 'blind'>): boolean {
  return (definition?.encrypted ?? []).length > 0 || Object.keys(blindOf(definition)).length > 0;
}

// indexFieldsError is the reason ndb refuses an index or a unique key on
// these fields: a field that is encrypted but not blind.
export function indexFieldsError(fields: string[], definition?: Pick<CollectionDefinition, 'encrypted' | 'blind'>): string | undefined {
  const sealed = encryptedOnly(definition);
  const field = fields.find(item => sealed.includes(item));

  if (!field) return undefined;

  return `${field} is encrypted, not blind. Only a blind field can be found by value, so only a blind field can have an index or a unique key.`;
}

export interface EncryptionDraft {
  encrypted: string[];
  blind: Record<string, string>;
}

type EncryptionTarget = Pick<CollectionDefinition, 'time' | 'expires_field' | 'write' | 'stream' | 'indexes' | 'unique'>;

// encryptionError is the reason ndb refuses these encrypted and blind
// fields on a collection, or undefined when it takes them.
export function encryptionError(draft: EncryptionDraft, target: EncryptionTarget): string | undefined {
  const blind = Object.keys(draft.blind);
  const all = [...new Set([...draft.encrypted, ...blind])];

  if (all.length === 0) return undefined;

  if (target.stream) return 'A stream has no encrypted fields.';

  if (target.write === 'direct') return 'Encrypted fields need engine mode. In direct mode the app writes the files, so ndb cannot encrypt them first.';

  for (const [field, mode] of Object.entries(draft.blind)) {
    if (!(BLIND_MODES as readonly string[]).includes(mode)) return `The mode of the blind field ${field} must be ${BLIND_MODES.join(', ')}.`;
  }

  const indexed = new Set([...(target.indexes ?? []), ...(target.unique ?? [])].flatMap(name => name.split(',')));

  for (const field of all) {
    if (/[.$,\s]/.test(field)) return `${field} is not a top-level field. Only a top-level field can be encrypted.`;

    if (field === '_id') return '_id cannot be encrypted: ndb reads it.';

    if (field === target.time) return `${field} is the time field. ndb reads it, so it cannot be encrypted.`;

    if (field === target.expires_field) return `${field} is the expires_field. ndb reads it, so it cannot be encrypted.`;

    if (indexed.has(field) && !blind.includes(field)) return `${field} has an index or a unique key. Make it a blind field, not only encrypted.`;
  }

  return undefined;
}

// encryptionDraft reads the encrypted and blind fields of a form: the text
// of encrypted, and the rows blind_field_N with blind_mode_N.
export function encryptionDraft(form: Record<string, unknown>): EncryptionDraft {
  const blind: Record<string, string> = {};

  for (const [key, value] of Object.entries(form)) {
    const row = /^blind_field_(\d+)$/.exec(key);
    const field = String(value ?? '').trim();

    if (!row || !field) continue;

    blind[field] = String(form[`blind_mode_${row[1]}`] ?? 'exact').trim() || 'exact';
  }

  const encrypted = parseFields(String(form.encrypted ?? '')).filter(field => !(field in blind));

  return { encrypted, blind };
}

// fieldNames lists the fields that the index picker offers: the top-level
// fields of a sample of documents and one level of their objects, and the
// fields of the definition.
export function fieldNames(rows: Record<string, unknown>[], definition?: CollectionDefinition): string[] {
  const names = new Set<string>();

  for (const row of rows) {
    for (const [key, value] of Object.entries(row)) {
      if (key === '_id' || key === '_v' || key.endsWith(BLIND_SUFFIX)) continue;

      names.add(key);

      if (value && typeof value === 'object' && !Array.isArray(value)) {
        for (const inner of Object.keys(value)) names.add(`${key}.${inner}`);
      }
    }
  }

  const blind = blindOf(definition);
  const indexed = [...(definition?.indexes ?? []), ...(definition?.unique ?? [])].flatMap(name => blindName(name, blind).fields.split(','));

  for (const field of [...(definition?.encrypted ?? []), ...Object.keys(blind), ...indexed]) names.add(field);

  if (definition?.time) names.add(definition.time);

  return [...names].filter(name => name !== '_id').sort();
}

// reencryptBody is the update that docs/dev/encryption.md gives to write
// every document again, so ndb encrypts the fields that were added later.
export function reencryptBody(day: string): Record<string, unknown> {
  return { query: { _id: { $exists: true } }, update: { $set: { encrypted_at: day } } };
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

  const draft = encryptionDraft(form);

  if (draft.encrypted.length > 0) body.encrypted = draft.encrypted;

  if (Object.keys(draft.blind).length > 0) body.blind = draft.blind;

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

export function reencrypt(ndb: Ndb, collection: string, day: string): Promise<{ modified?: number }> {
  return ndb.call('POST', path(collection, '/_update_by_query'), reencryptBody(day), 600_000);
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
