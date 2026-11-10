import { NdbError, type ErrorCode } from './errors.js'
import type { Ndb } from './ndb.js'
import {
  entryOf,
  type ConsumerInfo,
  type Doc,
  type Entry,
  type GroupInfo,
  type PendingEntry,
  type PendingSummary,
  type Stream,
  type StreamInfo,
  type TrimOptions,
} from './stream.js'

// The code of the Redis commands that the two adapters share (ioredis.ts and
// node-redis.ts). Each adapter reads the arguments of its library and gives
// the answer shape of its library; the calls to the daemon are here.

// Redis gives every entry when a read has no COUNT. The daemon gives at most
// this many.
export const maxCount = 10_000

// The default COUNT of XAUTOCLAIM in Redis.
const autoClaimCount = 100

// A read that blocks with no limit (BLOCK 0) is a sequence of reads of this
// length.
const foreverChunkMs = 30_000

// How often a blocked read looks for a stream that does not exist.
const missingPollMs = 500

// An error with the text that Redis gives: "BUSYGROUP ...", "NOGROUP ...",
// "ERR ...". code and status are those of the ndb error (cause), so one catch
// works for the two backends by the message, and for ndb by the code.
export class ReplyError extends NdbError {
  constructor(message: string, code: ErrorCode, status = 0, cause?: unknown) {
    super(code, '', status)

    this.message = message
    this.cause = cause
  }
}

export function notSupported(what: string): ReplyError {
  return new ReplyError(`ERR ndb: ${what} is not supported`, 'not_supported')
}

export function syntaxError(text = 'syntax error'): ReplyError {
  return new ReplyError(`ERR ${text}`, 'invalid_request')
}

// Which stream, and which group, a command is about. xgroup: the command is
// an XGROUP subcommand, which has its own error for a stream that does not
// exist.
interface Scope {
  key: string
  group?: string
  xgroup?: boolean
}

const noKeyForGroup =
  'ERR The XGROUP subcommand requires the key to exist. Note that for CREATE you may want to use the MKSTREAM option to create an empty stream automatically.'

function replyText(code: string, scope: Scope): string | undefined {
  switch (code) {
    case 'group_exists':
      return 'BUSYGROUP Consumer Group name already exists'
    case 'unknown_group':
      return `NOGROUP No such key '${scope.key}' or consumer group '${scope.group ?? ''}'`
    case 'unknown_collection':
      if (scope.xgroup) return noKeyForGroup

      return scope.group === undefined ? 'ERR no such key' : replyText('unknown_group', scope)
    case 'invalid_stream_id':
      return 'ERR Invalid stream ID specified as stream command argument'
    case 'stream_id_too_small':
      return 'ERR The ID specified in XADD is equal or smaller than the target stream top item'
    case 'not_a_stream':
      return 'WRONGTYPE Operation against a key holding the wrong kind of value'
    default:
      return undefined
  }
}

// replyError turns an error of the daemon into a ReplyError with the text of
// Redis. An error that Redis does not have (unauthorized, not_ready, a
// network error) stays as it is.
export function replyError(err: unknown, scope: Scope): unknown {
  if (!(err instanceof NdbError) || err instanceof ReplyError) {
    return err
  }

  const text = replyText(err.code, scope)

  return text === undefined ? err : new ReplyError(text, err.code, err.status, err)
}

function codeOf(err: unknown): string {
  return err instanceof NdbError ? err.code : ''
}

// text is a field value as Redis gives it: a string. A string stays as it
// is, a number or a boolean becomes its text, and any other value (null, an
// array, an object) becomes JSON text.
export function text(value: unknown): string {
  if (typeof value === 'string') {
    return value
  }

  if (typeof value === 'number' || typeof value === 'boolean') {
    return String(value)
  }

  return JSON.stringify(value) ?? ''
}

// integer reads a number argument, which the libraries also take as text.
// As in Redis, the text is decimal digits with an optional "-": "0x10",
// "5000.0", "1e3" and " 5" fail.
export function integer(value: unknown, what: string): number {
  const n = typeof value === 'number' ? value : Number(String(value))

  if (!Number.isSafeInteger(n) || (typeof value !== 'number' && !/^-?\d+$/.test(String(value)))) {
    throw syntaxError(`${what} is not an integer or out of range`)
  }

  return n
}

// countOf is the COUNT of one read. Redis gives every entry without a COUNT
// or with a COUNT of 0 or less; the daemon gives at most maxCount.
function countOf(count: number | undefined): number {
  return count === undefined || count <= 0 ? maxCount : Math.min(count, maxCount)
}

