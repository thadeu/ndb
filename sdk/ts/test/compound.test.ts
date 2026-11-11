import { afterAll, describe, expect, it } from 'vitest'
import { UniqueViolationError } from '../src/index.js'
import { collName, unixDb } from './helpers.js'

const db = unixDb()

afterAll(() => db.close())

describe('compound indexes', () => {
  it('declares a compound index and a compound unique key', async () => {
    const members = db.collection(collName(), {
      indexes: [['acc', 'n']],
      unique: [['acc', 'email']],
      fields: {
        acc: { type: 'string' },
        email: { type: 'string' },
        n: { type: 'number', optional: true },
      },
    })

    await members.insert({ _id: 'm1', acc: 'a', email: 'x@x', n: 1 })
    await members.insert({ _id: 'm2', acc: 'b', email: 'x@x', n: 2 })

    await expect(members.insert({ _id: 'm3', acc: 'a', email: 'x@x' })).rejects.toBeInstanceOf(UniqueViolationError)

    const indexes = await members.ensureIndex('email,acc')

    expect(indexes).toContain('acc,n')
    expect(indexes).toContain('email,acc')
  })
})
