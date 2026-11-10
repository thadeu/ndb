import type { Ndb } from './ndb.js'
import {
  RedisStreams,
  integer,
  notSupported,
  syntaxError,
  text,
  type ClaimSpec,
  type StreamEntries,
  type StreamId,
} from './redis-streams.js'
import type { Doc, Entry, TrimOptions } from './stream.js'

export { ReplyError } from './redis-streams.js'

// The stream commands of node-redis 5 (the redis package) on top of ndb
// streams: the same method names, arguments and answer objects.

// One argument of a command, as node-redis takes it.
export type RedisArgument = string | Buffer

// One ID or more.
export type RedisVariadicArgument = RedisArgument | RedisArgument[]

// The fields of a new entry. Redis keeps text: a number becomes its text.
export type Message = Record<string, RedisArgument | number>

// { id, message: { field: value } }. The values are text.
export interface StreamMessage {
  id: string
  message: Record<string, string>
}

export interface StreamMessages {
  name: string
  messages: StreamMessage[]
}

export interface XReadStream {
  key: RedisArgument
  id: RedisArgument
}

export type XReadStreams = XReadStream[] | XReadStream

export interface XAddOptions {
  TRIM?: {
    strategy?: 'MAXLEN' | 'MINID'
    strategyModifier?: '=' | '~'
    threshold: number
    limit?: number
  }
}

export interface XRangeOptions {
  COUNT?: number
}

export interface XReadOptions {
  COUNT?: number
  // 0 waits with no limit.
  BLOCK?: number
}

export interface XReadGroupOptions extends XReadOptions {
  NOACK?: boolean
}

export interface XTrimOptions {
  strategyModifier?: '=' | '~'
  LIMIT?: number
}

export interface XGroupCreateOptions {
  MKSTREAM?: boolean
  ENTRIESREAD?: number
}

export interface XGroupSetIdOptions {
  ENTRIESREAD?: number
}

export interface XPendingReply {
  pending: number
  firstId: string | null
  lastId: string | null
  consumers: { name: string; deliveriesCounter: number }[] | null
}

export interface XPendingRangeOptions {
  IDLE?: number
  consumer?: RedisArgument
}

export interface XPendingRangeItem {
  id: string
  consumer: string
  millisecondsSinceLastDelivery: number
  deliveriesCounter: number
}

// TIME is not supported. LASTID has no effect.
export interface XClaimOptions {
  IDLE?: number
  RETRYCOUNT?: number
  FORCE?: boolean
  LASTID?: RedisArgument
}

export interface XAutoClaimOptions {
  COUNT?: number
}

export interface XAutoClaimReply {
  nextId: string
  messages: StreamMessage[]
  deletedMessages: string[]
}

export interface XAutoClaimJustIdReply {
  nextId: string
  messages: string[]
  deletedMessages: string[]
}

// ndb has no radix tree: the two numbers are 0.
export interface XInfoStreamReply {
  length: number
  'radix-tree-keys': number
  'radix-tree-nodes': number
  'last-generated-id': string
  'max-deleted-entry-id': string
  'entries-added': number
  'recorded-first-entry-id': string
  groups: number
  'first-entry': StreamMessage | null
  'last-entry': StreamMessage | null
}

export interface XInfoGroup {
  name: string
  consumers: number
  pending: number
  'last-delivered-id': string
  'entries-read': number | null
  lag: number | null
}

export interface XInfoConsumer {
  name: string
  pending: number
  idle: number
  inactive: number
}

