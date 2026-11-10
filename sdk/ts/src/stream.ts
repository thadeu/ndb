import { GroupExistsError } from './errors.js'
import type { Ndb } from './ndb.js'
import type { Method } from './transport.js'
import { workGroup, type Handler, type WorkOptions } from './stream-worker.js'

export type Doc = Record<string, unknown>

// Group starts, for Group.create and Group.setId.
// startNew: the group reads only the entries appended after it ($).
export const startNew = '$'

// startAll: the group reads every entry of the stream (0).
export const startAll = '0'

// One entry of a stream.
export interface Entry<T> {
  // "<ms>-<seq>".
  id: string
  // The ms of the ID.
  time: Date
  // The entry without _id and _ts.
  doc: T
  // The entry was pending, but it is no longer in the stream (a trim or a
  // delete). doc is an empty object.
  deleted: boolean
}

// Which entries a trim deletes: maxLen or minId.
export interface TrimOptions {
  // Keep the newest maxLen entries.
  maxLen?: number
  // Delete the entries with an ID lower than minId.
  minId?: string
  // Delete only whole blocks of 1 024 entries (~ in Redis).
  approx?: boolean
  // With approx, delete at most limit entries.
  limit?: number
}

export interface AppendOptions {
  // "<ms>-<seq>" or "<ms>-*". Default "*": the daemon makes it. On
  // appendMany, it applies to the first entry.
  id?: string
  // Trims the stream after the append.
  trim?: TrimOptions
  // Fail with unknown_collection when the stream does not exist (NOMKSTREAM).
  noCreate?: boolean
}

export interface StreamInfo {
  length: number
  first_id: string
  last_id: string
  entries_added: number
  max_deleted_id: string
  groups: number
}

// lag is null when it cannot be known.
export interface GroupInfo {
  name: string
  consumers: number
  pending: number
  last_delivered_id: string
  entries_read: number | null
  lag: number | null
}

export interface ConsumerInfo {
  name: string
  pending: number
  idle_ms: number
  inactive_ms: number
}

export interface ReadOptions {
  // The most entries to give: default 10, at most 10 000.
  count?: number
  // Wait up to this long (ms) for a new entry when there is none.
  blockMs?: number
  // Give the consumer's own pending entries, with an ID greater than after,
  // instead of new ones.
  pending?: boolean
  after?: string
  // Do not add the entries to the pending list: at most once.
  noAck?: boolean
  // Give only these fields (and _id).
  fields?: string[]
  // Ends the read, also a blocked one.
  signal?: AbortSignal
}

export interface PendingSummary {
  count: number
  lowest: string | null
  highest: string | null
  consumers: { name: string; count: number }[]
}

export interface PendingQuery {
  // The ID range: default "-" and "+".
  start?: string
  end?: string
  // The most entries to give: default 100.
  count?: number
  // Only the entries of this consumer.
  consumer?: string
  // Only the entries idle for at least this long (ms).
  idleMs?: number
}

export interface PendingEntry {
  id: string
  consumer: string
  idleMs: number
  deliveries: number
}

// The answer of claim and autoClaim.
export interface Claimed<T> {
  // Where the next autoClaim starts; "0-0" when the scan is complete. Empty
  // for claim.
  cursor: string
  entries: Entry<T>[]
  // Pending IDs whose entry is no longer in the stream. They left the
  // pending list.
  deleted: string[]
}

// idTime is the time of an entry ID: its ms part. A bad ID gives an invalid
// Date.
export function idTime(id: string): Date {
  const [ms] = id.split('-')

  return new Date(ms && /^\d+$/.test(ms) ? Number(ms) : NaN)
}

// entryOf turns one row of the daemon into an Entry.
export function entryOf<T>(row: Doc): Entry<T> {
  const id = String(row._id ?? '')
  const doc: Doc = { ...row }

  delete doc._id
  delete doc._ts
  delete doc._deleted

  return { id, time: idTime(id), doc: doc as T, deleted: row._deleted === true }
}

function trimBody(t: TrimOptions): Doc {
  return { max_len: t.maxLen, min_id: t.minId, approx: t.approx || undefined, limit: t.limit }
}

// Stream is one stream of entries of type T: an append-only log with
// consumer groups, with the behavior of Redis Streams. Nothing is sent until
// the first call; the first append, or the first group create, makes it.
export class Stream<T = Doc> {
  constructor(
    readonly db: Ndb,
    readonly name: string,
  ) {}

  // Makes the stream, or checks that the name is one.
  async create(): Promise<void> {
    await this.call('PUT', '', { stream: true })
  }