// checkBlock fails on a negative BLOCK, as Redis does.
function checkBlock(block: number | undefined): void {
  if (block !== undefined && block < 0) {
    throw syntaxError('timeout is negative')
  }
}

function sleep(ms: number): Promise<void> {
  return new Promise((resolve) => setTimeout(resolve, ms))
}

// The highest seq of an ID.
const maxSeq = '18446744073709551615'

// One side of an XRANGE interval: "(id" leaves the ID out. An ID without a
// seq is <ms>-0 as a start and <ms>-<max> as an end, also after "(": so
// "(1000" as a start leaves out only 1000-0, and "(2000" as an end leaves
// out only 2000-<max>, as in Redis. The daemon reads an ID without a seq
// the other way with $gt and $lt, so the SDK sends the full ID.
function bound(id: string, side: 'start' | 'end'): Doc {
  const closed = side === 'start' ? '$gte' : '$lte'

  if (!id.startsWith('(')) {
    return { [closed]: id }
  }

  const bare = id.slice(1)
  const full = bare.includes('-') ? bare : `${bare}-${side === 'start' ? '0' : maxSeq}`
  const edge = side === 'start' ? `${maxSeq}-${maxSeq}` : '0-0'

  if (full === edge) {
    throw syntaxError(`invalid ${side} ID for the interval`)
  }

  return side === 'start' ? { $gt: full } : { $lt: full }
}

export interface StreamId {
  key: string
  id: string
}

export interface StreamEntries {
  key: string
  entries: Entry<Doc>[]
}

export interface ReadSpec {
  count?: number
  // BLOCK: undefined does not wait, 0 waits with no limit.
  block?: number
}

export interface ReadGroupSpec extends ReadSpec {
  noAck?: boolean
}

export interface AddSpec {
  noCreate?: boolean
  trim?: TrimOptions
}

export interface GroupCreateSpec {
  mkStream?: boolean
  entriesRead?: number
}

export interface PendingRangeSpec {
  start: string
  end: string
  count: number
  consumer?: string
  idle?: number
}

export interface ClaimSpec {
  idle?: number
  retryCount?: number
  force?: boolean
  justId?: boolean
}

// The answer of claim and autoClaim. With justId, ids has the IDs and
// entries is empty.
export interface ClaimAnswer {
  cursor: string
  entries: Entry<Doc>[]
  ids: string[]
  deleted: string[]
}

export interface StreamState extends StreamInfo {
  first: Entry<Doc> | null
  last: Entry<Doc> | null
}

// The answer when the stream, or the group, does not exist, for the commands
// where Redis gives a value and not an error.
interface Absent<R> {
  codes: readonly string[]
  value: R
}

const noStream = ['unknown_collection'] as const
const noStreamOrGroup = ['unknown_collection', 'unknown_group'] as const

export class RedisStreams {
  constructor(readonly db: Ndb) {}

  // XADD. null: noCreate and the stream does not exist.
  add(key: string, id: string, doc: Doc, spec: AddSpec = {}): Promise<string | null> {
    const absent = spec.noCreate ? { codes: noStream, value: null } : undefined

    return this.on<string | null>({ key }, (s) => s.append(doc, { id, noCreate: spec.noCreate, trim: spec.trim }), absent)
  }

  len(key: string): Promise<number> {
    return this.on({ key }, async (s) => (await s.info()).length, { codes: noStream, value: 0 })
  }

  // XRANGE, and XREVRANGE with rev. start is the lower ID in the two cases.
  range(key: string, start: string, end: string, count?: number, rev = false): Promise<Entry<Doc>[]> {
    if (count !== undefined && count <= 0) {
      return Promise.resolve([])
    }

    let query: Doc

    try {
      query = { _id: { ...bound(start, 'start'), ...bound(end, 'end') } }
    } catch (err) {
      return Promise.reject(err)
    }

    const body: Doc = {
      query,
      sort: { _id: rev ? -1 : 1 },
      limit: count,
    }

    return this.on({ key }, (s) => s.rows('_search', body), { codes: noStream, value: [] })
  }

  // XREAD. null: no stream has an entry.
  async read(streams: StreamId[], spec: ReadSpec = {}): Promise<StreamEntries[] | null> {
    checkBlock(spec.block)

    if (spec.block !== undefined && streams.length > 1) {
      throw notSupported('XREAD with BLOCK on more than one stream')
    }

    const all = await Promise.all(streams.map(async (s) => ({ key: s.key, entries: await this.readOne(s, spec) })))
    const full = all.filter((s) => s.entries.length > 0)

    return full.length > 0 ? full : null
  }

