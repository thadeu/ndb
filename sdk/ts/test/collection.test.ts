import { afterAll, describe, expect, it } from 'vitest'
import { DuplicateIdError, InvalidUpdateError, newId } from '../src/index.js'
import { callsModel } from './fixtures.js'
import { collName, unixDb } from './helpers.js'

const db = unixDb()

afterAll(() => db.close())

describe('collection writes and reads', () => {
  it('ensures the collection on first use and inserts with versions', async () => {
    const calls = callsModel(db)
    const results = await calls.insert([
      { _id: 'c1', status: 'answered', dur: 10, ts: new Date('2026-11-01T10:00:00Z') },
      { _id: 'c2', status: 'missed', ts: new Date('2026-11-01T11:00:00Z') },
    ])

    expect(results).toEqual([
      { _id: 'c1', _v: expect.any(Number) },
      { _id: 'c2', _v: expect.any(Number) },
    ])
  })

  it('lets the daemon generate an empty id', async () => {
    const notes = db.collection(collName(), { fields: { text: { type: 'string' } } })
    const res = await notes.insert({ text: 'hi' })

    expect(res?._id).toBeTruthy()
  })

  it('finds by id with its version and a Date for date fields', async () => {
    const calls = callsModel(db)

    await calls.insert({ _id: 'c1', status: 'answered', ts: new Date('2026-11-01T10:00:00Z') })

    const row = await calls.get('c1')

    expect(row?.status).toBe('answered')
    expect(row?.ts).toEqual(new Date('2026-11-01T10:00:00Z'))
    expect(row?._v).toBeGreaterThan(0)
    expect(await calls.get('nope')).toBeNull()
  })

  it('updates by filter with set and unset, guarded by _v', async () => {
    const calls = callsModel(db)

    await calls.insert({ _id: 'c1', status: 'ringing', dur: 5, ts: new Date() })

    const row = await calls.get('c1')
    const upd = await calls.update('c1', { $set: { status: 'answered' }, $unset: { dur: true } }, { guard: { _v: row!._v } })
    const after = await calls.get('c1')

    expect(upd).toMatchObject({ matched: 1, modified: 1 })
    expect(upd.doc?._v).toBeGreaterThan(row!._v)
    expect(after?.status).toBe('answered')
    expect(after?.dur).toBeUndefined()

    const stale = await calls.update('c1', { $set: { status: 'x' } }, { guard: { _v: row!._v } })

    expect(stale).toEqual({ matched: 0, modified: 0, doc: undefined })
  })

  it('does a guarded decrement once, and many with an index', async () => {
    const calls = callsModel(db)

    await calls.insert([
      { _id: 'a', status: 'q', dur: 1, ts: new Date() },
      { _id: 'b', status: 'q', dur: 1, ts: new Date() },
    ])

    const guard = { _id: 'a', dur: { $gte: 1 } }
    const tries = await Promise.all([
      calls.updateBy(guard, { $inc: { dur: -1 } }),
      calls.updateBy(guard, { $inc: { dur: -1 } }),
    ])

    expect(tries.map((r) => r.modified).sort()).toEqual([0, 1])
    expect((await calls.get('a'))?.dur).toBe(0)

    const many = await calls.updateBy({ status: 'q' }, { $inc: { dur: 10 } }, { many: true })

    expect(many).toEqual({ matched: 2, modified: 2, doc: undefined })
    await expect(calls.updateBy({ dur: 10 }, { $set: { vip: true } }, { many: true })).rejects.toMatchObject({
      code: 'unindexed_many',
    })
  })

  it('upserts from the equality fields of the filter', async () => {
    const calls = callsModel(db)
    const res = await calls.update('new', { $inc: { dur: 2 }, $set: { status: 'n' } }, { upsert: true })

    expect(res.matched).toBe(0)
    expect(res.doc).toMatchObject({ _id: 'new', dur: 2, status: 'n', _v: 1 })
  })

  it('refuses operator keys without $ before sending', async () => {
    const calls = callsModel(db)

    await expect(calls.update('c1', { set: { status: 'x' } } as never)).rejects.toBeInstanceOf(
      InvalidUpdateError,
    )
  })

  it('deletes by filter', async () => {
    const calls = callsModel(db)

    await calls.insert({ _id: 'c1', status: 'a', ts: new Date() })

    const row = await calls.get('c1')

    expect(await calls.deleteBy({ _id: 'c1', _v: row!._v + 5 })).toEqual({ deleted: 0 })
    expect(await calls.deleteBy({ _id: 'c1', _v: row!._v })).toEqual({ deleted: 1 })
    expect(await calls.get('c1')).toBeNull()
    expect(await calls.deleteBy({ _id: 'c1' })).toEqual({ deleted: 0 })
  })

  it('raises duplicate_id on a repeated id', async () => {
    const calls = callsModel(db)

    await calls.insert({ _id: 'c1', status: 'a', ts: new Date() })
    await expect(calls.insert({ _id: 'c1', status: 'b', ts: new Date() })).rejects.toBeInstanceOf(
      DuplicateIdError,
    )
  })
})

describe('by _id', () => {
  it('get, update and delete use the /{_id} routes', async () => {
    const calls = callsModel(db)

    await calls.insert({ _id: 'a', status: 'q', dur: 3, ts: new Date() })

    const res = await calls.update('a', { $inc: { dur: -1 } })

    expect(res).toMatchObject({ matched: 1, modified: 1, doc: { _id: 'a', dur: 2, _v: 2 } })

    const guarded = await calls.update('a', { $set: { status: 'x' } }, { guard: { dur: { $gte: 1 } } })

    expect(guarded.matched).toBe(1)

    const upserted = await calls.update('z', { $set: { status: 'n' } }, { upsert: true })

    expect(upserted.doc).toMatchObject({ _id: 'z', status: 'n' })
    expect(await calls.delete('a')).toEqual({ deleted: 1 })
    expect(await calls.delete('a')).toEqual({ deleted: 0 })
    expect(await calls.get('a')).toBeNull()
  })
})

describe('newId', () => {
  it('makes UUIDv7 ids in creation order', () => {
    let prev = newId()

    expect(prev).toMatch(/^[0-9a-f]{8}-[0-9a-f]{4}-7[0-9a-f]{3}-[89ab][0-9a-f]{3}-[0-9a-f]{12}$/)

    for (let i = 0; i < 10000; i++) {
      const next = newId()

      expect(next > prev).toBe(true)

      prev = next
    }
  })
})
