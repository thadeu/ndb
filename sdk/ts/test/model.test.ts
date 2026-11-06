import { afterAll, describe, expect, it } from 'vitest'
import { DuplicateIdError, InvalidUpdateError } from '../src/index.js'
import { callsModel } from './fixtures.js'
import { collName, unixDb } from './helpers.js'

const db = unixDb()

afterAll(() => db.close())

describe('model writes and reads', () => {
  it('ensures the collection on first use and inserts with versions', async () => {
    const calls = callsModel(db)
    const results = await calls.insert([
      { call_id: 'c1', status: 'answered', dur: 10, ts: new Date('2026-11-01T10:00:00Z') },
      { call_id: 'c2', status: 'missed', ts: new Date('2026-11-01T11:00:00Z') },
    ])

    expect(results).toEqual([
      { id: 'c1', v: expect.any(Number) },
      { id: 'c2', v: expect.any(Number) },
    ])
  })

  it('lets the daemon generate an empty id', async () => {
    const notes = db.model(collName(), { fields: { text: { type: 'string' } } })
    const [res] = await notes.create({ text: 'hi' })

    expect(res?.id).toBeTruthy()
  })

  it('finds by id with its version and a Date for date fields', async () => {
    const calls = callsModel(db)

    await calls.create({ call_id: 'c1', status: 'answered', ts: new Date('2026-11-01T10:00:00Z') })

    const row = await calls.find('c1')

    expect(row?.status).toBe('answered')
    expect(row?.ts).toEqual(new Date('2026-11-01T10:00:00Z'))
    expect(row?._v).toBeGreaterThan(0)
    expect(await calls.find('nope')).toBeNull()
  })

  it('updates by filter with set and unset, guarded by _v', async () => {
    const calls = callsModel(db)

    await calls.create({ call_id: 'c1', status: 'ringing', dur: 5, ts: new Date() })

    const row = await calls.find('c1')
    const upd = await calls.update(
      { call_id: 'c1', _v: row!._v },
      { $set: { status: 'answered' }, $unset: { dur: true } },
    )
    const after = await calls.find('c1')

    expect(upd).toMatchObject({ matched: 1, modified: 1 })
    expect(upd.doc?._v).toBeGreaterThan(row!._v)
    expect(after?.status).toBe('answered')
    expect(after?.dur).toBeUndefined()

    const stale = await calls.update({ call_id: 'c1', _v: row!._v }, { $set: { status: 'x' } })

    expect(stale).toEqual({ matched: 0, modified: 0, doc: undefined })
  })

  it('does a guarded decrement once, and many with an index', async () => {
    const calls = callsModel(db)

    await calls.create([
      { call_id: 'a', status: 'q', dur: 1, ts: new Date() },
      { call_id: 'b', status: 'q', dur: 1, ts: new Date() },
    ])

    const guard = { call_id: 'a', dur: { $gte: 1 } }
    const tries = await Promise.all([
      calls.update(guard, { $inc: { dur: -1 } }),
      calls.update(guard, { $inc: { dur: -1 } }),
    ])

    expect(tries.map((r) => r.modified).sort()).toEqual([0, 1])
    expect((await calls.find('a'))?.dur).toBe(0)

    const many = await calls.update({ status: 'q' }, { $inc: { dur: 10 } }, { many: true })

    expect(many).toEqual({ matched: 2, modified: 2, doc: undefined })
    await expect(calls.update({ dur: 10 }, { $set: { vip: true } }, { many: true })).rejects.toMatchObject({
      code: 'unindexed_many',
    })
  })

  it('upserts from the equality fields of the filter', async () => {
    const calls = callsModel(db)
    const res = await calls.update({ call_id: 'new' }, { $inc: { dur: 2 }, $set: { status: 'n' } }, { upsert: true })

    expect(res.matched).toBe(0)
    expect(res.doc).toMatchObject({ call_id: 'new', dur: 2, status: 'n', _v: 1 })
  })

  it('refuses operator keys without $ before sending', async () => {
    const calls = callsModel(db)

    await expect(calls.update({ call_id: 'c1' }, { set: { status: 'x' } } as never)).rejects.toBeInstanceOf(
      InvalidUpdateError,
    )
  })

  it('deletes by filter', async () => {
    const calls = callsModel(db)

    await calls.create({ call_id: 'c1', status: 'a', ts: new Date() })

    const row = await calls.find('c1')

    expect(await calls.delete({ call_id: 'c1', _v: row!._v + 5 })).toEqual({ deleted: 0 })
    expect(await calls.delete({ call_id: 'c1', _v: row!._v })).toEqual({ deleted: 1 })
    expect(await calls.find('c1')).toBeNull()
    expect(await calls.delete({ call_id: 'c1' })).toEqual({ deleted: 0 })
  })

  it('raises duplicate_id on a repeated id', async () => {
    const calls = callsModel(db)

    await calls.create({ call_id: 'c1', status: 'a', ts: new Date() })
    await expect(calls.create({ call_id: 'c1', status: 'b', ts: new Date() })).rejects.toBeInstanceOf(
      DuplicateIdError,
    )
  })
})
