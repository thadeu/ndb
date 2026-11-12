// The stream routes of ndb (docs/dev/http/streams.md) for the Streams page,
// and the pure logic of that page: the dead letter name, the progress of a
// group, and the state of a pending entry.
import type { Definition, Doc, Ndb } from '@/ndb';
import { NdbError } from '@/ndb';

export interface StreamInfo {
  length: number;
  first_id?: string;
  last_id?: string;
  entries_added: number;
  max_deleted_id?: string;
  groups: number;
}

export interface Group {
  name: string;
  consumers: number;
  pending: number;
  last_delivered_id: string;
  entries_read: number | null;
  lag: number | null;
}

export interface Consumer {
  name: string;
  pending: number;
  idle_ms: number;
  inactive_ms: number;
}

export interface PendingEntry {
  _id: string;
  consumer: string;
  idle_ms: number;
  deliveries: number;
  retry_at: number | null;
}

// An entry that waits this long at a consumer can be claimed by another one.
export const CLAIM_IDLE_MS = 60_000;

// The most rows of a tab. The page shows the newest entries.
export const PAGE = 50;

const path = (stream: string, rest = '') => `/${encodeURIComponent(stream)}/_streams${rest}`;

const groupPath = (stream: string, group: string, rest = '') => path(stream, `/groups/${encodeURIComponent(group)}${rest}`);

// deadName is the dead letter stream that the SDK workers use by default.
export function deadName(stream: string): string {
  return `${stream}_dead`;
}

// streamNames gives the collections made with "stream": true.
export async function streamNames(ndb: Ndb): Promise<string[]> {
  const definitions = (await ndb.collections()) as (Definition & { stream?: boolean })[];

  return definitions
    .filter(definition => definition.stream === true)
    .map(definition => definition.name)
    .sort();
}

export function streamInfo(ndb: Ndb, stream: string): Promise<StreamInfo> {
  return ndb.call('GET', path(stream));
}

// deadInfo reads the dead letter of a stream. A stream with no dead letter
// yet has none: the answer is undefined.
export async function deadInfo(ndb: Ndb, stream: string): Promise<StreamInfo | undefined> {
  try {
    return await streamInfo(ndb, deadName(stream));
  } catch (error) {
    if (error instanceof NdbError && (error.code === 'unknown_collection' || error.code === 'not_a_stream')) return undefined;

    throw error;
  }
}

export function groups(ndb: Ndb, stream: string): Promise<Group[]> {
  return ndb.call('GET', path(stream, '/groups'));
}

export function consumers(ndb: Ndb, stream: string, group: string): Promise<Consumer[]> {
  return ndb.call('GET', groupPath(stream, group, '/consumers'));
}

export function pending(ndb: Ndb, stream: string, group: string, range: { start?: string; end?: string; count?: number } = {}): Promise<PendingEntry[]> {
  return ndb.call('POST', groupPath(stream, group, '/pending'), { start: range.start ?? '-', end: range.end ?? '+', count: range.count ?? PAGE });
}

// entries reads the newest entries, or the ones before an ID.
export async function entries(ndb: Ndb, stream: string, before?: string): Promise<Doc[]> {
  const query = before ? { _id: { $lt: before } } : {};
  const result = await ndb.search(stream, { query, sort: { _id: -1 }, limit: PAGE });

  return result.rows;
}

export function append(ndb: Ndb, stream: string, doc: Doc): Promise<{ _id: string }> {
  return ndb.call('POST', path(stream, '/append'), { doc });
}

export function trim(ndb: Ndb, stream: string, body: Record<string, unknown>): Promise<{ deleted: number }> {
  return ndb.call('POST', path(stream, '/trim'), body, 60_000);
}

export function createGroup(ndb: Ndb, stream: string, group: string, start: string): Promise<{ group: string; last_delivered_id: string }> {
  return ndb.call('PUT', groupPath(stream, group), { start, no_create: true });
}

export function ack(ndb: Ndb, stream: string, group: string, ids: string[]): Promise<{ acked: number }> {
  return ndb.call('POST', groupPath(stream, group, '/ack'), { ids });
}

async function pendingOne(ndb: Ndb, stream: string, group: string, id: string): Promise<PendingEntry> {
  const found = await pending(ndb, stream, group, { start: id, end: id, count: 1 });

  if (!found[0]) throw new NdbError('not_pending', `The entry ${id} is not pending in the group ${group}.`);

  return found[0];
}