  // XREADGROUP. The ID ">" gives new entries; any other ID gives the pending
  // entries of the consumer after it, and its stream is in the answer also
  // when it has none, as in Redis. null: no stream is in the answer.
  //
  // With more than one stream, each group is checked first: a read of ">"
  // moves entries to the pending list, so a NOGROUP on one stream after the
  // read of the others would lose them. As in Redis, the call then fails
  // and gives nothing.
  async readGroup(group: string, consumer: string, streams: StreamId[], spec: ReadGroupSpec = {}): Promise<StreamEntries[] | null> {
    checkBlock(spec.block)

    if (spec.block !== undefined && streams.length > 1) {
      throw notSupported('XREADGROUP with BLOCK on more than one stream')
    }

    if (streams.length > 1 && streams.some((s) => s.id === '>')) {
      await Promise.all(streams.map((s) => this.on({ key: s.key, group }, (stream) => stream.group(group).pending())))
    }

    const all = await Promise.all(
      streams.map(async (s) => ({
        key: s.key,
        history: s.id !== '>',
        entries: await this.readGroupOne(group, consumer, s, spec),
      })),
    )

    const kept = all.filter((s) => s.history || s.entries.length > 0)

    return kept.length > 0 ? kept.map((s) => ({ key: s.key, entries: s.entries })) : null
  }

  // XDEL: one request for each ID.
  async del(key: string, ids: string[]): Promise<number> {
    const counts = await Promise.all(ids.map((id) => this.on({ key }, (s) => s.delete(id), { codes: noStream, value: 0 })))

    return counts.reduce((sum, n) => sum + n, 0)
  }

  trim(key: string, spec: TrimOptions): Promise<number> {
    return this.on({ key }, (s) => s.trim(spec), { codes: noStream, value: 0 })
  }

  async groupCreate(key: string, group: string, id: string, spec: GroupCreateSpec = {}): Promise<void> {
    const body = { start: id, no_create: spec.mkStream ? undefined : true, entries_read: spec.entriesRead }

    await this.on({ key, group, xgroup: true }, (s) => s.call('PUT', groupPath(group), body))
  }

  async groupSetId(key: string, group: string, id: string, entriesRead?: number): Promise<void> {
    const body = { id, entries_read: entriesRead }

    await this.on({ key, group, xgroup: true }, (s) => s.call('POST', groupPath(group, 'setid'), body))
  }

  // XGROUP DESTROY: 1, or 0 when the group does not exist.
  async groupDestroy(key: string, group: string): Promise<number> {
    const scope = { key, group, xgroup: true }

    try {
      await this.on(scope, (s) => s.group(group).destroy())
    } catch (err) {
      if (codeOf(err) === 'unknown_group') {
        return 0
      }

      throw err
    }

    return 1
  }

  // XGROUP CREATECONSUMER: 1, or 0 when the consumer exists.
  async groupCreateConsumer(key: string, group: string, consumer: string): Promise<number> {
    const created = await this.on({ key, group, xgroup: true }, (s) => s.group(group).createConsumer(consumer))

    return created ? 1 : 0
  }

  // XGROUP DELCONSUMER: the number of pending entries the consumer had.
  groupDelConsumer(key: string, group: string, consumer: string): Promise<number> {
    return this.on({ key, group, xgroup: true }, (s) => s.group(group).deleteConsumer(consumer))
  }

  ack(key: string, group: string, ids: string[]): Promise<number> {
    return this.on({ key, group }, (s) => s.group(group).ack(...ids), { codes: noStreamOrGroup, value: 0 })
  }

  pending(key: string, group: string): Promise<PendingSummary> {
    return this.on({ key, group }, (s) => s.group(group).pending())
  }

  pendingRange(key: string, group: string, spec: PendingRangeSpec): Promise<PendingEntry[]> {
    if (spec.count <= 0) {
      return Promise.resolve([])
    }

    const query = { start: spec.start, end: spec.end, count: spec.count, consumer: spec.consumer, idleMs: spec.idle }

    return this.on({ key, group }, (s) => s.group(group).pendingList(query))
  }

  // XCLAIM. An entry that is no longer in the stream is not in the answer,
  // and it leaves the pending list, as in Redis 7.
  claim(key: string, group: string, consumer: string, minIdle: number, ids: string[], spec: ClaimSpec = {}): Promise<ClaimAnswer> {
    const body = {
      consumer,
      min_idle_ms: minIdle,
      ids,
      idle_ms: spec.idle,
      retry_count: spec.retryCount,
      force: spec.force || undefined,
      just_ids: spec.justId || undefined,
    }

    return this.claimOf(key, group, body, spec.justId === true)
  }