  // Adds one entry and returns its ID (XADD).
  async append(doc: T, opts: AppendOptions = {}): Promise<string> {
    const out = await this.call<{ _id: string }>('POST', '_streams/append', { doc, ...this.appendBody(opts) })

    return out._id
  }

  // Adds the entries in order, with one fsync for all, and returns their IDs.
  async appendMany(docs: T[], opts: AppendOptions = {}): Promise<string[]> {
    const out = await this.call<{ ids: string[] }>('POST', '_streams/append', { docs, ...this.appendBody(opts) })

    return out.ids
  }

  // Deletes old entries and returns how many it deleted (XTRIM).
  async trim(t: TrimOptions): Promise<number> {
    const out = await this.call<{ deleted: number }>('POST', '_streams/trim', trimBody(t))

    return out.deleted
  }

  // The state of the stream (XINFO STREAM).
  info(): Promise<StreamInfo> {
    return this.call('GET', '_streams')
  }

  // The groups of the stream (XINFO GROUPS).
  groups(): Promise<GroupInfo[]> {
    return this.call('GET', '_streams/groups')
  }

  // Up to count entries with an ID from start to end, both included, in ID
  // order (XRANGE). "-" and "+" are the lowest and the highest ID.
  range(start = '-', end = '+', count?: number): Promise<Entry<T>[]> {
    return this.rangeOf(start, end, count, 1)
  }

  // range from end down to start (XREVRANGE).
  revRange(end = '+', start = '-', count?: number): Promise<Entry<T>[]> {
    return this.rangeOf(start, end, count, -1)
  }

  // Removes one entry by ID (XDEL) and returns 1, or 0 when it is not there.
  async delete(id: string): Promise<number> {
    const out = await this.call<{ deleted: number }>('DELETE', `_doc/${encodeURIComponent(id)}`)

    return out.deleted
  }

  // The consumer group name of the stream. It sends nothing.
  group(name: string): Group<T> {
    return new Group<T>(this, name)
  }

  // call sends one request to /{stream}/{path} and parses the JSON answer.
  async call<R = unknown>(method: Method, path: string, body?: unknown): Promise<R> {
    const res = await this.db.transport.stream(method, this.path(path), body)
    const text = (await res.body.setEncoding('utf8').toArray()).join('')

    return parseAnswer(text) as R
  }

  // rows sends one request whose answer is NDJSON entries.
  async rows(path: string, body: unknown, signal?: AbortSignal): Promise<Entry<T>[]> {
    const res = await this.db.transport.stream('POST', this.path(path), body, signal, undefined, true)
    const out: Entry<T>[] = []

    for await (const row of res.rows()) {
      out.push(entryOf<T>(row as Doc))
    }

    return out
  }

  private path(action: string): string {
    const base = `/${encodeURIComponent(this.name)}`

    return action ? `${base}/${action}` : base
  }

  private appendBody(opts: AppendOptions): Doc {
    return {
      id: opts.id,
      no_create: opts.noCreate || undefined,
      trim: opts.trim ? trimBody(opts.trim) : undefined,
    }
  }

  private rangeOf(start: string, end: string, count: number | undefined, order: 1 | -1): Promise<Entry<T>[]> {
    const body: Doc = {
      query: { _id: { $gte: start, $lte: end } },
      sort: { _id: order },
    }

    if (count && count > 0) {
      body.limit = count
    }

    return this.rows('_search', body)
  }
}

// parseAnswer reads a JSON answer, or an NDJSON one as an array.
function parseAnswer(text: string): unknown {
  const trimmed = text.trim()

  if (trimmed === '') {
    return undefined
  }

  try {
    return JSON.parse(trimmed)
  } catch {
    return trimmed.split('\n').filter((l) => l.trim() !== '').map((l) => JSON.parse(l))
  }
}

// Group is one consumer group of a stream. Each entry goes to one consumer
// of the group and stays pending until it is acked.
export class Group<T = Doc> {
  constructor(
    readonly stream: Stream<T>,
    readonly name: string,
  ) {}

  // Makes the group (XGROUP CREATE ... MKSTREAM). start is startNew, startAll
  // or an ID. A group that exists: GroupExistsError.
  async create(start = startNew): Promise<void> {
    await this.stream.call('PUT', this.path(), { start })
  }

  // Makes the group, or does nothing when it exists.
  async ensure(start = startNew): Promise<void> {
    try {
      await this.create(start)
    } catch (err) {
      if (!(err instanceof GroupExistsError)) {
        throw err
      }
    }
  }

