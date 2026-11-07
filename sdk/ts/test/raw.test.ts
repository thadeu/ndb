import { afterAll, describe, expect, it } from 'vitest'
import { InvalidQueryError } from '../src/errors.js'
import { callsModel } from './fixtures.js'
import { unixDb } from './helpers.js'

const db = unixDb()

afterAll(() => db.close())

describe('queries built elsewhere', () => {
  it('rawQuery takes an untyped where and mixes with where()', async () => {
    const calls = callsModel(db)
    const ts = new Date()

    await calls.insert([
      { _id: 'c1', status: 'answered', dur: 10, ts },
      { _id: 'c2', status: 'failed', dur: 40, ts },
      { _id: 'c3', status: 'answered', dur: 50, ts },
    ])

    const form: Record<string, unknown> = JSON.parse('{"status":{"$in":["answered","failed"]},"dur":{"$gte":30}}')

    expect((await calls.rawQuery(form).sort({ dur: 1 }).all()).map((r) => r._id)).toEqual(['c2', 'c3'])
    expect((await calls.rawQuery({ status: 'answered' }).query({ dur: { $gte: 20 } }).all()).map((r) => r._id)).toEqual(['c3'])
  })

  it('find takes a whole body and pages with next', async () => {
    const calls = callsModel(db)
    const ts = new Date()

    await calls.insert([
      { _id: 'c1', status: 'answered', dur: 10, ts },
      { _id: 'c2', status: 'answered', dur: 40, ts },
      { _id: 'c3', status: 'answered', dur: 50, ts },
    ])

    const body = JSON.parse('{"query":{"status":"answered"},"sort":{"dur":-1},"limit":2,"count":true}')
    const page = await calls.search(body)

    expect(page.rows.map((r) => r._id)).toEqual(['c3', 'c2'])
    expect(page.rows[0]?.ts).toBeInstanceOf(Date)
    expect(page.total).toBe(3)

    const next = await calls.search({ ...body, count: undefined, after: page.next })

    expect(next.rows.map((r) => r._id)).toEqual(['c1'])
    expect(next.next).toBe('')

    await expect(calls.search({ query: { status: { $bad: 1 } } })).rejects.toBeInstanceOf(InvalidQueryError)
  })
})