  // XAUTOCLAIM.
  autoClaim(key: string, group: string, consumer: string, minIdle: number, start: string, count?: number, justId = false): Promise<ClaimAnswer> {
    const body = {
      consumer,
      min_idle_ms: minIdle,
      cursor: start,
      count: count ?? autoClaimCount,
      just_ids: justId || undefined,
    }

    return this.claimOf(key, group, body, justId)
  }

  // XINFO STREAM, with the first and the last entry.
  infoStream(key: string): Promise<StreamState> {
    return this.on({ key }, async (s) => {
      const [info, first, last] = await Promise.all([s.info(), s.range('-', '+', 1), s.revRange('+', '-', 1)])

      return { ...info, first: first[0] ?? null, last: last[0] ?? null }
    })
  }

  infoGroups(key: string): Promise<GroupInfo[]> {
    return this.on({ key }, async (s) => (await s.groups()) ?? [])
  }

  infoConsumers(key: string, group: string): Promise<ConsumerInfo[]> {
    return this.on({ key, group }, async (s) => (await s.group(group).consumers()) ?? [])
  }

  // on runs fn on the stream and gives its errors the text of Redis.
  private async on<R>(scope: Scope, fn: (s: Stream<Doc>) => Promise<R>, absent?: Absent<R>): Promise<R> {
    try {
      return await fn(this.db.stream<Doc>(scope.key))
    } catch (err) {
      if (absent?.codes.includes(codeOf(err))) {
        return absent.value
      }

      throw replyError(err, scope)
    }
  }

  private async readOne(s: StreamId, spec: ReadSpec): Promise<Entry<Doc>[]> {
    if (s.id === '+') {
      throw notSupported('XREAD with the ID "+"')
    }

    const count = countOf(spec.count)

    // A blocked read can be more than one request. "$" in each request loses
    // the entries appended between two of them: the last ID replaces it.
    const after = spec.block !== undefined && s.id === '$' ? await this.lastId(s.key) : s.id

    return this.blocked(spec.block, (blockMs) =>
      this.on<Entry<Doc>[] | undefined>({ key: s.key }, (stream) => stream.read({ after, count, blockMs }), {
        codes: noStream,
        value: undefined,
      }),
    )
  }

  private readGroupOne(group: string, consumer: string, s: StreamId, spec: ReadGroupSpec): Promise<Entry<Doc>[]> {
    const count = countOf(spec.count)
    const scope = { key: s.key, group }

    if (s.id !== '>') {
      return this.on(scope, (stream) => stream.group(group).read(consumer, { pending: true, after: s.id, count }))
    }

    return this.blocked(spec.block, (blockMs) =>
      this.on(scope, (stream) => stream.group(group).read(consumer, { count, blockMs, noAck: spec.noAck })),
    )
  }

  // blocked runs once until it gives entries or the block time ends. once
  // gives undefined when the stream does not exist: a later append makes it.
  private async blocked(block: number | undefined, once: (blockMs?: number) => Promise<Entry<Doc>[] | undefined>): Promise<Entry<Doc>[]> {
    if (block === undefined) {
      return (await once()) ?? []
    }

    const deadline = block > 0 ? Date.now() + block : Infinity

    for (;;) {
      const got = await once(Math.max(1, Math.min(deadline - Date.now(), foreverChunkMs)))

      if (got && got.length > 0) {
        return got
      }

      if (Date.now() >= deadline) {
        return []
      }

      if (!got) {
        await sleep(Math.min(missingPollMs, deadline - Date.now()))
      }
    }
  }

  private lastId(key: string): Promise<string> {
    return this.on({ key }, async (s) => (await s.info()).last_id, { codes: noStream, value: '0' })
  }

  private async claimOf(key: string, group: string, body: Doc, justId: boolean): Promise<ClaimAnswer> {
    type Answer = { cursor?: string; entries?: unknown[]; deleted?: string[] }

    const out = await this.on({ key, group }, (s) => s.call<Answer>('POST', groupPath(group, 'claim'), body))
    const rows = out.entries ?? []

    return {
      cursor: out.cursor ?? '0-0',
      entries: justId ? [] : rows.map((row) => entryOf<Doc>(row as Doc)),
      ids: justId ? rows.map(String) : [],
      deleted: out.deleted ?? [],
    }
  }
}

function groupPath(group: string, ...parts: string[]): string {
  return ['_streams/groups', ...[group, ...parts].map(encodeURIComponent)].join('/')
}

// fieldList is the document of an entry as Redis gives it: field, value,
// field, value, all text.
export function fieldList(doc: Doc): string[] {
  return Object.entries(doc).flatMap(([field, value]) => [field, text(value)])
}
