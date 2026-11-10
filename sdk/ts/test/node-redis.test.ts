import { createClient, type RedisClientType } from 'redis'
import { afterAll, beforeAll, describe, expect, it } from 'vitest'
import { NdbError } from '../src/index.js'
import { ReplyError, nodeRedis, type NodeRedisStreams } from '../src/node-redis.js'
import { unixDb } from './helpers.js'
import { later, redisUrl, streamKey } from './redis-backend.js'

// The commands under test, with the types of node-redis: the adapter and a
// real client run the same cases. The two info answers have loose types
// here: redis.test-d.ts tells why.
type Commands = Pick<RedisClientType, Exclude<keyof NodeRedisStreams, 'xInfoStream' | 'xInfoGroups'>> & {
  xInfoStream(key: string): Promise<object>
  xInfoGroups(key: string): Promise<object[]>
}

interface Backend {
  streams: Commands
  // A second connection: a blocked read holds the first one.
  writer: Commands
  close: (keys: string[]) => Promise<unknown>
}

const idShape = /^\d+-\d+$/

const first = { id: '1-1', message: { order: '1', total: '9.9' } }
const second = { id: '1-2', message: { order: '2', total: '20' } }
const third = { id: '2-0', message: { order: '3', total: '30' } }

function suite(name: string, open: () => Promise<Backend>): void {
  describe(`node-redis commands on ${name}`, () => {
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

      await streams.xAdd(key, '1-1', first.message)
      await streams.xAdd(key, '1-2', second.message)
      await streams.xAdd(key, '2-0', third.message)

      return key
    }

    beforeAll(async () => {
      ({ streams, writer, close } = await open())
    })

    afterAll(() => close(keys))

    it('xAdd, xLen, xRange and xRevRange', async () => {
      const key = newKey()

      expect(await streams.xLen(key)).toBe(0)
      expect(await streams.xRange(key, '-', '+')).toEqual([])

      expect(await streams.xAdd(key, '*', { order: '1', total: '9.9' })).toMatch(idShape)
      expect(await streams.xAdd(key, '9999999999999-1', { order: '2', total: '20' })).toBe('9999999999999-1')
      expect(await streams.xAdd(key, '9999999999999-*', { order: '3', total: '30' })).toBe('9999999999999-2')

      await expect(streams.xAdd(key, '1-1', { order: '4' })).rejects.toThrow(/^ERR The ID specified in XADD is equal or smaller/)

      expect(await streams.xLen(key)).toBe(3)

      const all = await streams.xRange(key, '-', '+')

      expect(all).toHaveLength(3)
      expect(all[0]!.id).toMatch(idShape)
      expect(all[0]!.message).toEqual({ order: '1', total: '9.9' })
      expect(Object.getPrototypeOf(all[0]!.message)).toBeNull()
      expect(all[1]).toEqual({ id: '9999999999999-1', message: { order: '2', total: '20' } })

      expect(await streams.xRange(key, '-', '+', { COUNT: 1 })).toEqual([all[0]])
      expect(await streams.xRange(key, '(9999999999999-1', '+')).toEqual([all[2]])

      expect(await streams.xRevRange(key, '+', '-')).toEqual([all[2], all[1], all[0]])
      expect(await streams.xRevRange(key, '9999999999999-1', '-', { COUNT: 1 })).toEqual([all[1]])
    })

    it('xAdd with TRIM, and xAddNoMkStream', async () => {
      const key = newKey()

      expect(await streams.xAddNoMkStream(key, '*', { order: '1' })).toBeNull()
      expect(await streams.xLen(key)).toBe(0)

      for (let i = 1; i <= 5; i++) {
        await streams.xAdd(key, `${i}-0`, { order: String(i) }, { TRIM: { strategy: 'MAXLEN', threshold: 2 } })
      }

      expect((await streams.xRange(key, '-', '+')).map((m) => m.id)).toEqual(['4-0', '5-0'])

      const approx = { TRIM: { strategy: 'MAXLEN', strategyModifier: '~', threshold: 1000, limit: 10 } } as const

      expect(await streams.xAdd(key, '6-0', { order: '6' }, approx)).toBe('6-0')
      expect(await streams.xLen(key)).toBe(3)

      expect(await streams.xAddNoMkStream(key, '7-0', { order: '7' }, { TRIM: { strategy: 'MAXLEN', threshold: 1 } })).toBe('7-0')
      expect((await streams.xRange(key, '-', '+')).map((m) => m.id)).toEqual(['7-0'])
    })

    it('xDel and xTrim', async () => {
      const key = await orders()

      expect(await streams.xDel(key, ['1-1', '7-7'])).toBe(1)
      expect(await streams.xDel(newKey(), '1-1')).toBe(0)

      expect(await streams.xTrim(key, 'MAXLEN', 1, { strategyModifier: '~', LIMIT: 10 })).toBe(0)
      expect(await streams.xTrim(key, 'MINID', '2-0')).toBe(1)
      expect(await streams.xTrim(key, 'MAXLEN', 0, { strategyModifier: '=' })).toBe(1)
      expect(await streams.xTrim(newKey(), 'MAXLEN', 1)).toBe(0)

      expect(await streams.xLen(key)).toBe(0)
    })

    it('xRead', async () => {
      const key = await orders()
      const other = newKey()
      const none = newKey()

      await streams.xAdd(other, '5-0', { order: '9' })

      expect(await streams.xRead({ key, id: '0' }, { COUNT: 2 })).toEqual([{ name: key, messages: [first, second] }])
      expect(await streams.xRead([{ key, id: '1-2' }])).toEqual([{ name: key, messages: [third] }])
      expect(await streams.xRead({ key, id: '2-0' })).toBeNull()
      expect(await streams.xRead({ key, id: '$' })).toBeNull()
      expect(await streams.xRead({ key: none, id: '0' })).toBeNull()

      const many = [
        { key, id: '1-2' },
        { key: none, id: '0' },
        { key: other, id: '0' },
      ]

      expect(await streams.xRead(many, { COUNT: 1 })).toEqual([
        { name: key, messages: [third] },
        { name: other, messages: [{ id: '5-0', message: { order: '9' } }] },
      ])
    })

    it('reads every entry with COUNT 0, and rejects a negative BLOCK and LIMIT without ~', async () => {
      const key = newKey()

      await streams.xGroupCreate(key, 'g', '0', { MKSTREAM: true })

      for (let i = 1; i <= 15; i++) {
        await streams.xAdd(key, `${i}-1`, { i: String(i) })
      }

      const read = await streams.xRead({ key, id: '0' }, { COUNT: 0 })
      const group = await streams.xReadGroup('g', 'w1', { key, id: '>' }, { COUNT: 0 })

      expect(read![0]!.messages).toHaveLength(15)
      expect(group![0]!.messages).toHaveLength(15)

      await expect(streams.xRead({ key, id: '$' }, { BLOCK: -1 })).rejects.toThrow(/^ERR timeout is negative/)
      await expect(streams.xTrim(key, 'MAXLEN', 0, { LIMIT: 10 })).rejects.toThrow(/^ERR syntax error, LIMIT cannot be used without the special ~ option/)

      expect(await streams.xLen(key)).toBe(15)
    })

    it('xRead with BLOCK waits for an append', async () => {
      const key = await orders()

      expect(await streams.xRead({ key, id: '$' }, { BLOCK: 50 })).toBeNull()

      later(200, () => writer.xAdd(key, '3-0', { order: '4' }))

      const start = Date.now()

      expect(await streams.xRead({ key, id: '$' }, { COUNT: 5, BLOCK: 5000 })).toEqual([
        { name: key, messages: [{ id: '3-0', message: { order: '4' } }] },
      ])

      expect(Date.now() - start).toBeLessThan(4000)

      later(200, () => writer.xAdd(key, '4-0', { order: '5' }))

      expect(await streams.xRead({ key, id: '3-0' }, { BLOCK: 0 })).toEqual([
        { name: key, messages: [{ id: '4-0', message: { order: '5' } }] },
      ])
    })

    it('xGroupCreate, xGroupSetId, xGroupDestroy and the consumers', async () => {
      const key = newKey()

      await expect(streams.xGroupCreate(key, 'g', '$')).rejects.toThrow(/^ERR The XGROUP subcommand requires the key to exist/)

      expect(await streams.xGroupCreate(key, 'g', '$', { MKSTREAM: true })).toBe('OK')
      expect(await streams.xLen(key)).toBe(0)

      await expect(streams.xGroupCreate(key, 'g', '$', { MKSTREAM: true })).rejects.toThrow(/^BUSYGROUP/)

      expect(await streams.xGroupCreateConsumer(key, 'g', 'w1')).toBe(1)
      expect(await streams.xGroupCreateConsumer(key, 'g', 'w1')).toBe(0)
      expect(await streams.xGroupDelConsumer(key, 'g', 'w1')).toBe(0)

      expect(await streams.xGroupSetId(key, 'g', '0')).toBe('OK')
      expect(await streams.xGroupSetId(key, 'g', '$', { ENTRIESREAD: 0 })).toBe('OK')

      await expect(streams.xGroupSetId(key, 'nope', '0')).rejects.toThrow(/^NOGROUP/)

      expect(await streams.xGroupDestroy(key, 'g')).toBe(1)
      expect(await streams.xGroupDestroy(key, 'g')).toBe(0)
    })

    it('xReadGroup, xAck, xPending, xClaim and xAutoClaim', async () => {
      const key = await orders()

      expect(await streams.xGroupCreate(key, 'g', '0')).toBe('OK')

      expect(await streams.xPending(key, 'g')).toEqual({ pending: 0, firstId: null, lastId: null, consumers: null })
      expect(await streams.xPendingRange(key, 'g', '-', '+', 10)).toEqual([])

      expect(await streams.xReadGroup('g', 'w1', { key, id: '>' }, { COUNT: 2 })).toEqual([{ name: key, messages: [first, second] }])

      expect(await streams.xAck(key, 'g', '1-1')).toBe(1)
      expect(await streams.xAck(key, 'g', ['1-1', '7-7'])).toBe(0)
      expect(await streams.xAck(key, 'nope', '1-2')).toBe(0)

      expect(await streams.xPending(key, 'g')).toEqual({
        pending: 1,
        firstId: '1-2',
        lastId: '1-2',
        consumers: [{ name: 'w1', deliveriesCounter: 1 }],
      })

      expect(await streams.xReadGroup('g', 'w1', [{ key, id: '0' }])).toEqual([{ name: key, messages: [second] }])
      expect(await streams.xReadGroup('g', 'idle', { key, id: '0' })).toEqual([{ name: key, messages: [] }])

      expect(await streams.xPendingRange(key, 'g', '-', '+', 10, { consumer: 'w1' })).toEqual([
        { id: '1-2', consumer: 'w1', millisecondsSinceLastDelivery: expect.any(Number), deliveriesCounter: 2 },
      ])

      expect(await streams.xPendingRange(key, 'g', '-', '+', 10, { IDLE: 600_000 })).toEqual([])
      expect(await streams.xPendingRange(key, 'g', '-', '+', 10, { consumer: 'idle' })).toEqual([])

      expect(await streams.xClaim(key, 'g', 'w2', 600_000, '1-2')).toEqual([])
      expect(await streams.xClaim(key, 'g', 'w2', 0, ['1-2', '7-7'])).toEqual([second])
      expect(await streams.xClaimJustId(key, 'g', 'w1', 0, '1-2')).toEqual(['1-2'])
      expect(await streams.xClaim(key, 'g', 'w2', 0, '1-2', { IDLE: 5000, RETRYCOUNT: 7, FORCE: true })).toEqual([second])

      const moved = await streams.xPendingRange(key, 'g', '-', '+', 10)

      expect(moved).toEqual([{ id: '1-2', consumer: 'w2', millisecondsSinceLastDelivery: expect.any(Number), deliveriesCounter: 7 }])
      expect(moved[0]!.millisecondsSinceLastDelivery).toBeGreaterThanOrEqual(5000)

      expect(await streams.xAutoClaim(key, 'g', 'w3', 600_000_000, '0')).toEqual({ nextId: '0-0', messages: [], deletedMessages: [] })

      expect(await streams.xAutoClaim(key, 'g', 'w3', 0, '0', { COUNT: 10 })).toEqual({
        nextId: '0-0',
        messages: [second],
        deletedMessages: [],
      })

      expect(await streams.xAutoClaimJustId(key, 'g', 'w3', 0, '0-0')).toEqual({
        nextId: '0-0',
        messages: ['1-2'],
        deletedMessages: [],
      })

      expect(await streams.xReadGroup('g', 'w4', { key, id: '>' }, { NOACK: true })).toEqual([{ name: key, messages: [third] }])
      expect(await streams.xReadGroup('g', 'w4', { key, id: '>' })).toBeNull()

      expect(await streams.xGroupDelConsumer(key, 'g', 'w3')).toBe(1)
      expect((await streams.xPending(key, 'g')).pending).toBe(0)
    })

    it('gives NOGROUP for a group or a stream that does not exist', async () => {
      const key = await orders()
      const none = newKey()

      await expect(streams.xReadGroup('nope', 'w1', { key, id: '>' })).rejects.toThrow(/^NOGROUP/)
      await expect(streams.xReadGroup('nope', 'w1', { key: none, id: '>' })).rejects.toThrow(/^NOGROUP/)
      await expect(streams.xPending(key, 'nope')).rejects.toThrow(/^NOGROUP/)
      await expect(streams.xClaim(key, 'nope', 'w1', 0, '1-1')).rejects.toThrow(/^NOGROUP/)
      await expect(streams.xAutoClaim(key, 'nope', 'w1', 0, '0')).rejects.toThrow(/^NOGROUP/)
      await expect(streams.xInfoConsumers(key, 'nope')).rejects.toThrow(/^NOGROUP/)
    })

    it('xReadGroup with BLOCK waits for an append', async () => {
      const key = await orders()

      await streams.xGroupCreate(key, 'g', '$')

      expect(await streams.xReadGroup('g', 'w1', { key, id: '>' }, { BLOCK: 50 })).toBeNull()

      later(200, () => writer.xAdd(key, '3-0', { order: '4' }))

      const start = Date.now()
      const got = await streams.xReadGroup('g', 'w1', { key, id: '>' }, { COUNT: 5, BLOCK: 5000 })

      expect(got).toEqual([{ name: key, messages: [{ id: '3-0', message: { order: '4' } }] }])
      expect(Date.now() - start).toBeLessThan(4000)
    })

    it('drops a pending entry that was deleted from xAutoClaim', async () => {
      const key = await orders()

      await streams.xGroupCreate(key, 'g', '0')
      await streams.xReadGroup('g', 'w1', { key, id: '>' }, { COUNT: 2 })

      expect(await streams.xDel(key, '1-1')).toBe(1)

      expect(await streams.xAutoClaim(key, 'g', 'w2', 0, '0')).toEqual({
        nextId: '0-0',
        messages: [second],
        deletedMessages: ['1-1'],
      })

      expect((await streams.xPending(key, 'g')).pending).toBe(1)
    })

    it('xInfoStream, xInfoGroups and xInfoConsumers', async () => {
      const key = await orders()

      await streams.xGroupCreate(key, 'g', '0')
      await streams.xReadGroup('g', 'w1', { key, id: '>' }, { COUNT: 2 })

      const info = await streams.xInfoStream(key)

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
        'first-entry': first,
        'last-entry': third,
      })

      expect(await streams.xInfoGroups(key)).toEqual([
        { name: 'g', consumers: 1, pending: 2, 'last-delivered-id': '1-2', 'entries-read': 2, lag: 1 },
      ])

      expect(await streams.xInfoConsumers(key, 'g')).toEqual([
        { name: 'w1', pending: 2, idle: expect.any(Number), inactive: expect.any(Number) },
      ])

      const empty = newKey()

      await streams.xGroupCreate(empty, 'g', '$', { MKSTREAM: true })

      expect(await streams.xInfoStream(empty)).toMatchObject({ length: 0, 'first-entry': null, 'last-entry': null })
      expect(await streams.xInfoGroups(await orders())).toEqual([])

      await expect(streams.xInfoStream(newKey())).rejects.toThrow(/^ERR no such key/)
    })
  })
}

