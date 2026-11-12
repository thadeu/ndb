import { afterAll, describe, expect, it } from 'vitest'
import { inject } from 'vitest'
import { clean, cleanAll, connect } from '../src/index.js'
import { callsModel } from './fixtures.js'
import { collName, unixDb } from './helpers.js'

const db = unixDb()

afterAll(() => db.close())

describe('clean', () => {
  it('truncates the collections the db used, less except, once', async () => {
    const a = callsModel(db)
    const b = callsModel(db)

    await a.insert({ _id: 'x', status: 's', ts: new Date() })
    await b.insert({ _id: 'y', status: 's', ts: new Date() })

    const cleaned = await clean(db, { except: [b.name] })

    expect(cleaned).toContain(a.name)
    expect(cleaned).not.toContain(b.name)
    expect(await a.get('x')).toBeNull()
    expect(await b.get('y')).not.toBeNull()
    expect(await clean(db)).toEqual([])
  })

  it('leaves out a collection that does not exist', async () => {
    expect(await clean(db, { only: [`never_${collName()}`] })).toHaveLength(1)
  })

  it('reads the prefix from NDB_PREFIX; the option wins', async () => {
    process.env.NDB_PREFIX = 'env_'

    try {
      const envDb = connect(inject('unixAddr'))
      const optDb = connect(inject('unixAddr'), { prefix: 'opt_' })

      expect(envDb.collection('calls', { fields: {} }).name).toBe('env_calls')
      expect(optDb.collection('calls', { fields: {} }).name).toBe('opt_calls')

      await envDb.close()
      await optDb.close()
    } finally {
      delete process.env.NDB_PREFIX
    }
  })

  it('prefix and cleanAll', async () => {
    const prefix = `p${collName()}_`
    const pdb = connect(inject('unixAddr'), { prefix })
    const calls = pdb.collection('calls', { fields: { status: { type: 'string' } } })

    expect(calls.name).toBe(`${prefix}calls`)

    await calls.insert({ _id: 'z', status: 's' })

    expect(await cleanAll(pdb)).toEqual([`${prefix}calls`])
    expect(await calls.get('z')).toBeNull()

    await pdb.close()
  })
})