  // Moves the last delivered ID of the group (XGROUP SETID).
  async setId(id: string): Promise<void> {
    await this.stream.call('POST', this.path('setid'), { id })
  }

  // Deletes the group and its pending entries (XGROUP DESTROY).
  async destroy(): Promise<void> {
    await this.stream.call('DELETE', this.path())
  }

  // Makes a consumer and tells if it was new. A read makes it too.
  async createConsumer(consumer: string): Promise<boolean> {
    const out = await this.stream.call<{ created: boolean }>('PUT', this.path('consumers', consumer))

    return out.created
  }

  // Deletes a consumer and its pending entries, and returns how many it held
  // (XGROUP DELCONSUMER).
  async deleteConsumer(consumer: string): Promise<number> {
    const out = await this.stream.call<{ pending: number }>('DELETE', this.path('consumers', consumer))

    return out.pending
  }

  // The consumers of the group (XINFO CONSUMERS).
  consumers(): Promise<ConsumerInfo[]> {
    return this.stream.call('GET', this.path('consumers'))
  }

  // Gives entries to consumer (XREADGROUP): new ones by default, which become
  // pending for consumer, or its own pending ones with pending: true. An
  // empty answer is an empty array.
  read(consumer: string, opts: ReadOptions = {}): Promise<Entry<T>[]> {
    const body = {
      consumer,
      count: opts.count,
      block_ms: opts.blockMs ? Math.max(1, Math.round(opts.blockMs)) : undefined,
      pending: opts.pending || undefined,
      after: opts.after,
      no_ack: opts.noAck || undefined,
      fields: opts.fields,
    }

    return this.stream.rows(this.path('read'), body, opts.signal)
  }

  // Ends the work on the entries and returns how many were pending (XACK).
  // An ID that is not pending counts 0.
  async ack(...ids: string[]): Promise<number> {
    if (ids.length === 0) {
      return 0
    }

    const out = await this.stream.call<{ acked: number }>('POST', this.path('ack'), { ids })

    return out.acked
  }

  // The pending list in short (XPENDING).
  pending(): Promise<PendingSummary> {
    return this.stream.call('POST', this.path('pending'), {})
  }

  // Pending entries, in ID order (XPENDING with a range).
  async pendingList(q: PendingQuery = {}): Promise<PendingEntry[]> {
    const body = {
      start: q.start || '-',
      end: q.end || '+',
      count: q.count && q.count > 0 ? q.count : 100,
      consumer: q.consumer || undefined,
      idle_ms: q.idleMs ? Math.round(q.idleMs) : undefined,
    }

    type Row = { _id: string; consumer: string; idle_ms: number; deliveries: number }

    const rows = (await this.stream.call<Row[] | undefined>('POST', this.path('pending'), body)) ?? []

    return rows.map((r) => ({ id: r._id, consumer: r.consumer, idleMs: r.idle_ms, deliveries: r.deliveries }))
  }

  // Moves the entries idle for at least minIdleMs to consumer (XCLAIM). Each
  // one gets one more delivery.
  claim(consumer: string, minIdleMs: number, ids: string[]): Promise<Claimed<T>> {
    return this.claimOf({ consumer, min_idle_ms: Math.round(minIdleMs), ids })
  }

  // Scans the pending list from cursor and moves up to count entries idle
  // for at least minIdleMs to consumer (XAUTOCLAIM).
  autoClaim(consumer: string, minIdleMs: number, cursor = '0-0', count?: number): Promise<Claimed<T>> {
    return this.claimOf({ consumer, min_idle_ms: Math.round(minIdleMs), cursor: cursor || '0-0', count })
  }

  // Runs a worker of the group as consumer until the signal aborts; see
  // stream-worker.ts. The handler resolves to ack the entry; a throw leaves
  // it pending.
  work(consumer: string, handler: Handler<T>, opts: WorkOptions = {}): Promise<void> {
    return workGroup(this, consumer, handler, opts)
  }

  private async claimOf(body: Doc): Promise<Claimed<T>> {
    type Answer = { cursor?: string; entries?: Doc[]; deleted?: string[] }

    const out = await this.stream.call<Answer>('POST', this.path('claim'), body)

    return {
      cursor: out.cursor ?? '',
      entries: (out.entries ?? []).map((e) => entryOf<T>(e)),
      deleted: out.deleted ?? [],
    }
  }

  private path(...parts: string[]): string {
    return ['_streams/groups', this.name, ...parts].map((p, i) => (i === 0 ? p : encodeURIComponent(p))).join('/')
  }
}
