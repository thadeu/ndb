import type { Ndb } from './ndb.js'
import {
  RedisStreams,
  fieldList,
  integer,
  notSupported,
  syntaxError,
  type ClaimSpec,
  type ReadGroupSpec,
  type StreamEntries,
  type StreamId,
} from './redis-streams.js'
import type { Doc, Entry, TrimOptions } from './stream.js'

export { ReplyError } from './redis-streams.js'

// The stream commands of ioredis 5 on top of ndb streams. The arguments are
// those of the Redis commands, in the same order, and the answers are the raw
// arrays that ioredis gives.

// One argument of a command, as ioredis takes it.
export type RedisArg = string | number | Buffer

// The optional last argument of every ioredis command.
export type Callback<T> = (err?: Error | null, result?: T) => void

export type Command<T> = (...args: (RedisArg | Callback<T> | undefined)[]) => Promise<T>

// [id, [field, value, field, value]].
export type EntryReply = [id: string, fields: string[]]

// The same, for the commands that can give an entry that was deleted from
// the stream: its fields are null.
export type PendingEntryReply = [id: string, fields: string[] | null]

// [[key, entries]], or null when no stream has an entry.
export type StreamsReply = [key: string, items: EntryReply[]][] | null

// The answer of xreadgroup at run time. ioredis types it as unknown[].
export type GroupStreamsReply = [key: string, items: PendingEntryReply[]][] | null

// The return types are those of ioredis, so the object can replace a Redis
// client in the code. Where ioredis says unknown, the comment gives the
// shape.
export interface IoredisStreams {
  // The ID, or null with NOMKSTREAM when the stream does not exist.
  xadd: Command<string | null>
  xlen: Command<number>
  xrange: Command<EntryReply[]>
  xrevrange: Command<EntryReply[]>
  xread: Command<StreamsReply>
  xdel: Command<number>
  xtrim: Command<number>
  // CREATE and SETID: "OK". DESTROY and CREATECONSUMER: 1 or 0. DELCONSUMER:
  // the number of pending entries the consumer had.
  xgroup: Command<unknown>
  // GroupStreamsReply.
  xreadgroup: Command<unknown[]>
  xack: Command<number>
  // Without a range: [count, lowest ID, highest ID, [[consumer, count]]].
  // With a range: [[id, consumer, idle ms, deliveries]].
  xpending: Command<unknown[]>
  // EntryReply[], or string[] with JUSTID.
  xclaim: Command<unknown[]>
  // [next start, EntryReply[] (string[] with JUSTID), deleted IDs].
  xautoclaim: Command<unknown[]>
  // A flat [name, value, name, value] array; GROUPS and CONSUMERS give one
  // for each group or consumer.
  xinfo: Command<unknown>
}

// Args reads the arguments of one command from left to right.
class Args {
  private at = 0

  constructor(
    private readonly command: string,
    private readonly list: string[],
  ) {}

  get done(): boolean {
    return this.at >= this.list.length
  }

  next(): string {
    const value = this.list[this.at]

    if (value === undefined) {
      throw syntaxError(`wrong number of arguments for '${this.command}' command`)
    }

    this.at += 1

    return value
  }

  integer(what: string): number {
    return integer(this.next(), what)
  }

  // is tells if the next argument is one of the words. Option words have no
  // case in Redis.
  is(...words: string[]): boolean {
    const value = this.list[this.at]

    return value !== undefined && words.includes(value.toUpperCase())
  }

  // take reads the next argument when it is the word.
  take(word: string): boolean {
    if (!this.is(word)) {
      return false
    }

    this.at += 1

    return true
  }

  rest(): string[] {
    const out = this.list.slice(this.at)

    this.at = this.list.length

    return out
  }

  // end fails when arguments remain.
  end(): void {
    if (!this.done) {
      throw syntaxError()
    }
  }
}