export interface NodeRedisStreams {
  xAdd(key: RedisArgument, id: RedisArgument, message: Message, options?: XAddOptions): Promise<string>
  // null when the stream does not exist.
  xAddNoMkStream(key: RedisArgument, id: RedisArgument, message: Message, options?: XAddOptions): Promise<string | null>
  xLen(key: RedisArgument): Promise<number>
  xRange(key: RedisArgument, start: RedisArgument, end: RedisArgument, options?: XRangeOptions): Promise<StreamMessage[]>
  // start is the higher ID.
  xRevRange(key: RedisArgument, start: RedisArgument, end: RedisArgument, options?: XRangeOptions): Promise<StreamMessage[]>
  xRead(streams: XReadStreams, options?: XReadOptions): Promise<StreamMessages[] | null>
  xDel(key: RedisArgument, id: RedisVariadicArgument): Promise<number>
  xTrim(key: RedisArgument, strategy: 'MAXLEN' | 'MINID', threshold: number | string, options?: XTrimOptions): Promise<number>
  xGroupCreate(key: RedisArgument, group: RedisArgument, id: RedisArgument, options?: XGroupCreateOptions): Promise<'OK'>
  xGroupSetId(key: RedisArgument, group: RedisArgument, id: RedisArgument, options?: XGroupSetIdOptions): Promise<'OK'>
  xGroupDestroy(key: RedisArgument, group: RedisArgument): Promise<number>
  xGroupCreateConsumer(key: RedisArgument, group: RedisArgument, consumer: RedisArgument): Promise<number>
  xGroupDelConsumer(key: RedisArgument, group: RedisArgument, consumer: RedisArgument): Promise<number>
  xReadGroup(group: RedisArgument, consumer: RedisArgument, streams: XReadStreams, options?: XReadGroupOptions): Promise<StreamMessages[] | null>
  xAck(key: RedisArgument, group: RedisArgument, id: RedisVariadicArgument): Promise<number>
  xPending(key: RedisArgument, group: RedisArgument): Promise<XPendingReply>
  xPendingRange(
    key: RedisArgument,
    group: RedisArgument,
    start: RedisArgument,
    end: RedisArgument,
    count: number,
    options?: XPendingRangeOptions,
  ): Promise<XPendingRangeItem[]>
  xClaim(
    key: RedisArgument,
    group: RedisArgument,
    consumer: RedisArgument,
    minIdleTime: number,
    id: RedisVariadicArgument,
    options?: XClaimOptions,
  ): Promise<StreamMessage[]>
  xClaimJustId(
    key: RedisArgument,
    group: RedisArgument,
    consumer: RedisArgument,
    minIdleTime: number,
    id: RedisVariadicArgument,
    options?: XClaimOptions,
  ): Promise<string[]>
  xAutoClaim(
    key: RedisArgument,
    group: RedisArgument,
    consumer: RedisArgument,
    minIdleTime: number,
    start: RedisArgument,
    options?: XAutoClaimOptions,
  ): Promise<XAutoClaimReply>
  xAutoClaimJustId(
    key: RedisArgument,
    group: RedisArgument,
    consumer: RedisArgument,
    minIdleTime: number,
    start: RedisArgument,
    options?: XAutoClaimOptions,
  ): Promise<XAutoClaimJustIdReply>
  xInfoStream(key: RedisArgument): Promise<XInfoStreamReply>
  xInfoGroups(key: RedisArgument): Promise<XInfoGroup[]>
  xInfoConsumers(key: RedisArgument, group: RedisArgument): Promise<XInfoConsumer[]>
}

function list(value: RedisVariadicArgument): string[] {
  return (Array.isArray(value) ? value : [value]).map(String)
}

function streamIds(streams: XReadStreams): StreamId[] {
  return (Array.isArray(streams) ? streams : [streams]).map((s) => ({ key: String(s.key), id: String(s.id) }))
}

function trimOf(strategy: 'MAXLEN' | 'MINID' | undefined, threshold: number | string, modifier?: '=' | '~', limit?: number): TrimOptions {
  // node-redis sends a TRIM without a strategy as it is, and Redis rejects it.
  if (strategy !== 'MAXLEN' && strategy !== 'MINID') {
    throw syntaxError('syntax error: TRIM needs the strategy MAXLEN or MINID')
  }

  const trim: TrimOptions = strategy === 'MINID' ? { minId: String(threshold) } : { maxLen: integer(threshold, 'MAXLEN') }

  trim.approx = modifier === '~'
  trim.limit = limit

  return trim
}