suite('ndb', async () => {
  const db = unixDb()
  const streams = nodeRedis(db)

  return { streams, writer: streams, close: () => db.close() }
})

if (redisUrl !== '') {
  suite('redis', async () => {
    const streams = createClient({ url: redisUrl }) as RedisClientType
    const writer = streams.duplicate()

    await Promise.all([streams.connect(), writer.connect()])

    return {
      streams,
      writer,
      close: async (keys) => {
        if (keys.length > 0) await streams.del(keys)

        await Promise.all([streams.close(), writer.close()])
      },
    }
  })
}

describe('node-redis on ndb only', () => {
  const db = unixDb()
  const streams = nodeRedis(db)

  afterAll(() => db.close())

  it('gives the values of an ndb entry as text', async () => {
    const key = streamKey()
    const id = await db.stream(key).append({ order: 42, paid: true, note: null, items: [{ sku: 'a', qty: 2 }], name: 'Ana' })

    expect(await streams.xRange(key, '-', '+')).toEqual([
      { id, message: { order: '42', paid: 'true', note: 'null', items: '[{"qty":2,"sku":"a"}]', name: 'Ana' } },
    ])

    await streams.xAdd(key, '*', { order: 43 })

    const last = await db.stream(key).revRange('+', '-', 1)

    expect(last[0]!.doc).toEqual({ order: '43' })
  })

  it('gives errors that are ndb errors with the text of Redis', async () => {
    const key = streamKey()

    await streams.xGroupCreate(key, 'g', '$', { MKSTREAM: true })

    const busy = await streams.xGroupCreate(key, 'g', '$', { MKSTREAM: true }).catch((err: unknown) => err)

    expect(busy).toBeInstanceOf(ReplyError)
    expect(busy).toBeInstanceOf(NdbError)
    expect(busy).toMatchObject({ code: 'group_exists', message: 'BUSYGROUP Consumer Group name already exists' })
  })

  it('does not block on more than one stream, and has no CLAIM and no TIME', async () => {
    const one = streamKey()
    const two = streamKey()

    const both = [
      { key: one, id: '$' },
      { key: two, id: '$' },
    ]

    await expect(streams.xRead(both, { BLOCK: 100 })).rejects.toMatchObject({ code: 'not_supported' })
    await expect(streams.xReadGroup('g', 'w1', both, { BLOCK: 100 })).rejects.toThrow(/more than one stream is not supported/)

    const claim = { CLAIM: 100 } as object
    const time = { TIME: 5 } as object
    const acked = { policy: 'ACKED' } as object

    await expect(streams.xReadGroup('g', 'w1', { key: one, id: '>' }, claim)).rejects.toMatchObject({ code: 'not_supported' })
    await expect(streams.xClaim(one, 'g', 'w1', 0, '1-1', time)).rejects.toMatchObject({ code: 'not_supported' })
    await expect(streams.xTrim(one, 'MAXLEN', 1, acked)).rejects.toMatchObject({ code: 'not_supported' })
    await expect(streams.xAdd(one, '*', { order: '1' }, { TRIM: { threshold: 1 } })).rejects.toThrow(/TRIM needs the strategy/)
  })
})
