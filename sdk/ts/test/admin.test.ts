import { afterAll, describe, expect, it } from 'vitest'
import { callsModel } from './fixtures.js'
import { unixDb } from './helpers.js'

const db = unixDb()

afterAll(() => db.close())

describe('1.0 vocabulary', () => {
  it('inserts one document or many, counts, deletes by query', async () => {
    const calls = callsModel(db)
    const one = await calls.insert({ _id: 'c1', status: 'a', ts: new Date() })

    expect(one).toEqual({ _id: 'c1', _v: 1 })

    const many = await calls.insert([
      { _id: 'c2', status: 'b', ts: new Date() },
      { _id: 'c3', status: 'b', ts: new Date() },
    ])

    expect(many.map((r) => r._id)).toEqual(['c2', 'c3'])
    expect(await calls.count()).toBe(3)
    expect(await calls.count({ status: 'b' })).toBe(2)
    expect(await calls.deleteBy({ status: 'b' }, { many: true })).toEqual({ deleted: 2 })
    expect(await calls.delete('c1')).toEqual({ deleted: 1 })
  })

  it('admin of the db and of a collection', async () => {
    const calls = callsModel(db)

    await calls.insert({ _id: 'c1', status: 'a', ts: new Date() })

    await db.health()
    expect((await db.collections()).some((c) => c.name === calls.name)).toBe(true)
    expect(await db.stats()).toHaveProperty('collections')
    expect(Object.keys(await db.size()).length).toBeGreaterThan(0)
    expect(Object.keys(await calls.stats()).length).toBeGreaterThan(0)
    expect(Object.keys(await calls.compact()).length).toBeGreaterThan(0)
    expect(await calls.ensureIndex('dur')).toContain('dur')
    expect(await calls.dropIndex('dur')).not.toContain('dur')
  })

  it('stores and reads a blob', async () => {
    const blob = await db.putBlob(new TextEncoder().encode('hello'), 'text/plain')

    expect(blob.$blob).toMatch(/^sha256:/)
    expect(new TextDecoder().decode(await db.getBlob(blob))).toBe('hello')
  })
})
