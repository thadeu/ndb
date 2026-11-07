import { afterAll, describe, expect, it } from 'vitest'
import { collName, unixDb } from './helpers.js'

const db = unixDb()

afterAll(() => db.close())

describe('date math and $elemMatch', () => {
  const schema = {
    time: 'ts',
    fields: { ts: { type: 'date' }, items: { type: 'json' }, scores: { type: 'json' } },
  } as const

  it('filters with now-1d and $elemMatch', async () => {
    const orders = db.collection(collName(), schema)
    const now = Date.now()

    await orders.insert([
      { _id: 'o1', ts: new Date(now - 48 * 3600_000), items: [{ sku: 'a', qty: 1 }, { sku: 'b', qty: 5 }], scores: [5, 25] },
      { _id: 'o2', ts: new Date(now - 60_000), items: [{ sku: 'a', qty: 3 }], scores: [15] },
    ])

    const ids = async (where: Parameters<typeof orders.query>[0]) => (await orders.query(where).all()).map((r) => r._id)

    expect(await ids({ ts: { $gte: 'now-1d' } })).toEqual(['o2'])
    expect(await ids({ items: { $elemMatch: { sku: 'a', qty: { $gt: 2 } } } })).toEqual(['o2'])
    expect(await ids({ scores: { $elemMatch: { $gte: 10, $lt: 20 } } })).toEqual(['o2'])
  })
})
