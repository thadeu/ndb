import { afterAll, describe, expect, it } from 'vitest'
import { collName, unixDb } from './helpers.js'

const db = unixDb()

afterAll(() => db.close())

describe('collection patterns', () => {
  const schema = { fields: { at: { type: 'number' }, lvl: { type: 'string' } } } as const

  it('reads every collection of the pattern, with _coll on each row', async () => {
    const base = `${collName()}-logs-`

    await db.collection(`${base}1`, schema).insert([
      { _id: 'a1', at: 1, lvl: 'info' },
      { _id: 'a2', at: 3, lvl: 'error' },
    ])
    await db.collection(`${base}2`, schema).insert({ _id: 'b1', at: 2, lvl: 'error' })

    const logs = db.collection(`${base}*`, schema)
    const rows = await logs.query({}).sort({ at: -1 }).all()

    expect(rows.map((r) => r._id)).toEqual(['a2', 'b1', 'a1'])
    expect(rows.map((r) => r._coll)).toEqual([`${base}1`, `${base}2`, `${base}1`])
    expect(await logs.query({ lvl: 'error' }).count()).toBe(2)

    await expect(logs.insert({ at: 9, lvl: 'x' })).rejects.toMatchObject({ code: 'pattern_read_only' })
  })
})