function docOf(message: Message): Doc {
  const doc: Doc = {}

  for (const [field, value] of Object.entries(message)) {
    doc[field] = String(value)
  }

  return doc
}

// node-redis gives the message as an object without a prototype.
function messageOf(e: Entry<Doc>): StreamMessage {
  const message: Record<string, string> = Object.create(null)

  for (const [field, value] of Object.entries(e.doc)) {
    message[field] = text(value)
  }

  return { id: e.id, message }
}

function streamsOf(streams: StreamEntries[] | null): StreamMessages[] | null {
  return streams && streams.map((s) => ({ name: s.key, messages: s.entries.map(messageOf) }))
}

function claimSpec(options: XClaimOptions | undefined, justId: boolean): ClaimSpec {
  if (options && 'TIME' in options && options.TIME !== undefined) {
    throw notSupported('XCLAIM TIME')
  }

  return { idle: options?.IDLE, retryCount: options?.RETRYCOUNT, force: options?.FORCE, justId }
}

function checkRead(options: object | undefined): void {
  if (options && 'CLAIM' in options && options.CLAIM !== undefined) {
    throw notSupported('CLAIM')
  }
}

// The options of Redis 8.2 and later that ndb does not have. KEEPREF is what
// ndb does.
function checkPolicy(...options: (object | undefined)[]): void {
  for (const option of options) {
    const given = (option ?? {}) as { policy?: string; IDMP?: unknown; IDMPAUTO?: unknown }

    if (given.policy !== undefined && given.policy !== 'KEEPREF') {
      throw notSupported(given.policy)
    }

    if (given.IDMP !== undefined || given.IDMPAUTO !== undefined) {
      throw notSupported('IDMP')
    }
  }
}

