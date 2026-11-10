import { afterAll, describe, expect, it } from 'vitest'
import {
  GroupExistsError,
  NotFoundError,
  PoisonError,
  StreamIdError,
  UnknownGroupError,
  idTime,
  startAll,
  startNew,
  type DeadEntry,
  type Entry,
  type Stream,
} from '../src/index.js'
import { collName, unixDb } from './helpers.js'

interface Order {
  order: number
  total: number
}

const db = unixDb()

afterAll(() => db.close())

function orders(): Stream<Order> {
  return db.stream<Order>(collName())
}

function appendOrders(s: Stream<Order>, from: number, to: number): Promise<string[]> {
  const docs: Order[] = []

  for (let i = from; i <= to; i++) {
    docs.push({ order: i, total: i * 10 })
  }

  return s.appendMany(docs)
}

async function waitFor(ok: () => boolean): Promise<void> {
  const deadline = Date.now() + 15_000

  while (!ok()) {
    if (Date.now() > deadline) {
      throw new Error('timed out')
    }

    await new Promise((r) => setTimeout(r, 20))
  }
}

describe('stream', () => {
  it('appends, gives info and ranges', async () => {
    const s = orders()
    const id = await s.append({ order: 1, total: 9.9 })

    expect(Number.isNaN(idTime(id).getTime())).toBe(false)

    const ids = await appendOrders(s, 2, 4)

    expect(ids).toHaveLength(3)

    const info = await s.info()

    expect(info).toMatchObject({ length: 4, first_id: id, last_id: ids[2], entries_added: 4 })

    const all = await s.range()

    expect(all).toHaveLength(4)
    expect(all[0]!.doc).toEqual({ order: 1, total: 9.9 })
    expect(all[0]!.time.getTime()).toBe(idTime(id).getTime())

    const last = await s.revRange('+', '-', 1)

    expect(last.map((e) => e.id)).toEqual([ids[2]])

    await expect(s.append({ order: 0, total: 0 }, { id: '1-1' })).rejects.toBeInstanceOf(StreamIdError)

    expect(await s.delete(id)).toBe(1)
    expect(await s.trim({ maxLen: 1 })).toBe(2)
  })

  it('trims on append and honors noCreate', async () => {
    const s = orders()

    await expect(s.append({ order: 1, total: 0 }, { noCreate: true })).rejects.toBeInstanceOf(NotFoundError)

    await appendOrders(s, 1, 5)
    await s.append({ order: 6, total: 0 }, { trim: { maxLen: 2 } })

    expect((await s.info()).length).toBe(2)
  })

  it('reads, acks, lists pending and claims in a group', async () => {
    const s = orders()
    const g = s.group('workers')

    await g.create(startAll)

    await expect(g.create(startAll)).rejects.toBeInstanceOf(GroupExistsError)

    await g.ensure(startAll)

    const ids = await appendOrders(s, 1, 3)
    const got = await g.read('w1', { count: 2 })

    expect(got.map((e) => e.id)).toEqual([ids[0], ids[1]])
    expect(got[1]!.doc.order).toBe(2)

    expect(await g.ack(ids[0]!)).toBe(1)

    const sum = await g.pending()

    expect(sum.count).toBe(1)
    expect(sum.lowest).toBe(ids[1])
    expect(sum.consumers.map((c) => c.name)).toEqual(['w1'])

    const mine = await g.read('w1', { pending: true })

    expect(mine.map((e) => e.id)).toEqual([ids[1]])

    const list = await g.pendingList({ consumer: 'w1' })

    expect(list).toEqual([expect.objectContaining({ id: ids[1], consumer: 'w1', deliveries: 2 })])

    const claimed = await g.claim('w2', 0, [ids[1]!])

    expect(claimed.entries.map((e) => e.doc.order)).toEqual([2])

    const auto = await g.autoClaim('w3', 0, '0-0', 10)

    expect(auto.cursor).toBe('0-0')
    expect(auto.entries).toHaveLength(1)

    expect(await g.consumers()).toHaveLength(3)
    expect(await g.createConsumer('w4')).toBe(true)
    expect(await g.createConsumer('w4')).toBe(false)

    const groups = await s.groups()

    expect(groups).toHaveLength(1)
    expect(groups[0]).toMatchObject({ name: 'workers', pending: 1, last_delivered_id: ids[1] })

    expect(await g.deleteConsumer('w3')).toBe(1)

    await g.destroy()

    await expect(g.read('w1')).rejects.toBeInstanceOf(UnknownGroupError)
  })

  it('wakes a blocked read with an append', async () => {
    const s = orders()
    const g = s.group('workers')

    await g.create(startNew)

    setTimeout(() => void s.append({ order: 7, total: 0 }), 200)

    const start = Date.now()
    const got = await g.read('w1', { blockMs: 5000 })

    expect(got.map((e) => e.doc.order)).toEqual([7])
    expect(Date.now() - start).toBeLessThan(4000)

    expect(await g.read('w1', { blockMs: 50 })).toEqual([])
  })

  it('ends a blocked read when the signal aborts', async () => {
    const s = orders()
    const g = s.group('workers')

    await g.create(startNew)

    const ctl = new AbortController()

    setTimeout(() => ctl.abort(), 100)

    const start = Date.now()

    await expect(g.read('w1', { blockMs: 10_000, signal: ctl.signal })).rejects.toBeDefined()

    expect(Date.now() - start).toBeLessThan(5000)
  })

  it('reads without a group, from an ID and from "$"', async () => {
    const s = orders()
    const ids = await appendOrders(s, 1, 4)

    const first = await s.read({ count: 2 })

    expect(first.map((e) => e.id)).toEqual([ids[0], ids[1]])
    expect(first[0]!.doc).toEqual({ order: 1, total: 10 })

    const next = await s.read({ after: ids[1] })

    expect(next.map((e) => e.id)).toEqual([ids[2], ids[3]])

    const picked = await s.read({ after: ids[2], fields: ['order'] })

    expect(picked.map((e) => e.doc)).toEqual([{ order: 4 }])

    expect(await s.read({ after: '$' })).toEqual([])
    expect(await s.read({ after: ids[3], blockMs: 50 })).toEqual([])
    expect((await s.info()).groups).toBe(0)
  })

  it('wakes a blocked read without a group with an append', async () => {
    const s = orders()

    await appendOrders(s, 1, 1)

    setTimeout(() => void s.append({ order: 9, total: 0 }), 200)

    const start = Date.now()
    const got = await s.read({ after: '$', blockMs: 5000 })

    expect(got.map((e) => e.doc.order)).toEqual([9])
    expect(Date.now() - start).toBeLessThan(4000)
  })

  it('ends a blocked read without a group when the signal aborts', async () => {
    const s = orders()

    await s.create()

    const ctl = new AbortController()

    setTimeout(() => ctl.abort(), 100)

    const start = Date.now()

    await expect(s.read({ after: '$', blockMs: 10_000, signal: ctl.signal })).rejects.toBeDefined()

    expect(Date.now() - start).toBeLessThan(5000)
  })
})