// MAXLEN|MINID [=|~] threshold [LIMIT count]
function trimOf(a: Args): TrimOptions {
  const strategy = a.next().toUpperCase()

  if (strategy !== 'MAXLEN' && strategy !== 'MINID') {
    throw syntaxError()
  }

  const approx = a.take('~')

  if (!approx) {
    a.take('=')
  }

  const threshold = a.next()
  const trim: TrimOptions = strategy === 'MAXLEN' ? { maxLen: integer(threshold, 'MAXLEN') } : { minId: threshold }

  trim.approx = approx

  if (a.take('LIMIT')) {
    trim.limit = a.integer('LIMIT')
  }

  if (a.is('DELREF', 'ACKED')) {
    throw notSupported(a.next().toUpperCase())
  }

  a.take('KEEPREF')

  return trim
}

// field value [field value ...]
function docOf(command: string, fields: string[]): Doc {
  if (fields.length === 0 || fields.length % 2 !== 0) {
    throw syntaxError(`wrong number of arguments for '${command}' command`)
  }

  const doc: Doc = {}

  for (let i = 0; i < fields.length; i += 2) {
    doc[fields[i]!] = fields[i + 1]
  }

  return doc
}

// STREAMS key [key ...] id [id ...]
function streamsOf(a: Args): StreamId[] {
  if (!a.take('STREAMS')) {
    throw syntaxError()
  }

  const rest = a.rest()
  const half = rest.length / 2

  if (rest.length === 0 || rest.length % 2 !== 0) {
    throw syntaxError("Unbalanced 'xread' list of streams: for each stream key an ID or '$' must be specified.")
  }

  return rest.slice(0, half).map((key, i) => ({ key, id: rest[half + i]! }))
}

// [COUNT count] [BLOCK ms] [NOACK], in any order, up to STREAMS.
function readSpecOf(a: Args, group: boolean): ReadGroupSpec {
  const spec: ReadGroupSpec = {}

  while (!a.done && !a.is('STREAMS')) {
    if (a.take('COUNT')) {
      spec.count = a.integer('COUNT')
    } else if (a.take('BLOCK')) {
      spec.block = a.integer('timeout')
    } else if (group && a.take('NOACK')) {
      spec.noAck = true
    } else if (a.is('CLAIM')) {
      throw notSupported('CLAIM')
    } else {
      throw syntaxError()
    }
  }

  return spec
}

// [COUNT count]
function countOf(a: Args): number | undefined {
  return a.take('COUNT') ? a.integer('COUNT') : undefined
}

function entryReply(e: Entry<Doc>): EntryReply {
  return [e.id, fieldList(e.doc)]
}

function pendingEntryReply(e: Entry<Doc>): PendingEntryReply {
  return [e.id, e.deleted ? null : fieldList(e.doc)]
}

function streamsReply(streams: StreamEntries[] | null): StreamsReply {
  return streams && streams.map((s) => [s.key, s.entries.map(entryReply)])
}

function groupStreamsReply(streams: StreamEntries[] | null): GroupStreamsReply {
  return streams && streams.map((s) => [s.key, s.entries.map(pendingEntryReply)])
}

const claimOptions = ['IDLE', 'TIME', 'RETRYCOUNT', 'FORCE', 'JUSTID', 'LASTID']

// [IDLE ms] [TIME ms] [RETRYCOUNT count] [FORCE] [JUSTID] [LASTID id]
function claimSpecOf(a: Args): ClaimSpec {
  const spec: ClaimSpec = {}

  while (!a.done) {
    if (a.take('IDLE')) {
      spec.idle = a.integer('IDLE')
    } else if (a.take('RETRYCOUNT')) {
      spec.retryCount = a.integer('RETRYCOUNT')
    } else if (a.take('FORCE')) {
      spec.force = true
    } else if (a.take('JUSTID')) {
      spec.justId = true
    } else if (a.take('LASTID')) {
      a.next()
    } else if (a.is('TIME')) {
      throw notSupported('XCLAIM TIME')
    } else {
      throw syntaxError()
    }
  }

  return spec
}

