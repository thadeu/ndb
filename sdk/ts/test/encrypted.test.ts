import { afterAll, describe, expect, it } from 'vitest'
import { UniqueViolationError } from '../src/index.js'
import { collName, unixDb } from './helpers.js'

const db = unixDb()

afterAll(() => db.close())

describe('encrypted fields', () => {
  it('declares encrypted and blind fields and finds by the blind value', async () => {
    const people = db.collection(collName(), {
      fields: {
        cpf: { type: 'string', blind: 'numeric', unique: true },
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

describe('encrypted()', () => {
  it('keeps the values sealed on a list and opens them on a show', async () => {
    const vault = db.collection(collName(), { fields: { secret: { type: 'string', encrypted: true } } })

    await vault.insert({ _id: 's1', secret: 'PRIVATE' })

    const list = await vault.encrypted().query().all()

    expect(list[0]?.secret).toMatch(/^ndb:e1:k1:/)
    expect((await vault.get('s1'))?.secret).toBe('PRIVATE')
    expect((await vault.encrypted().get('s1'))?.secret).toMatch(/^ndb:e1:k1:/)
  })
})
