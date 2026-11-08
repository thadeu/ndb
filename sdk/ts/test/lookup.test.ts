import { afterAll, describe, expect, it } from 'vitest'
import { LookupError } from '../src/errors.js'
import { belongsTo, hasMany } from '../src/lookup.js'
import { collName, unixDb } from './helpers.js'

const db = unixDb()

afterAll(() => db.close())

describe('lookup', () => {
  it('attaches belongs_to and has_many relations to the rows', async () => {
    const customers = db.collection(collName(), { fields: { name: { type: 'string' } } })
    const items = db.collection(collName(), { fields: { order_id: { type: 'string', index: true }, pos: { type: 'number' } } })
    const orders = db.collection(collName(), { fields: { customer_id: { type: 'string' }, total: { type: 'number' } } })

    await customers.insert({ _id: 'c1', name: 'Ana' })
    await items.insert([
      { _id: 'i1', order_id: 'o1', pos: 2 },
      { _id: 'i2', order_id: 'o1', pos: 1 },
    ])
    await orders.insert({ _id: 'o1', customer_id: 'c1', total: 120 })

    const rows = await orders
      .query({})
      .lookup<'customer', { name: string } | null>('customer', belongsTo(customers.name, 'customer_id'))
      .lookup<'items', { _id: string }[]>('items', hasMany(items.name, 'order_id', { sort: { pos: 1 }, limit: 10 }))
      .all()

    expect(rows[0]?.customer?.name).toBe('Ana')
    expect(rows[0]?.items.map(i => i._id)).toEqual(['i2', 'i1'])

    await expect(orders.query({}).lookup('x', hasMany(items.name, 'pos')).all()).rejects.toBeInstanceOf(LookupError)
  })
})
