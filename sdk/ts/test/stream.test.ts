import { createServer } from 'node:http'
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
  tiers,
  type DeadEntry,
  type Entry,
  type Stream,
  connect,
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

  it('buries at the last failure, not after one more claimIdleMs', async () => {
    const s = orders()
    const g = s.group('workers')

    await g.create(startAll)

    const ids = await appendOrders(s, 7, 7)
    const deadName = `${s.name}_dead`
    const ctl = new AbortController()

    let poisoned = 0

    const run = g.work('w1', () => {
      throw new Error('boom')
    }, {
      blockMs: 50,
      claimIdleMs: 3_600_000,
      maxDeliveries: 1,
      deadLetter: deadName,
      signal: ctl.signal,
      onError: (err) => {
        if (err instanceof PoisonError) poisoned++
      },
    })

    await waitFor(() => poisoned === 1)

    ctl.abort()
    await run

    const dead = await db.stream<DeadEntry>(deadName).range()

    expect(dead.map((e) => e.doc.id)).toEqual([ids[0]])
    expect(dead[0]!.doc.deliveries).toBe(1)
    expect((await g.pending()).count).toBe(0)
  })

  it('buries an own pending entry that a crash left at the limit, without the handler', async () => {
    const s = orders()
    const g = s.group('workers')

    await g.create(startAll)

    const ids = await appendOrders(s, 9, 9)

    await g.read('w1')

    const deadName = `${s.name}_dead`
    const ctl = new AbortController()
    const ran: string[] = []

    let poisoned = 0

    const run = g.work('w1', (e) => void ran.push(e.id), {
      blockMs: 50,
      claimIdleMs: -1,
      maxDeliveries: 1,
      deadLetter: deadName,
      signal: ctl.signal,
      onError: (err) => {
        if (err instanceof PoisonError) poisoned++
      },
    })

    await waitFor(() => poisoned === 1)

    ctl.abort()
    await run

    expect(ran).toEqual([])

    const dead = await db.stream<DeadEntry>(deadName).range()

    expect(dead.map((e) => e.doc.id)).toEqual([ids[0]])
    expect(dead[0]!.doc.deliveries).toBe(1)
    expect((await g.pending()).count).toBe(0)
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

  it('leaves an entry pending when the handler stops on the abort', async () => {
    for (const deadLetter of [true, false]) {
      const s = orders()
      const g = s.group('workers')

      await g.create(startAll)

      const ids = await appendOrders(s, 1, 1)
      const deadName = `${s.name}_dead`
      const ctl = new AbortController()
      const errors: unknown[] = []

      let started = false

      // A long job that stops on the abort, as fetch(url, { signal }) does.
      const handler = (_e: Entry<Order>, signal: AbortSignal) => {
        started = true

        return new Promise((_resolve, reject) => {
          signal.addEventListener('abort', () => reject(signal.reason), { once: true })
        })
      }

      const run = g.work('w1', handler, {
        blockMs: 50,
        claimIdleMs: -1,
        maxDeliveries: 1,
        deadLetter: deadLetter ? deadName : undefined,
        signal: ctl.signal,
        onError: (err) => void errors.push(err),
      })

      await waitFor(() => started)

      ctl.abort()
      await run

      expect(errors).toEqual([])
      expect(await db.stream(deadName).range().catch(() => [])).toEqual([])

      const pending = await g.pendingList()

      expect(pending.map((p) => p.id)).toEqual(ids)
    }
  })

  // A worker that starts before its stream and group waits for them: each
  // failed read goes to onError, and the worker tries again.
  it('waits for a group that does not exist yet', async () => {
    const s = orders()
    const g = s.group('late')
    const ctl = new AbortController()
    const errors: unknown[] = []
    const got: number[] = []

    const run = g.work('w1', (e) => void got.push(e.doc.order), {
      blockMs: 50,
      claimIdleMs: -1,
      signal: ctl.signal,
      onError: (err) => void errors.push(err),
    })

    await waitFor(() => errors.length >= 2)

    const missing = (err: unknown) => {
      const cause = err instanceof Error ? err.cause : undefined

      return cause instanceof UnknownGroupError || cause instanceof NotFoundError
    }

    expect(errors.every(missing)).toBe(true)

    await g.create(startAll)
    await appendOrders(s, 1, 1)

    await waitFor(() => got.length === 1)

    let pending = 1

    while (pending > 0) {
      pending = (await g.pending()).count

      await new Promise((r) => setTimeout(r, 20))
    }

    ctl.abort()
    await run

    expect(got).toEqual([1])
  })

  it('reports a failed pending read at the start and tries again', async () => {
    let calls = 0

    const http = createServer((_req, res) => {
      calls += 1
      res.writeHead(503, { 'content-type': 'application/json' }).end('{"error":{"code":"not_ready","message":"recovering"}}')
    })

    await new Promise<void>((done) => http.listen(0, '127.0.0.1', () => done()))

    const { port } = http.address() as { port: number }
    const down = connect(`tcp://127.0.0.1:${port}`, { readyTimeout: 0 })
    const ctl = new AbortController()
    const errors: unknown[] = []

    const run = down.stream('x').group('g').work('w1', () => {}, { signal: ctl.signal, onError: (err) => void errors.push(err) })

    await waitFor(() => errors.length >= 2)

    ctl.abort()

    await expect(run).resolves.toBeUndefined()
    expect(calls).toBeGreaterThanOrEqual(2)
    expect(String(errors[0])).toMatch(/pending read x\/g/)

    await down.close()
    http.closeAllConnections()
    await new Promise((done) => http.close(done))
  })

  it('still acks the batch when onError throws', async () => {
    const s = orders()
    const g = s.group('workers')

    await g.create(startAll)

    const ids = await appendOrders(s, 1, 2)
    const ctl = new AbortController()
    const seen: number[] = []

    const run = g.work('w1', (e) => {
      seen.push(e.doc.order)

      if (e.doc.order === 1) {
        throw new Error('boom')
      }
    }, {
      blockMs: 50,
      claimIdleMs: -1,
      signal: ctl.signal,
      onError: () => {
        throw new Error('onError failed')
      },
    })

    await waitFor(() => seen.length === 2)
    await new Promise((r) => setTimeout(r, 200))

    ctl.abort()
    await run

    const pending = await g.pendingList()

    expect(pending.map((p) => p.id)).toEqual([ids[0]])
  })
})