// nodeRedis gives the stream commands of node-redis on the streams of db:
//
//   const streams = env.STREAMS_BACKEND === 'ndb' ? nodeRedis(db) : await createClient().connect()
//
//   await streams.xAdd('orders', '*', { order_id: '42' })
export function nodeRedis(db: Ndb): NodeRedisStreams {
  const core = new RedisStreams(db)

  async function add(key: RedisArgument, id: RedisArgument, message: Message, options: XAddOptions | undefined, noCreate: boolean) {
    checkPolicy(options, options?.TRIM)

    const t = options?.TRIM
    const trim = t ? trimOf(t.strategy, t.threshold, t.strategyModifier, t.limit) : undefined

    return core.add(String(key), String(id), docOf(message), { noCreate, trim })
  }

  return {
    async xAdd(key, id, message, options) {
      return (await add(key, id, message, options, false)) as string
    },

    xAddNoMkStream(key, id, message, options) {
      return add(key, id, message, options, true)
    },

    xLen(key) {
      return core.len(String(key))
    },

    async xRange(key, start, end, options) {
      const entries = await core.range(String(key), String(start), String(end), options?.COUNT)

      return entries.map(messageOf)
    },

    async xRevRange(key, start, end, options) {
      const entries = await core.range(String(key), String(end), String(start), options?.COUNT, true)

      return entries.map(messageOf)
    },

    async xRead(streams, options) {
      checkRead(options)

      return streamsOf(await core.read(streamIds(streams), { count: options?.COUNT, block: options?.BLOCK }))
    },

    xDel(key, id) {
      return core.del(String(key), list(id))
    },

    async xTrim(key, strategy, threshold, options) {
      checkPolicy(options)

      return core.trim(String(key), trimOf(strategy, threshold, options?.strategyModifier, options?.LIMIT))
    },

    async xGroupCreate(key, group, id, options) {
      await core.groupCreate(String(key), String(group), String(id), {
        mkStream: options?.MKSTREAM,
        entriesRead: options?.ENTRIESREAD,
      })

      return 'OK'
    },

    async xGroupSetId(key, group, id, options) {
      await core.groupSetId(String(key), String(group), String(id), options?.ENTRIESREAD)

      return 'OK'
    },

    xGroupDestroy(key, group) {
      return core.groupDestroy(String(key), String(group))
    },

    xGroupCreateConsumer(key, group, consumer) {
      return core.groupCreateConsumer(String(key), String(group), String(consumer))
    },

    xGroupDelConsumer(key, group, consumer) {
      return core.groupDelConsumer(String(key), String(group), String(consumer))
    },

    async xReadGroup(group, consumer, streams, options) {
      checkRead(options)

      const spec = { count: options?.COUNT, block: options?.BLOCK, noAck: options?.NOACK }

      return streamsOf(await core.readGroup(String(group), String(consumer), streamIds(streams), spec))
    },

    xAck(key, group, id) {
      return core.ack(String(key), String(group), list(id))
    },

    async xPending(key, group) {
      const sum = await core.pending(String(key), String(group))

      if (sum.count === 0) {
        return { pending: 0, firstId: null, lastId: null, consumers: null }
      }

      return {
        pending: sum.count,
        firstId: sum.lowest,
        lastId: sum.highest,
        consumers: sum.consumers.map((c) => ({ name: c.name, deliveriesCounter: c.count })),
      }
    },

    async xPendingRange(key, group, start, end, count, options) {
      const consumer = options?.consumer === undefined ? undefined : String(options.consumer)
      const spec = { start: String(start), end: String(end), count, consumer, idle: options?.IDLE }
      const pending = await core.pendingRange(String(key), String(group), spec)

      return pending.map((p) => ({
        id: p.id,
        consumer: p.consumer,
        millisecondsSinceLastDelivery: p.idleMs,
        deliveriesCounter: p.deliveries,
      }))
    },

    async xClaim(key, group, consumer, minIdleTime, id, options) {
      const out = await core.claim(String(key), String(group), String(consumer), minIdleTime, list(id), claimSpec(options, false))

      return out.entries.map(messageOf)
    },

    async xClaimJustId(key, group, consumer, minIdleTime, id, options) {
      const out = await core.claim(String(key), String(group), String(consumer), minIdleTime, list(id), claimSpec(options, true))

      return out.ids
    },

    async xAutoClaim(key, group, consumer, minIdleTime, start, options) {
      const out = await core.autoClaim(String(key), String(group), String(consumer), minIdleTime, String(start), options?.COUNT)

      return { nextId: out.cursor, messages: out.entries.map(messageOf), deletedMessages: out.deleted }
    },

    async xAutoClaimJustId(key, group, consumer, minIdleTime, start, options) {
      const out = await core.autoClaim(String(key), String(group), String(consumer), minIdleTime, String(start), options?.COUNT, true)

      return { nextId: out.cursor, messages: out.ids, deletedMessages: out.deleted }
    },

    async xInfoStream(key) {
      const info = await core.infoStream(String(key))

      return {
        length: info.length,
        'radix-tree-keys': 0,
        'radix-tree-nodes': 0,
        'last-generated-id': info.last_id,
        'max-deleted-entry-id': info.max_deleted_id,
        'entries-added': info.entries_added,
        'recorded-first-entry-id': info.first_id,
        groups: info.groups,
        'first-entry': info.first && messageOf(info.first),
        'last-entry': info.last && messageOf(info.last),
      }
    },

    async xInfoGroups(key) {
      const groups = await core.infoGroups(String(key))

      return groups.map((g) => ({
        name: g.name,
        consumers: g.consumers,
        pending: g.pending,
        'last-delivered-id': g.last_delivered_id,
        'entries-read': g.entries_read,
        lag: g.lag,
      }))
    },

    async xInfoConsumers(key, group) {
      const consumers = await core.infoConsumers(String(key), String(group))

      return consumers.map((c) => ({ name: c.name, pending: c.pending, idle: c.idle_ms, inactive: c.inactive_ms }))
    },
  }
}