// nack gives a pending entry back to the group after a wait. ndb counts
// only an entry of the named consumer, so the owner comes from the PEL.
export async function nack(ndb: Ndb, stream: string, group: string, id: string, delayMs: number): Promise<{ nacked: number }> {
  const entry = await pendingOne(ndb, stream, group, id);

  if (entry.consumer === '') throw new NdbError('not_pending', `The entry ${id} already waits for a retry.`);

  return ndb.call('POST', groupPath(stream, group, '/nack'), { consumer: entry.consumer, ids: [id], delay_ms: delayMs });
}

// claimIdle moves the idle entries of a group to one consumer, with a scan.
export async function claimIdle(ndb: Ndb, stream: string, group: string, consumer: string, minIdleMs: number): Promise<number> {
  let cursor = '0-0';
  let claimed = 0;

  for (let round = 0; round < 10; round++) {
    const answer = await ndb.call<{ cursor: string; entries: Doc[] }>('POST', groupPath(stream, group, '/claim'), { consumer, min_idle_ms: minIdleMs, cursor, count: 100 });

    claimed += answer.entries.length;
    cursor = answer.cursor;

    if (cursor === '0-0') break;
  }

  return claimed;
}

// payloadOf removes the fields that ndb adds to an entry (_id, _ts).
export function payloadOf(entry: Doc): Doc {
  return Object.fromEntries(Object.entries(entry).filter(([key]) => !key.startsWith('_')));
}

// bury does what an SDK worker does with a poison entry: it appends
// {stream, group, id, deliveries, reason, doc} to the dead letter, then acks
// the entry in its group. The reason is "manual".
export async function bury(ndb: Ndb, stream: string, group: string, id: string): Promise<string> {
  const entry = await pendingOne(ndb, stream, group, id);
  const doc = await ndb.document(stream, id);

  await append(ndb, deadName(stream), { stream, group, id, deliveries: entry.deliveries, reason: 'manual', doc: payloadOf(doc) });
  await ack(ndb, stream, group, [id]);

  return deadName(stream);
}

// replay appends the payload of a dead entry to the stream as a new entry,
// then deletes the dead entry.
export async function replay(ndb: Ndb, stream: string, deadId: string): Promise<string> {
  const dead = await ndb.document(deadName(stream), deadId);
  const doc = dead.doc;

  if (doc === null || typeof doc !== 'object' || Array.isArray(doc)) throw new NdbError('invalid_request', `The dead entry ${deadId} has no doc to append.`);

  const made = await append(ndb, stream, doc as Doc);

  await ndb.remove(deadName(stream), deadId);

  return made._id;
}

export interface Totals {
  consumers: number;
  pending: number;
  lag: number | null;
}

// totalsOf sums the groups. The lag is unknown when ndb cannot know the lag
// of one group.
export function totalsOf(list: Group[]): Totals {
  const known = list.every(group => group.lag !== null);

  return {
    consumers: list.reduce((sum, group) => sum + group.consumers, 0),
    pending: list.reduce((sum, group) => sum + group.pending, 0),
    lag: known ? list.reduce((sum, group) => sum + (group.lag ?? 0), 0) : null,
  };
}

// progressOf is the part of the stream that a group read, from 0 to 100, or
// undefined when the lag is unknown.
export function progressOf(group: Group, info: StreamInfo): number | undefined {
  if (group.lag === null) return undefined;

  const total = Math.max(info.entries_added, group.lag);

  if (total === 0) return 100;

  return Math.round(((total - group.lag) / total) * 100);
}

export function formatMs(ms: number): string {
  const seconds = Math.max(0, Math.round(ms / 1000));

  if (seconds < 60) return `${seconds} s`;

  if (seconds < 3600) return `${Math.round(seconds / 60)} min`;

  if (seconds < 86_400) return `${Math.round(seconds / 3600)} h`;

  return `${Math.round(seconds / 86_400)} d`;
}

export interface PendingState {
  label: string;
  tone: 'neutral' | 'warn' | 'bad';
}

// stateOf says what a pending entry does now: it waits for a retry (nacked),
// it can be taken (retry due, or idle at its consumer), or it is in work.
export function stateOf(entry: PendingEntry, now: number): PendingState {
  if (entry.retry_at !== null) {
    if (entry.retry_at > now) return { label: `retry in ${formatMs(entry.retry_at - now)}`, tone: 'warn' };

    return { label: 'retry due, can be claimed', tone: 'bad' };
  }

  if (entry.idle_ms >= CLAIM_IDLE_MS) return { label: 'idle, can be claimed', tone: 'bad' };

  return { label: 'in work', tone: 'neutral' };
}