describe('tiers', () => {
  it('gives one wait per try, and the last one repeats', () => {
    const wait = tiers(15_000, 60_000, 600_000)

    expect([1, 2, 3, 4, 50].map(wait)).toEqual([15_000, 60_000, 600_000, 600_000, 600_000])
  })

  it('holds a step up to its upTo', () => {
    const wait = tiers({ waitMs: 100, upTo: 2 }, 200, { waitMs: 300, upTo: 6 }, { waitMs: 400 })

    expect([1, 2, 3, 4, 6, 7, 100].map(wait)).toEqual([100, 100, 200, 300, 300, 400, 400])
  })

  it('refuses no wait and a bad wait', () => {
    expect(() => tiers()).toThrow(/at least one wait/)
    expect(() => tiers(-1)).toThrow(/bad wait/)
    expect(() => tiers({ waitMs: Number.NaN })).toThrow(/bad wait/)
  })

  // The post-call outbox of fsw-esl: try 1 waits 15 s, 2-5 wait 1 min, 6-11
  // wait 10 min, 12-23 wait 1 h, then 6 h until the max age of 15 days.
  it('gives the fsw-esl schedule', () => {
    const minute = 60_000
    const hour = 60 * minute
    const maxAgeMs = 15 * 24 * hour

    const wait = tiers(
      { waitMs: 15_000, upTo: 1 },
      { waitMs: minute, upTo: 5 },
      { waitMs: 10 * minute, upTo: 11 },
      { waitMs: hour, upTo: 23 },
      { waitMs: 6 * hour },
    )

    expect(wait(1)).toBe(15_000)
    expect([2, 5].map(wait)).toEqual([minute, minute])
    expect([6, 11].map(wait)).toEqual([10 * minute, 10 * minute])
    expect([12, 23].map(wait)).toEqual([hour, hour])
    expect([24, 81].map(wait)).toEqual([6 * hour, 6 * hour])

    // A fake clock: every try fails; the try after the max age goes to the
    // dead letter without the handler, as the worker does.
    let at = 0
    let tries = 0

    while (at <= maxAgeMs) {
      tries++
      at += wait(tries)
    }

    expect(tries).toBe(81)
    expect(at).toBe(15_000 + 4 * minute + 6 * 10 * minute + 12 * hour + 58 * 6 * hour)
  })
})

