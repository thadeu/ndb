import { afterAll, describe, expect, it } from 'vitest'
import { collName, unixDb } from './helpers.js'

const db = unixDb()

afterAll(() => db.close())

describe('$unwind', () => {
  it('groups by the elements of an array', async () => {
    const baskets = db.collection(collName(), { fields: { items: { type: 'json' } } })

    await baskets.insert([
      { _id: 'b1', items: [{ sku: 'a', qty: 2 }, { sku: 'b', qty: 1 }] },
      { _id: 'b2', items: [{ sku: 'a', qty: 5 }] },
      { _id: 'b3', items: [] },
    ])

    const res = await baskets.aggregate([
      { $unwind: '$items' },
      { $group: { _id: '$items.sku', qty: { $sum: '$items.qty' } } },
      { $sort: { _id: 1 } },
    ])

    expect(res.rows).toEqual([{ _id: 'a', qty: 7 }, { _id: 'b', qty: 1 }])
  })
})