// ioredis gives the stream commands of ioredis on the streams of db:
//
//   const streams = env.STREAMS_BACKEND === 'ndb' ? ioredis(db) : new Redis()
//
//   await streams.xadd('orders', '*', 'order_id', '42')
export function ioredis(db: Ndb): IoredisStreams {
  const core = new RedisStreams(db)

  // command makes one method: it takes the callback off the arguments, turns
  // the others into text and gives the answer to the promise and to the
  // callback.
  function command<T>(name: string, run: (a: Args) => Promise<T>): Command<T> {
    return (...args) => {
      const given = args.filter((arg) => arg !== undefined)
      const last = given.at(-1)
      const callback = typeof last === 'function' ? last : undefined
      const words = (callback ? given.slice(0, -1) : given).map(String)
      const answer = Promise.resolve().then(() => run(new Args(name, words)))

      if (callback) {
        answer.then(
          (result) => callback(null, result),
          (err: Error) => callback(err),
        )
      }

      return answer
    }
  }

  async function ranged(a: Args, rev: boolean): Promise<EntryReply[]> {
    const key = a.next()
    const from = a.next()
    const to = a.next()
    const count = countOf(a)

    a.end()

    // XREVRANGE takes the higher ID first.
    const entries = rev ? await core.range(key, to, from, count, true) : await core.range(key, from, to, count)

    return entries.map(entryReply)
  }

  async function xgroup(a: Args): Promise<unknown> {
    const sub = a.next().toUpperCase()

    if (sub === 'HELP') {
      throw notSupported('XGROUP HELP')
    }

    const key = a.next()
    const group = a.next()

    switch (sub) {
      case 'CREATE': {
        const id = a.next()
        const mkStream = a.take('MKSTREAM')
        const entriesRead = a.take('ENTRIESREAD') ? a.integer('ENTRIESREAD') : undefined

        a.end()

        await core.groupCreate(key, group, id, { mkStream, entriesRead })

        return 'OK'
      }

      case 'SETID': {
        const id = a.next()
        const entriesRead = a.take('ENTRIESREAD') ? a.integer('ENTRIESREAD') : undefined

        a.end()

        await core.groupSetId(key, group, id, entriesRead)

        return 'OK'
      }

      case 'DESTROY':
        a.end()

        return core.groupDestroy(key, group)
      case 'CREATECONSUMER':
        return core.groupCreateConsumer(key, group, a.next())
      case 'DELCONSUMER':
        return core.groupDelConsumer(key, group, a.next())
      default:
        throw syntaxError(`unknown subcommand '${sub}'. Try XGROUP HELP.`)
    }
  }

  async function xpending(a: Args): Promise<unknown[]> {
    const key = a.next()
    const group = a.next()

    if (a.done) {
      const sum = await core.pending(key, group)

      if (sum.count === 0) {
        return [0, null, null, null]
      }

      return [sum.count, sum.lowest, sum.highest, sum.consumers.map((c) => [c.name, String(c.count)])]
    }

    const idle = a.take('IDLE') ? a.integer('IDLE') : undefined
    const start = a.next()
    const end = a.next()
    const count = a.integer('COUNT')
    const consumer = a.done ? undefined : a.next()

    a.end()

    const list = await core.pendingRange(key, group, { start, end, count, consumer, idle })

    return list.map((p) => [p.id, p.consumer, p.idleMs, p.deliveries])
  }

  async function xclaim(a: Args): Promise<unknown[]> {
    const key = a.next()
    const group = a.next()
    const consumer = a.next()
    const minIdle = a.integer('min-idle-time')
    const ids: string[] = []

    while (!a.done && !a.is(...claimOptions)) {
      ids.push(a.next())
    }

    if (ids.length === 0) {
      throw syntaxError("wrong number of arguments for 'xclaim' command")
    }

    const spec = claimSpecOf(a)
    const out = await core.claim(key, group, consumer, minIdle, ids, spec)

    return spec.justId ? out.ids : out.entries.map(entryReply)
  }

  async function xautoclaim(a: Args): Promise<unknown[]> {
    const key = a.next()
    const group = a.next()
    const consumer = a.next()
    const minIdle = a.integer('min-idle-time')
    const start = a.next()
    const count = countOf(a)
    const justId = a.take('JUSTID')

    a.end()

    const out = await core.autoClaim(key, group, consumer, minIdle, start, count, justId)

    return [out.cursor, justId ? out.ids : out.entries.map(entryReply), out.deleted]
  }

  async function xinfo(a: Args): Promise<unknown> {
    const sub = a.next().toUpperCase()

    switch (sub) {
      case 'STREAM': {
        const key = a.next()

        if (a.is('FULL')) {
          throw notSupported('XINFO STREAM FULL')
        }

        a.end()

        const info = await core.infoStream(key)

        // ndb has no radix tree: the two numbers are 0.
        return [
          'length', info.length,
          'radix-tree-keys', 0,
          'radix-tree-nodes', 0,
          'last-generated-id', info.last_id,
          'max-deleted-entry-id', info.max_deleted_id,
          'entries-added', info.entries_added,
          'recorded-first-entry-id', info.first_id,
          'groups', info.groups,
          'first-entry', info.first && entryReply(info.first),
          'last-entry', info.last && entryReply(info.last),
        ]
      }

      case 'GROUPS': {
        const groups = await core.infoGroups(a.next())

        return groups.map((g) => [
          'name', g.name,
          'consumers', g.consumers,
          'pending', g.pending,
          'last-delivered-id', g.last_delivered_id,
          'entries-read', g.entries_read,
          'lag', g.lag,
        ])
      }

      case 'CONSUMERS': {
        const consumers = await core.infoConsumers(a.next(), a.next())

        return consumers.map((c) => ['name', c.name, 'pending', c.pending, 'idle', c.idle_ms, 'inactive', c.inactive_ms])
      }

      default:
        throw notSupported(`XINFO ${sub}`)
    }
  }

  return {
    xadd: command('xadd', (a) => {
      const key = a.next()
      const noCreate = a.take('NOMKSTREAM')
      const trim = a.is('MAXLEN', 'MINID') ? trimOf(a) : undefined
      const id = a.next()

      return core.add(key, id, docOf('xadd', a.rest()), { noCreate, trim })
    }),

    xlen: command('xlen', (a) => core.len(a.next())),
    xrange: command('xrange', (a) => ranged(a, false)),
    xrevrange: command('xrevrange', (a) => ranged(a, true)),

    xread: command('xread', async (a) => {
      const spec = readSpecOf(a, false)

      return streamsReply(await core.read(streamsOf(a), spec))
    }),

    xdel: command('xdel', (a) => core.del(a.next(), a.rest())),
    xtrim: command('xtrim', (a) => core.trim(a.next(), trimOf(a))),
    xgroup: command('xgroup', xgroup),

    xreadgroup: command('xreadgroup', async (a) => {
      if (!a.take('GROUP')) {
        throw syntaxError()
      }

      const group = a.next()
      const consumer = a.next()
      const spec = readSpecOf(a, true)
      const reply = groupStreamsReply(await core.readGroup(group, consumer, streamsOf(a), spec))

      // null is the answer of Redis for no entry; the ioredis type has no
      // null.
      return reply as unknown[]
    }),

    xack: command('xack', (a) => core.ack(a.next(), a.next(), a.rest())),
    xpending: command('xpending', xpending),
    xclaim: command('xclaim', xclaim),
    xautoclaim: command('xautoclaim', xautoclaim),
    xinfo: command('xinfo', xinfo),
  }
}