describe('stream worker', () => {
  it('does every entry once with 3 workers', async () => {
    const s = orders()
    const g = s.group('workers')

    await g.create(startAll)
    await appendOrders(s, 1, 50)

    const seen = new Map<number, number>()
    const ctl = new AbortController()

    const handler = (e: Entry<Order>) => {
      seen.set(e.doc.order, (seen.get(e.doc.order) ?? 0) + 1)
    }

    const workers = ['w1', 'w2', 'w3'].map((name) => g.work(name, handler, { blockMs: 100, signal: ctl.signal }))

    await appendOrders(s, 51, 60)
    await waitFor(() => seen.size === 60)

    ctl.abort()
    await Promise.all(workers)

    expect([...seen.values()].every((n) => n === 1)).toBe(true)
    expect((await g.pending()).count).toBe(0)
  })

  it('retries and buries poison into the dead letter', async () => {
    const s = orders()
    const g = s.group('workers')

    await g.create(startAll)

    const ids = await appendOrders(s, 1, 2)
    const deadName = `${s.name}_dead`
    const tries = new Map<number, number>()
    const ctl = new AbortController()

    let poisoned = 0

    const handler = (e: Entry<Order>) => {
      const n = (tries.get(e.doc.order) ?? 0) + 1

      tries.set(e.doc.order, n)

      if (e.doc.order === 2 || (e.doc.order === 1 && n === 1)) {
        throw new Error('boom')
      }
    }

    const run = g.work('w1', handler, {
      blockMs: 50,
      claimIdleMs: 100,
      claimEveryMs: 100,
      maxDeliveries: 3,
      deadLetter: deadName,
      signal: ctl.signal,
      onError: (err) => {
        if (err instanceof PoisonError) poisoned++
      },
    })

    await waitFor(() => poisoned === 1)

    ctl.abort()
    await run

    expect(tries.get(1)).toBe(2)
    expect(tries.get(2)).toBe(3)
    expect((await g.pending()).count).toBe(0)

    const dead = await db.stream<DeadEntry>(deadName).range()

    expect(dead).toHaveLength(1)
    expect(dead[0]!.doc).toMatchObject({ stream: s.name, group: 'workers', id: ids[1], deliveries: 3 })
    expect(dead[0]!.doc.doc).toEqual({ order: 2, total: 20 })
  })

  it('starts with its own pending entries', async () => {
    const s = orders()
    const g = s.group('workers')

    await g.create(startAll)
    await appendOrders(s, 1, 3)

    await g.read('w1')

    const got: number[] = []
    const ctl = new AbortController()

    const run = g.work('w1', (e) => void got.push(e.doc.order), { blockMs: 50, claimIdleMs: -1, signal: ctl.signal })

    await waitFor(() => got.length === 3)

    ctl.abort()
    await run

    expect(got).toEqual([1, 2, 3])
  })
})
