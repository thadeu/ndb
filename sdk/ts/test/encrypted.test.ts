import { afterAll, describe, expect, it } from 'vitest'
import { UniqueViolationError } from '../src/index.js'
import { collName, unixDb } from './helpers.js'

const db = unixDb()

afterAll(() => db.close())

describe('encrypted fields', () => {
  it('declares encrypted and blind fields and finds by the blind value', async () => {
    const people = db.collection(collName(), {
      fields: {
        cpf: { type: 'string', blind: 'digits', unique: true },
        pem: { type: 'string', encrypted: true, optional: true },
      },
    })

    await people.insert({ _id: 'p1', cpf: '390.533.447-05', pem: 'PRIVATE' })

    await expect(people.insert({ _id: 'p2', cpf: '39053344705' })).rejects.toBeInstanceOf(UniqueViolationError)

    const found = await people.query({ cpf: '39053344705' }).all()

    expect(found).toHaveLength(1)
    expect(found[0]).toMatchObject({ _id: 'p1', cpf: '390.533.447-05', pem: 'PRIVATE' })
  })
})
