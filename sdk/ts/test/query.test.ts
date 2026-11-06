import { afterAll, beforeAll, describe, expect, it } from 'vitest'
import { InvalidQueryError } from '../src/index.js'
import { callsModel } from './fixtures.js'
import { unixDb } from './helpers.js'

const db = unixDb()
const calls = callsModel(db)

beforeAll(async () => {
  const docs = Array.from({ length: 12 }, (_, i) => ({
    _id: `c${String(i).padStart(2, '0')}`,
    status: i % 3 === 0 ? 'missed' : 'answered',
    dur: i,
    ts: new Date(Date.UTC(2026, 10, 1, i)),
  }))

  await calls.insert(docs)
})

afterAll(() => db.close())

describe('query builder', () => {
  it('is thenable: await runs it', async () => {
    const rows = await calls.where({ status: 'missed' }).sort({ _id: 1 })

    expect(rows.map((r) => r._id)).toEqual(['c00', 'c03', 'c06', 'c09'])
  })

  it('combines where, not, or, limit, skip and fields', async () => {
    const rows = await calls
      .where({ dur: { $gte: 2 } })
      .not({ status: 'missed' })
      .or({ dur: { $lt: 5 } }, { dur: { $gt: 9 } })
      .sort({ dur: -1 })
      .skip(1)
      .limit(3)
      .fields('_id', 'dur')

    expect(rows.map((r) => r.dur)).toEqual([10, 4, 2])
    expect(Object.keys(rows[0]!).sort()).toEqual(['_id', '_v', 'dur'])
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
    const p3 = await calls.sort({ _id: 1 }).page(3, 4)

    expect(p3.map((r) => r._id)).toEqual(['c08', 'c09', 'c10', 'c11'].slice(0, 4))
  })

  it('follows a cursor with after(token) and nextCursor', async () => {
    const q1 = calls.sort({ _id: 1 }).limit(5)
    const first = await q1

    expect(first).toHaveLength(5)
    expect(q1.nextCursor).not.toBe('')

    const second = await calls.sort({ _id: 1 }).limit(5).after(q1.nextCursor)

    expect(second.map((r) => r._id)).toEqual(['c05', 'c06', 'c07', 'c08', 'c09'])
  })

  it('iterate() follows the cursor across pages', async () => {
    const seen: string[] = []

    for await (const row of calls.sort({ _id: 1 }).limit(5).iterate()) {
      seen.push(row._id)
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

describe('$regex', () => {
  const regexDb = unixDb()
  const people = regexDb.model(`rx${process.pid}`, {
    fields: {
      name: { type: 'string', index: true, optional: true },
      tags: { type: 'json', optional: true },
      n: { type: 'number', optional: true },
    },
  })

  beforeAll(async () => {
    await people.insert([
      { _id: 'p1', name: 'Alice', tags: ['red', 'blue'], n: 1 },
      { _id: 'p2', name: 'alina', tags: ['green'], n: 2 },
      { _id: 'p3', name: 'Bob', tags: [], n: 3 },
      { _id: 'p4', n: 4 },
    ])
  })

  afterAll(() => regexDb.close())

  const ids = (rows: { _id: string }[]) => rows.map((r) => r._id).sort()

  it('matches a string pattern and $options', async () => {
    expect(ids(await people.where({ name: { $regex: '^Al' } }))).toEqual(['p1'])
    expect(ids(await people.where({ name: { $regex: '^al', $options: 'i' } }))).toEqual(['p1', 'p2'])
  })

  it('accepts a RegExp as the value and as $regex', async () => {
    expect(ids(await people.where({ name: /^al/i }))).toEqual(['p1', 'p2'])
    expect(ids(await people.where({ name: { $regex: /^Al/ } }))).toEqual(['p1'])
    expect(ids(await people.where({ name: { $regex: /LI/i, $ne: 'alina' } }))).toEqual(['p1'])
  })

  it('works in not, or, array fields and counts', async () => {
    expect(ids(await people.not({ name: /^Al/i }))).toEqual(['p3', 'p4'])
    expect(ids(await people.or({ name: /^B/ }, { name: /na$/ }))).toEqual(['p2', 'p3'])
    expect(ids(await people.where({ tags: { $regex: '^gr' } } as never))).toEqual(['p2'])
    expect(await people.where({ name: /^al/i }).count()).toBe(2)
  })

  it('never matches non-strings', async () => {
    expect(ids(await people.where({ n: { $regex: '.' } } as never))).toEqual([])
  })

  it('rejects unsupported RegExp flags before sending', () => {
    expect(() => people.where({ name: /a/g })).toThrow(InvalidQueryError)
    expect(() => people.where({ name: { $regex: /a/u } })).toThrow(/flag "u"/)
    expect(() => people.not({ name: /a/y })).toThrow(/flag "y"/)
  })

  it('rejects RegExp flags together with $options', () => {
    expect(() => people.where({ name: { $regex: /a/i, $options: 's' } })).toThrow(InvalidQueryError)
  })

  it('maps a bad pattern or flag to InvalidQueryError', async () => {
    await expect(people.where({ name: { $regex: '(' } }).then((r) => r)).rejects.toBeInstanceOf(InvalidQueryError)
    await expect(people.where({ name: { $regex: 'a(?=b)' } }).then((r) => r)).rejects.toBeInstanceOf(InvalidQueryError)
    await expect(people.where({ name: { $regex: 'a', $options: 'g' } }).then((r) => r)).rejects.toBeInstanceOf(
      InvalidQueryError,
    )
  })
})
