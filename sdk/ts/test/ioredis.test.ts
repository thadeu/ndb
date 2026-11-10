import { Redis } from 'ioredis'
import { afterAll, beforeAll, describe, expect, it } from 'vitest'
import { NdbError, UnknownGroupError } from '../src/index.js'
import { ReplyError, ioredis, type IoredisStreams } from '../src/ioredis.js'
import { unixDb } from './helpers.js'
import { later, pairs, redisUrl, streamKey } from './redis-backend.js'

// The commands under test, with the types of ioredis: the adapter and a
// real client run the same cases.
type Commands = Pick<Redis, keyof IoredisStreams>

interface Backend {
  streams: Commands
  // A second connection: a blocked read holds the first one.
  writer: Commands
  close: (keys: string[]) => Promise<unknown>
}

const idShape = /^\d+-\d+$/

// The commands with no argument types: the ioredis types do not take every
// order of options that Redis takes.
type Loose = Record<keyof IoredisStreams, (...args: unknown[]) => Promise<unknown>>

function loose(c: Commands): Loose {
  return c as unknown as Loose
}

function suite(name: string, open: () => Backend): void {
  describe(`ioredis commands on ${name}`, () => {
    const keys: string[] = []

    let streams!: Commands
    let writer!: Commands
    let close!: Backend['close']

    function newKey(): string {
      const key = streamKey()

      keys.push(key)

      return key
    }

    // Three entries with the IDs 1-1, 1-2 and 2-0.
    async function orders(): Promise<string> {
      const key = newKey()

      await streams.xadd(key, '1-1', 'order', '1', 'total', '9.9')
      await streams.xadd(key, '1-2', 'order', '2', 'total', '20')
      await streams.xadd(key, '2-0', 'order', '3', 'total', '30')

      return key
    }

    beforeAll(() => {
      ({ streams, writer, close } = open())
    })

    afterAll(() => close(keys))

    it('xadd, xlen, xrange and xrevrange', async () => {
      const key = newKey()

      expect(await streams.xlen(key)).toBe(0)
      expect(await streams.xrange(key, '-', '+')).toEqual([])

      expect(await streams.xadd(key, '*', 'order', '1', 'total', '9.9')).toMatch(idShape)
      expect(await streams.xadd(key, '9999999999999-1', 'order', 2, 'total', 20)).toBe('9999999999999-1')
      expect(await streams.xadd(key, '9999999999999-*', 'order', '3', 'total', '30')).toBe('9999999999999-2')

      await expect(streams.xadd(key, '1-1', 'order', '4')).rejects.toThrow(/^ERR The ID specified in XADD is equal or smaller/)

      expect(await streams.xlen(key)).toBe(3)

      const all = await streams.xrange(key, '-', '+')

      expect(all).toHaveLength(3)
      expect(all[0]![0]).toMatch(idShape)
      expect(all[0]![1]).toEqual(['order', '1', 'total', '9.9'])
      expect(all[1]).toEqual(['9999999999999-1', ['order', '2', 'total', '20']])

      expect(await streams.xrange(key, '-', '+', 'COUNT', 1)).toEqual([all[0]])
      expect(await streams.xrange(key, '9999999999999', '9999999999999')).toEqual([all[1], all[2]])
      expect(await streams.xrange(key, '(9999999999999-1', '+')).toEqual([all[2]])
      expect(await streams.xrange(key, '-', '(9999999999999-1')).toEqual([all[0]])

      expect(await streams.xrevrange(key, '+', '-')).toEqual([all[2], all[1], all[0]])
      expect(await streams.xrevrange(key, '+', '-', 'COUNT', 2)).toEqual([all[2], all[1]])
      expect(await streams.xrevrange(key, '9999999999999-1', '-')).toEqual([all[1], all[0]])
    })

    it('takes option words in any case, and a callback', async () => {
      const key = await orders()

      // The types of ioredis have the words in capitals; Redis takes any case.
      const lower = 'count' as 'COUNT'

      expect(await streams.xrange(key, '-', '+', lower, 1)).toEqual([['1-1', ['order', '1', 'total', '9.9']]])

      const length = await new Promise((resolve, reject) => {
        void streams.xlen(key, (err, result) => (err ? reject(err) : resolve(result)))
      })

      expect(length).toBe(3)

      const failure = await new Promise((resolve) => {
        streams.xadd(key, '1-1', 'order', '4', (err) => resolve(err)).catch(() => undefined)
      })

      expect(String(failure)).toMatch(/equal or smaller/)
    })

    it('xadd with MAXLEN, MINID and NOMKSTREAM', async () => {
      const key = newKey()

      expect(await streams.xadd(key, 'NOMKSTREAM', '*', 'order', '1')).toBeNull()
      expect(await streams.xlen(key)).toBe(0)

      for (let i = 1; i <= 5; i++) {
        await streams.xadd(key, 'MAXLEN', 2, `${i}-0`, 'order', String(i))
      }

      expect((await streams.xrange(key, '-', '+')).map((e) => e[0])).toEqual(['4-0', '5-0'])

      expect(await streams.xadd(key, 'MAXLEN', '~', 1000, '6-0', 'order', '6')).toBe('6-0')
      expect(await streams.xlen(key)).toBe(3)

      expect(await streams.xadd(key, 'NOMKSTREAM', 'MINID', '=', '6-0', '7-0', 'order', '7')).toBe('7-0')
      expect((await streams.xrange(key, '-', '+')).map((e) => e[0])).toEqual(['6-0', '7-0'])
    })

    it('xdel and xtrim', async () => {
      const key = await orders()

      expect(await streams.xdel(key, '1-1', '7-7')).toBe(1)
      expect(await streams.xdel(newKey(), '1-1')).toBe(0)

      expect(await streams.xtrim(key, 'MAXLEN', '~', 1)).toBe(0)
      expect(await streams.xtrim(key, 'MINID', '2-0')).toBe(1)
      expect(await streams.xtrim(key, 'MAXLEN', '=', 0)).toBe(1)
      expect(await streams.xtrim(newKey(), 'MAXLEN', 1)).toBe(0)

      expect(await streams.xlen(key)).toBe(0)
    })

    it('xread', async () => {
      const key = await orders()
      const other = newKey()
      const none = newKey()

      await streams.xadd(other, '5-0', 'order', '9')

      expect(await streams.xread('COUNT', 2, 'STREAMS', key, '0')).toEqual([
        [key, [['1-1', ['order', '1', 'total', '9.9']], ['1-2', ['order', '2', 'total', '20']]]],
      ])

      expect(await streams.xread('STREAMS', key, '1-2')).toEqual([[key, [['2-0', ['order', '3', 'total', '30']]]]])
      expect(await streams.xread('STREAMS', key, '2-0')).toBeNull()
      expect(await streams.xread('STREAMS', key, '$')).toBeNull()
      expect(await streams.xread('STREAMS', none, '0')).toBeNull()

      expect(await streams.xread('COUNT', 1, 'STREAMS', key, none, other, '1-2', '0', '0')).toEqual([
        [key, [['2-0', ['order', '3', 'total', '30']]]],
        [other, [['5-0', ['order', '9']]]],
      ])
    })

    it('xread with BLOCK waits for an append', async () => {
      const key = await orders()

      expect(await streams.xread('BLOCK', 50, 'STREAMS', key, '$')).toBeNull()

      later(200, () => writer.xadd(key, '3-0', 'order', '4'))

      const start = Date.now()

      expect(await streams.xread('COUNT', 5, 'BLOCK', 5000, 'STREAMS', key, '$')).toEqual([[key, [['3-0', ['order', '4']]]]])
      expect(Date.now() - start).toBeLessThan(4000)

      later(200, () => writer.xadd(key, '4-0', 'order', '5'))

      expect(await streams.xread('BLOCK', 0, 'STREAMS', key, '3-0')).toEqual([[key, [['4-0', ['order', '5']]]]])
    })

    it('xread with BLOCK waits for a stream that does not exist', async () => {
      const key = newKey()

      later(200, () => writer.xadd(key, '1-1', 'order', '1'))

      expect(await streams.xread('BLOCK', 5000, 'STREAMS', key, '$')).toEqual([[key, [['1-1', ['order', '1']]]]])
    })

    it('xgroup', async () => {
      const key = newKey()

      await expect(streams.xgroup('CREATE', key, 'g', '$')).rejects.toThrow(/^ERR The XGROUP subcommand requires the key to exist/)

      expect(await streams.xgroup('CREATE', key, 'g', '$', 'MKSTREAM')).toBe('OK')
      expect(await streams.xlen(key)).toBe(0)

      await expect(streams.xgroup('CREATE', key, 'g', '$', 'MKSTREAM')).rejects.toThrow(/^BUSYGROUP/)
      await expect(streams.xgroup('CREATE', key, 'g', '0')).rejects.toThrow(/^BUSYGROUP/)

      expect(await streams.xgroup('CREATECONSUMER', key, 'g', 'w1')).toBe(1)
      expect(await streams.xgroup('CREATECONSUMER', key, 'g', 'w1')).toBe(0)
      expect(await streams.xgroup('DELCONSUMER', key, 'g', 'w1')).toBe(0)

      expect(await streams.xgroup('SETID', key, 'g', '0')).toBe('OK')
      expect(await streams.xgroup('SETID', key, 'g', '$')).toBe('OK')

      await expect(streams.xgroup('SETID', key, 'nope', '0')).rejects.toThrow(/^NOGROUP/)
      await expect(streams.xgroup('CREATECONSUMER', key, 'nope', 'w1')).rejects.toThrow(/^NOGROUP/)

      expect(await streams.xgroup('DESTROY', key, 'g')).toBe(1)
      expect(await streams.xgroup('DESTROY', key, 'g')).toBe(0)
    })

    it('xreadgroup, xack, xpending, xclaim and xautoclaim', async () => {
      const key = await orders()
      const second = ['1-2', ['order', '2', 'total', '20']]

      expect(await streams.xgroup('CREATE', key, 'g', '0')).toBe('OK')

      expect(await streams.xpending(key, 'g')).toEqual([0, null, null, null])
      expect(await streams.xpending(key, 'g', '-', '+', 10)).toEqual([])

      expect(await streams.xreadgroup('GROUP', 'g', 'w1', 'COUNT', 2, 'STREAMS', key, '>')).toEqual([
        [key, [['1-1', ['order', '1', 'total', '9.9']], second]],
      ])

      expect(await streams.xack(key, 'g', '1-1')).toBe(1)
      expect(await streams.xack(key, 'g', '1-1', '7-7')).toBe(0)
      expect(await streams.xack(key, 'nope', '1-2')).toBe(0)

      expect(await streams.xpending(key, 'g')).toEqual([1, '1-2', '1-2', [['w1', '1']]])
      expect(await streams.xpending(key, 'g', '-', '+', 10)).toEqual([['1-2', 'w1', expect.any(Number), 1]])

      expect(await streams.xreadgroup('GROUP', 'g', 'w1', 'STREAMS', key, '0')).toEqual([[key, [second]]])
      expect(await streams.xreadgroup('GROUP', 'g', 'w1', 'COUNT', 5, 'STREAMS', key, '1-2')).toEqual([[key, []]])
      expect(await streams.xreadgroup('GROUP', 'g', 'idle', 'STREAMS', key, '0')).toEqual([[key, []]])

      expect(await streams.xpending(key, 'g', '-', '+', 10, 'w1')).toEqual([['1-2', 'w1', expect.any(Number), 2]])
      expect(await streams.xpending(key, 'g', '-', '+', 10, 'idle')).toEqual([])
      expect(await streams.xpending(key, 'g', 'IDLE', 600_000, '-', '+', 10)).toEqual([])
      expect(await streams.xpending(key, 'g', 'IDLE', 0, '1-2', '1-2', 10, 'w1')).toHaveLength(1)

      expect(await streams.xclaim(key, 'g', 'w2', 600_000, '1-2')).toEqual([])
      expect(await streams.xclaim(key, 'g', 'w2', 0, '1-2', '7-7')).toEqual([second])
      expect(await streams.xclaim(key, 'g', 'w1', 0, '1-2', 'JUSTID')).toEqual(['1-2'])
      expect(await streams.xclaim(key, 'g', 'w2', 0, '1-2', 'IDLE', 5000, 'RETRYCOUNT', 7, 'FORCE')).toEqual([second])

      const moved = (await streams.xpending(key, 'g', '-', '+', 10)) as [string, string, number, number][]

      expect(moved).toEqual([['1-2', 'w2', expect.any(Number), 7]])
      expect(moved[0]![2]).toBeGreaterThanOrEqual(5000)

      expect(await streams.xautoclaim(key, 'g', 'w3', 600_000_000, '0')).toEqual(['0-0', [], []])
      expect(await streams.xautoclaim(key, 'g', 'w3', 0, '0', 'COUNT', 10)).toEqual(['0-0', [second], []])
      expect(await streams.xautoclaim(key, 'g', 'w3', 0, '0-0', 'COUNT', 10, 'JUSTID')).toEqual(['0-0', ['1-2'], []])

      expect(await streams.xreadgroup('GROUP', 'g', 'w4', 'NOACK', 'STREAMS', key, '>')).toEqual([
        [key, [['2-0', ['order', '3', 'total', '30']]]],
      ])

      expect(await streams.xreadgroup('GROUP', 'g', 'w4', 'STREAMS', key, '>')).toBeNull()
      expect((await streams.xpending(key, 'g'))[0]).toBe(1)

      expect(await streams.xgroup('DELCONSUMER', key, 'g', 'w3')).toBe(1)
      expect(await streams.xpending(key, 'g')).toEqual([0, null, null, null])
    })

    it('gives NOGROUP for a group or a stream that does not exist', async () => {
      const key = await orders()
      const none = newKey()

      await expect(streams.xreadgroup('GROUP', 'nope', 'w1', 'STREAMS', key, '>')).rejects.toThrow(/^NOGROUP/)
      await expect(streams.xreadgroup('GROUP', 'nope', 'w1', 'STREAMS', none, '>')).rejects.toThrow(/^NOGROUP/)
      await expect(streams.xpending(key, 'nope')).rejects.toThrow(/^NOGROUP/)
      await expect(streams.xclaim(key, 'nope', 'w1', 0, '1-1')).rejects.toThrow(/^NOGROUP/)
      await expect(streams.xautoclaim(key, 'nope', 'w1', 0, '0')).rejects.toThrow(/^NOGROUP/)
      await expect(streams.xinfo('CONSUMERS', key, 'nope')).rejects.toThrow(/^NOGROUP/)
    })

    it('xreadgroup with BLOCK waits for an append', async () => {
      const key = await orders()

      await streams.xgroup('CREATE', key, 'g', '$')

      expect(await streams.xreadgroup('GROUP', 'g', 'w1', 'BLOCK', 50, 'STREAMS', key, '>')).toBeNull()

      later(200, () => writer.xadd(key, '3-0', 'order', '4'))

      const start = Date.now()
      const got = await streams.xreadgroup('GROUP', 'g', 'w1', 'COUNT', 5, 'BLOCK', 5000, 'STREAMS', key, '>')

      expect(got).toEqual([[key, [['3-0', ['order', '4']]]]])
      expect(Date.now() - start).toBeLessThan(4000)
    })

    it('gives a pending entry that was deleted without its fields', async () => {
      const key = await orders()

      await streams.xgroup('CREATE', key, 'g', '0')
      await streams.xreadgroup('GROUP', 'g', 'w1', 'COUNT', 2, 'STREAMS', key, '>')

      expect(await streams.xdel(key, '1-1')).toBe(1)

      expect(await streams.xreadgroup('GROUP', 'g', 'w1', 'STREAMS', key, '0')).toEqual([
        [key, [['1-1', null], ['1-2', ['order', '2', 'total', '20']]]],
      ])

      expect(await streams.xautoclaim(key, 'g', 'w2', 0, '0')).toEqual(['0-0', [['1-2', ['order', '2', 'total', '20']]], ['1-1']])
      expect(await streams.xpending(key, 'g')).toEqual([1, '1-2', '1-2', [['w2', '1']]])
    })

    it('reads "(ms" as Redis does at the two ends of a range', async () => {
      const key = newKey()

      for (const id of ['1000-0', '1000-5', '2000-3', '3000-0']) {
        await streams.xadd(key, id, 'a', '1')
      }

      const ids = (rows: [string, string[]][]) => rows.map((row) => row[0])

      expect(ids(await streams.xrange(key, '(1000', '+'))).toEqual(['1000-5', '2000-3', '3000-0'])
      expect(ids(await streams.xrange(key, '(2000', '+'))).toEqual(['2000-3', '3000-0'])
      expect(ids(await streams.xrange(key, '-', '(2000'))).toEqual(['1000-0', '1000-5', '2000-3'])
      expect(ids(await streams.xrange(key, '-', '(3000'))).toEqual(['1000-0', '1000-5', '2000-3', '3000-0'])
      expect(ids(await streams.xrevrange(key, '(2000', '-'))).toEqual(['2000-3', '1000-5', '1000-0'])
      expect(ids(await streams.xrevrange(key, '+', '(2000'))).toEqual(['3000-0', '2000-3'])

      await expect(streams.xrange(key, '-', '(0-0')).rejects.toThrow(/^ERR invalid end ID for the interval/)
    })

    it('reads every entry with no COUNT, COUNT 0 or a COUNT over the limit', async () => {
      const key = newKey()

      await streams.xgroup('CREATE', key, 'g', '0', 'MKSTREAM')

      for (let i = 1; i <= 15; i++) {
        await streams.xadd(key, `${i}-1`, 'i', String(i))
      }

      const size = (reply: unknown) => (reply as [string, unknown[]][])[0]![1].length

      expect(size(await streams.xread('STREAMS', key, '0'))).toBe(15)
      expect(size(await streams.xread('COUNT', 0, 'STREAMS', key, '0'))).toBe(15)
      expect(size(await streams.xread('COUNT', -1, 'STREAMS', key, '0'))).toBe(15)
      expect(size(await streams.xread('COUNT', 20_000, 'STREAMS', key, '0'))).toBe(15)
      expect(size(await streams.xreadgroup('GROUP', 'g', 'w1', 'COUNT', 0, 'STREAMS', key, '>'))).toBe(15)
    })

    it('rejects a negative BLOCK', async () => {
      const key = await orders()

      await streams.xgroup('CREATE', key, 'g', '0')

      await expect(streams.xread('BLOCK', -1, 'STREAMS', key, '$')).rejects.toThrow(/^ERR timeout is negative/)
      await expect(streams.xreadgroup('GROUP', 'g', 'w1', 'BLOCK', -1, 'STREAMS', key, '>')).rejects.toThrow(/^ERR timeout is negative/)
    })

    it('rejects bad trim and number arguments', async () => {
      const key = await orders()

      await expect(loose(streams).xtrim(key, 'MAXLEN', 1, 'garbage')).rejects.toThrow(/^ERR syntax error/)
      await expect(loose(streams).xtrim(key, 'MAXLEN', 0, 'LIMIT', 10)).rejects.toThrow(/^ERR syntax error, LIMIT cannot be used without the special ~ option/)
      await expect(streams.xtrim(key, 'MAXLEN', '0x10')).rejects.toThrow(/not an integer or out of range/)
      await expect(streams.xtrim(key, 'MAXLEN', '5000.0')).rejects.toThrow(/not an integer or out of range/)
      await expect(streams.xread('COUNT', '0x10', 'STREAMS', key, '0')).rejects.toThrow(/not an integer or out of range/)

      expect(await streams.xlen(key)).toBe(3)
    })

    it('takes the options in any order where Redis does', async () => {
      const key = await orders()

      expect(await streams.xadd(key, 'MAXLEN', 10, 'NOMKSTREAM', '3-0', 'f', 'v')).toBe('3-0')
      expect(await streams.xadd(newKey(), 'MAXLEN', 10, 'NOMKSTREAM', '*', 'f', 'v')).toBeNull()

      expect(await loose(streams).xgroup('CREATE', key, 'g', '$', 'ENTRIESREAD', 0, 'MKSTREAM')).toBe('OK')
      expect(await loose(streams).xgroup('CREATE', newKey(), 'g', '$', 'ENTRIESREAD', 0, 'MKSTREAM')).toBe('OK')

      await streams.xgroup('SETID', key, 'g', '0')
      await streams.xreadgroup('GROUP', 'g', 'w1', 'STREAMS', key, '>')

      expect(await loose(streams).xautoclaim(key, 'g', 'w2', 0, '0', 'JUSTID', 'COUNT', 2)).toEqual(['2-0', ['1-1', '1-2'], []])
    })

    it('delivers nothing when one group of a multi-stream xreadgroup does not exist', async () => {
      const one = newKey()
      const two = newKey()

      await streams.xgroup('CREATE', one, 'g', '0', 'MKSTREAM')
      await streams.xadd(one, '1-1', 'x', '1')
      await streams.xadd(two, '1-1', 'x', '1')

      await expect(streams.xreadgroup('GROUP', 'g', 'w1', 'STREAMS', one, two, '>', '>')).rejects.toThrow(/^NOGROUP/)

      expect(await streams.xpending(one, 'g')).toEqual([0, null, null, null])
    })

    it('xinfo', async () => {
      const key = await orders()

      await streams.xgroup('CREATE', key, 'g', '0')
      await streams.xreadgroup('GROUP', 'g', 'w1', 'COUNT', 2, 'STREAMS', key, '>')

      const info = pairs(await streams.xinfo('STREAM', key))

      expect(Object.keys(info)).toEqual([
        'length',
        'radix-tree-keys',
        'radix-tree-nodes',
        'last-generated-id',
        'max-deleted-entry-id',
        'entries-added',
        'recorded-first-entry-id',
        'groups',
        'first-entry',
        'last-entry',
      ])

      expect(info).toMatchObject({
        length: 3,
        'last-generated-id': '2-0',
        'max-deleted-entry-id': '0-0',
        'entries-added': 3,
        'recorded-first-entry-id': '1-1',
        groups: 1,
        'first-entry': ['1-1', ['order', '1', 'total', '9.9']],
        'last-entry': ['2-0', ['order', '3', 'total', '30']],
      })

      const groups = (await streams.xinfo('GROUPS', key)) as unknown[]

      expect(groups.map(pairs)).toEqual([
        { name: 'g', consumers: 1, pending: 2, 'last-delivered-id': '1-2', 'entries-read': 2, lag: 1 },
      ])

      expect(Object.keys(pairs(groups[0]))).toEqual(['name', 'consumers', 'pending', 'last-delivered-id', 'entries-read', 'lag'])

      const consumers = (await streams.xinfo('CONSUMERS', key, 'g')) as unknown[]

      expect(consumers.map(pairs)).toEqual([{ name: 'w1', pending: 2, idle: expect.any(Number), inactive: expect.any(Number) }])
      expect(Object.keys(pairs(consumers[0]))).toEqual(['name', 'pending', 'idle', 'inactive'])

      const empty = newKey()

      await streams.xgroup('CREATE', empty, 'g', '$', 'MKSTREAM')

      expect(pairs(await streams.xinfo('STREAM', empty))).toMatchObject({
        length: 0,
        'last-generated-id': '0-0',
        'recorded-first-entry-id': '0-0',
        'first-entry': null,
        'last-entry': null,
      })

      expect(await streams.xinfo('GROUPS', await orders())).toEqual([])

      await expect(streams.xinfo('STREAM', newKey())).rejects.toThrow(/^ERR no such key/)
    })
  })
}