describe('stream delayed retry', () => {
  it('nacks, lists retryAt and due, and claims the due entries', async () => {
    const s = orders()
    const g = s.group('workers')

    await g.create(startAll)

    const ids = await appendOrders(s, 1, 3)

    await g.read('w1', { count: 3 })

    expect(await g.nack('w1', [], 1000)).toBe(0)
    expect(await g.nack('w2', [ids[0]!], 1000)).toBe(0)

    const before = Date.now()

    expect(await g.nack('w1', [ids[0]!], 60_000)).toBe(1)
    expect(await g.nack('w1', [ids[1]!], 0)).toBe(1)

    await expect(g.nack('w1', [ids[2]!], 31 * 24 * 3600 * 1000)).rejects.toMatchObject({ code: 'invalid_request' })

    const sum = await g.pending()

    expect(sum).toMatchObject({ count: 3, waiting: 1, consumers: [{ name: 'w1', count: 1 }] })

    const rows = await g.pendingList()

    expect(rows.map((r) => r.consumer)).toEqual(['', '', 'w1'])
    expect(rows.map((r) => r.deliveries)).toEqual([1, 1, 1])
    expect(rows[0]!.retryAt!.getTime()).toBeGreaterThanOrEqual(before + 60_000)
    expect(rows[1]!.retryAt!.getTime()).toBeLessThanOrEqual(Date.now())
    expect(rows[2]!.retryAt).toBeNull()

    expect((await g.pendingList({ due: true })).map((r) => r.id)).toEqual([ids[1]])
    expect((await g.pendingList({ due: false })).map((r) => r.id)).toEqual([ids[0]])

    // A nacked entry has no owner: it is in no pending read, and no claim
    // takes it before its retry time.
    expect((await g.read('w1', { pending: true })).map((e) => e.id)).toEqual([ids[2]])
    expect((await g.claim('w2', 0, [ids[0]!])).entries).toEqual([])

    const justIds = await g.claimDue('w2', { justIds: true })

    expect(justIds).toEqual({ cursor: '0-0', ids: [ids[1]], deleted: [] })
    expect((await g.pendingList({ start: ids[1], end: ids[1] }))[0]).toMatchObject({ consumer: 'w2', deliveries: 1, retryAt: null })

    expect(await g.nack('w2', [ids[1]!], 0)).toBe(1)

    const due = await g.claimDue('w3', { count: 10 })

    expect(due.cursor).toBe('0-0')
    expect(due.entries.map((e) => e.doc.order)).toEqual([2])
    expect((await g.pendingList({ start: ids[1], end: ids[1] }))[0]).toMatchObject({ consumer: 'w3', deliveries: 2 })

    // An ack of a waiting entry cancels its retry.
    expect(await g.ack(ids[0]!)).toBe(1)
    expect((await g.pending()).waiting).toBe(0)
  })

  // Shared case 1 (Go, Ruby, TypeScript).
  it('takes a failed entry again after each wait, and not before', async () => {
    const s = orders()
    const g = s.group('workers')

    await g.create(startAll)
    await appendOrders(s, 1, 1)

    const times: number[] = []
    const tries: number[] = []
    const ctl = new AbortController()

    const retry = (deliveries: number) => {
      tries.push(deliveries)

      return deliveries === 1 ? 600 : 1200
    }

    const handler = () => {
      times.push(Date.now())

      if (times.length <= 2) {
        throw new Error('boom')
      }
    }

    const run = g.work('w1', handler, { blockMs: 50, claimIdleMs: -1, retry, dueEveryMs: 50, signal: ctl.signal })

    await waitFor(() => times.length === 3)

    ctl.abort()
    await run

    expect(tries).toEqual([1, 2])
    expect(times[1]! - times[0]!).toBeGreaterThanOrEqual(600)
    expect(times[1]! - times[0]!).toBeLessThan(600 + 1000)
    expect(times[2]! - times[1]!).toBeGreaterThanOrEqual(1200)
    expect(times[2]! - times[1]!).toBeLessThan(1200 + 1000)
    expect((await g.pending()).count).toBe(0)
  })

  // Shared case 2.
  it('sends an entry older than maxAgeMs to the dead letter, acked, with no retry', async () => {
    const s = orders()
    const g = s.group('workers')

    await g.create(startAll)

    const old = await s.append({ order: 1, total: 0 }, { id: `${Date.now() - 60_000}-1` })
    const fresh = await s.append({ order: 2, total: 0 })
    const deadName = `${s.name}_dead`
    const ctl = new AbortController()
    const ran: number[] = []
    const reasons: string[] = []

    // The old entry does not run; the fresh one fails once it is too old.
    const handler = async (e: Entry<Order>) => {
      ran.push(e.doc.order)

      await new Promise((r) => setTimeout(r, 400))

      throw new Error('boom')
    }

    const run = g.work('w1', handler, {
      blockMs: 50,
      claimIdleMs: -1,
      retry: [100],
      dueEveryMs: 50,
      maxAgeMs: 300,
      deadLetter: deadName,
      signal: ctl.signal,
      onError: (err) => {
        if (err instanceof PoisonError) reasons.push(err.reason)
      },
    })

    await waitFor(() => reasons.length === 2)

    ctl.abort()
    await run

    expect(ran).toEqual([2])
    expect(reasons).toEqual(['max_age', 'max_age'])

    const dead = await db.stream<DeadEntry>(deadName).range()

    expect(dead.map((e) => e.doc.id)).toEqual([old, fresh])
    expect(dead[0]!.doc).toMatchObject({ reason: 'max_age', deliveries: 1, doc: { order: 1, total: 0 } })
    expect(dead[1]!.doc).toMatchObject({ reason: 'max_age', deliveries: 1 })
    expect((await g.pending()).count).toBe(0)
  })

  // Shared case 3.
  it('does not give a waiting entry to a restarted worker early; a worker takes it when due', async () => {
    const s = orders()
    const g = s.group('workers')

    await g.create(startAll)
    await appendOrders(s, 1, 1)

    const first = new AbortController()

    let failed = false

    const failing = () => {
      failed = true

      throw new Error('boom')
    }

    const run1 = g.work('w1', failing, { blockMs: 50, claimIdleMs: -1, retry: [1500], signal: first.signal })

    let row = (await g.pendingList())[0]

    while (!row || row.retryAt === null) {
      await new Promise((r) => setTimeout(r, 20))

      row = (await g.pendingList())[0]
    }

    first.abort()
    await run1

    expect(failed).toBe(true)
    expect(row.consumer).toBe('')

    const retryAt = row.retryAt.getTime()
    const second = new AbortController()
    const took: { who: string; at: number }[] = []

    const runs = ['w1', 'w2'].map((who) => {
      const opts = { blockMs: 50, claimIdleMs: 100, claimEveryMs: 100, retry: [1500], dueEveryMs: 50, signal: second.signal }

      return g.work(who, () => void took.push({ who, at: Date.now() }), opts)
    })

    await new Promise((r) => setTimeout(r, 300))

    expect(took).toEqual([])

    await waitFor(() => took.length === 1)
    await new Promise((r) => setTimeout(r, 200))

    second.abort()
    await Promise.all(runs)

    expect(took).toHaveLength(1)
    expect(took[0]!.at).toBeGreaterThanOrEqual(retryAt)
    expect(took[0]!.at).toBeLessThan(retryAt + 1000)
    expect((await g.pending()).count).toBe(0)
  })

  it('buries an entry that fails on its maxDeliveries-th delivery instead of a nack', async () => {
    const s = orders()
    const g = s.group('workers')

    await g.create(startAll)

    const ids = await appendOrders(s, 1, 1)
    const deadName = `${s.name}_dead`
    const ctl = new AbortController()
    const tries: number[] = []

    let poisoned = 0

    const run = g.work('w1', () => {
      tries.push(Date.now())

      throw new Error('boom')
    }, {
      blockMs: 50,
      claimIdleMs: -1,
      retry: [100],
      dueEveryMs: 50,
      maxDeliveries: 2,
      deadLetter: deadName,
      signal: ctl.signal,
      onError: (err) => {
        if (err instanceof PoisonError) poisoned++
      },
    })

    await waitFor(() => poisoned === 1)

    ctl.abort()
    await run

    expect(tries).toHaveLength(2)

    const dead = await db.stream<DeadEntry>(deadName).range()

    expect(dead.map((e) => e.doc)).toMatchObject([{ id: ids[0], deliveries: 2, reason: 'max_deliveries' }])
    expect((await g.pending()).count).toBe(0)
  })
})
