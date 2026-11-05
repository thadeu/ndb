import { afterAll, describe, expect, it } from 'vitest'
import { DuplicateIdError, NotFoundError, VersionConflictError } from '../src/index.js'
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

  it('updates with set and unset, guarded by ifVersion', async () => {
    const calls = callsModel(db)

    await calls.create({ call_id: 'c1', status: 'ringing', dur: 5, ts: new Date() })

    const row = await calls.find('c1')
    const upd = await calls.update('c1', { status: 'answered' }, { ifVersion: row!._v, unset: ['dur'] })
    const after = await calls.find('c1')

    expect(upd.v).toBeGreaterThan(row!._v)
    expect(after?.status).toBe('answered')
    expect(after?.dur).toBeUndefined()

    await expect(calls.update('c1', { status: 'x' }, { ifVersion: row!._v })).rejects.toBeInstanceOf(
      VersionConflictError,
    )
  })

  it('deletes, guarded by ifVersion', async () => {
    const calls = callsModel(db)

    await calls.create({ call_id: 'c1', status: 'a', ts: new Date() })

    const row = await calls.find('c1')

    await expect(calls.delete('c1', { ifVersion: row!._v + 5 })).rejects.toBeInstanceOf(VersionConflictError)
    await calls.delete('c1', { ifVersion: row!._v })

    expect(await calls.find('c1')).toBeNull()
    await expect(calls.delete('c1')).rejects.toBeInstanceOf(NotFoundError)
  })

  it('raises duplicate_id on a repeated id', async () => {
    const calls = callsModel(db)

    await calls.create({ call_id: 'c1', status: 'a', ts: new Date() })
    await expect(calls.create({ call_id: 'c1', status: 'b', ts: new Date() })).rejects.toBeInstanceOf(
      DuplicateIdError,
    )
  })
})
