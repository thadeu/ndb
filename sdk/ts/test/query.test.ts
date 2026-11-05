import { afterAll, beforeAll, describe, expect, it } from 'vitest'
import { InvalidQueryError } from '../src/index.js'
import { callsModel } from './fixtures.js'
import { unixDb } from './helpers.js'

const db = unixDb()
const calls = callsModel(db)

beforeAll(async () => {
  const docs = Array.from({ length: 12 }, (_, i) => ({
    call_id: `c${String(i).padStart(2, '0')}`,
    status: i % 3 === 0 ? 'missed' : 'answered',
    dur: i,
    ts: new Date(Date.UTC(2026, 10, 1, i)),
  }))

  await calls.insert(docs)
})

afterAll(() => db.close())

describe('query builder', () => {
  it('is thenable: await runs it', async () => {
    const rows = await calls.where({ status: 'missed' }).sort({ call_id: 1 })

    expect(rows.map((r) => r.call_id)).toEqual(['c00', 'c03', 'c06', 'c09'])
  })

  it('combines where, not, or, limit, skip and fields', async () => {
    const rows = await calls
      .where({ dur: { $gte: 2 } })
      .not({ status: 'missed' })
      .or({ dur: { $lt: 5 } }, { dur: { $gt: 9 } })
      .sort({ dur: -1 })
      .skip(1)
      .limit(3)
      .fields('call_id', 'dur')

    expect(rows.map((r) => r.dur)).toEqual([10, 4, 2])
    expect(Object.keys(rows[0]!).sort()).toEqual(['_v', 'call_id', 'dur'])
  })

  it('merges two where clauses on the same field with $and', async () => {
    const rows = await calls.where({ dur: { $gt: 2 } }).where({ dur: { $lt: 5 } })

    expect(rows.map((r) => r.dur).sort()).toEqual([3, 4])
  })

  it('counts and returns the first row', async () => {
    expect(await calls.where({ status: 'missed' }).count()).toBe(4)
    expect((await calls.sort({ dur: -1 }).first())?.dur).toBe(11)
    expect(await calls.where({ status: 'zzz' }).first()).toBeNull()
  })

  it('pages with page(n, size)', async () => {
    const p3 = await calls.sort({ call_id: 1 }).page(3, 4)

    expect(p3.map((r) => r.call_id)).toEqual(['c08', 'c09', 'c10', 'c11'].slice(0, 4))
  })

  it('follows a cursor with after(token) and nextCursor', async () => {
    const q1 = calls.sort({ call_id: 1 }).limit(5)
    const first = await q1

    expect(first).toHaveLength(5)
    expect(q1.nextCursor).not.toBe('')

    const second = await calls.sort({ call_id: 1 }).limit(5).after(q1.nextCursor)

    expect(second.map((r) => r.call_id)).toEqual(['c05', 'c06', 'c07', 'c08', 'c09'])
  })

  it('iterate() follows the cursor across pages', async () => {
    const seen: string[] = []

    for await (const row of calls.sort({ call_id: 1 }).limit(5).iterate()) {
      seen.push(row.call_id)
    }

    expect(seen).toHaveLength(12)
    expect(new Set(seen).size).toBe(12)
  })

  it('raises a typed error for a bad query', async () => {
    await expect(calls.limit(-1)).rejects.toBeInstanceOf(InvalidQueryError)
  })

  it('aggregates', async () => {
    const res = await calls.aggregate<{ _id: string; n: number }>([
      { $group: { _id: '$status', n: { $count: {} } } },
      { $sort: { _id: 1 } },
    ])

    expect(res.rows).toEqual([
      { _id: 'answered', n: 8 },
      { _id: 'missed', n: 4 },
    ])
  })
})
