import { afterAll, describe, expect, it } from 'vitest'
import { DuplicateIdError } from '../src/errors.js'
import { callsModel } from './fixtures.js'
import { unixDb } from './helpers.js'

const db = unixDb()

afterAll(() => db.close())

describe('upsert', () => {
  it('creates or merges by _id, one or many in one transaction', async () => {
    const calls = callsModel(db)
    const ts = new Date()

    await calls.insert({ _id: 'c1', status: 'a', dur: 5, ts })

    expect(await calls.upsert({ _id: 'c1', status: 'b' })).toEqual({ _id: 'c1', _v: 2 })

    const row = await calls.get('c1')

    expect(row?.status).toBe('b')
    expect(row?.dur).toBe(5)

    const all = await calls.upsertAll([
      { _id: 'c1', status: 'c' },
      { _id: 'c2', status: 'n', ts },
    ])

    expect(all).toEqual([
      { _id: 'c1', _v: 3 },
      { _id: 'c2', _v: 1 },
    ])

    await expect(calls.insert({ _id: 'c2', status: 'x', ts })).rejects.toBeInstanceOf(DuplicateIdError)
  })
})