suite('ndb', () => {
  const db = unixDb()
  const streams = ioredis(db)

  return { streams, writer: streams, close: () => db.close() }
})

if (redisUrl !== '') {
  suite('redis', () => {
    const streams = new Redis(redisUrl)
    const writer = new Redis(redisUrl)

    return {
      streams,
      writer,
      close: async (keys) => {
        if (keys.length > 0) await streams.del(...keys)

        await Promise.all([streams.quit(), writer.quit()])
      },
    }
  })
}

describe('ioredis on ndb only', () => {
  const db = unixDb()
  const streams = ioredis(db)

  afterAll(() => db.close())

  it('gives the values of an ndb entry as text', async () => {
    const key = streamKey()
    const id = await db.stream(key).append({ order: 42, paid: true, note: null, items: [{ sku: 'a', qty: 2 }], name: 'Ana' })

    // The daemon keeps the fields of an entry in name order, also in a
    // nested value.
    expect(await streams.xrange(key, '-', '+')).toEqual([
      [id, ['items', '[{"qty":2,"sku":"a"}]', 'name', 'Ana', 'note', 'null', 'order', '42', 'paid', 'true']],
    ])

    await streams.xadd(key, '*', 'order', 43)

    const last = await db.stream(key).revRange('+', '-', 1)

    expect(last[0]!.doc).toEqual({ order: '43' })
  })

  it('gives errors that are ndb errors with the text of Redis', async () => {
    const key = streamKey()

    await streams.xgroup('CREATE', key, 'g', '$', 'MKSTREAM')

    const busy = await streams.xgroup('CREATE', key, 'g', '$', 'MKSTREAM').catch((err: unknown) => err)

    expect(busy).toBeInstanceOf(ReplyError)
    expect(busy).toBeInstanceOf(NdbError)
    expect(busy).toMatchObject({ name: 'ReplyError', code: 'group_exists', status: 409 })
    expect((busy as ReplyError).message).toBe('BUSYGROUP Consumer Group name already exists')

    const noGroup = await streams.xpending(key, 'nope').catch((err: unknown) => err)

    expect(noGroup).toMatchObject({ code: 'unknown_group', message: `NOGROUP No such key '${key}' or consumer group 'nope'` })
    expect((noGroup as ReplyError).cause).toBeInstanceOf(UnknownGroupError)
  })

  it('does not block on more than one stream', async () => {
    const one = streamKey()
    const two = streamKey()

    await expect(streams.xread('BLOCK', 100, 'STREAMS', one, two, '$', '$')).rejects.toMatchObject({ code: 'not_supported' })

    const read = streams.xreadgroup('GROUP', 'g', 'w1', 'BLOCK', 100, 'STREAMS', one, two, '>', '>')

    await expect(read).rejects.toThrow(/^ERR ndb: XREADGROUP with BLOCK on more than one stream is not supported/)
  })

  it('rejects bad arguments and what ndb does not have', async () => {
    const key = streamKey()

    await expect(streams.xadd(key, '*', 'order')).rejects.toThrow(/wrong number of arguments for 'xadd'/)
    await expect(streams.xread('STREAMS', key)).rejects.toThrow(/Unbalanced/)
    await expect(streams.xrange(key, '-', '+', 'COUNT', 'ten')).rejects.toThrow(/not an integer/)
    await expect(streams.xinfo('STREAM', key, 'FULL')).rejects.toMatchObject({ code: 'not_supported' })
    await expect(streams.xclaim(key, 'g', 'w1', 0, '1-1', 'TIME', 5)).rejects.toMatchObject({ code: 'not_supported' })
    await expect(streams.xread('STREAMS', key, '+')).rejects.toMatchObject({ code: 'not_supported' })
  })

  it('reads two streams of a group without BLOCK', async () => {
    const one = streamKey()
    const two = streamKey()

    await streams.xgroup('CREATE', one, 'g', '$', 'MKSTREAM')
    await streams.xgroup('CREATE', two, 'g', '$', 'MKSTREAM')
    await streams.xadd(two, '1-1', 'order', '1')

    expect(await streams.xreadgroup('GROUP', 'g', 'w1', 'STREAMS', one, two, '>', '>')).toEqual([[two, [['1-1', ['order', '1']]]]])
  })
})
