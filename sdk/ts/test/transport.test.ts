import { afterAll, describe, expect, inject, it } from 'vitest'
import { InvalidUpdateError, NdbError, NotFoundError, connect } from '../src/index.js'
import { collName, tcpDb, unixDb } from './helpers.js'

const dbs = [unixDb(), tcpDb()]

afterAll(() => Promise.all(dbs.map((db) => db.close())))

describe.each([
  ['unix', dbs[0]!],
  ['tcp', dbs[1]!],
])('transport over %s', (_name, db) => {
  it('streams NDJSON rows as an async iterator', async () => {
    const name = collName()

    await db.request('PUT', `/${name}`, {})
    await db.request('POST', `/${name}/_bulk`, { docs: [{ _id: 'a', k: 'a' }, { _id: 'b', k: 'b' }] })

    const res = await db.stream('POST', `/${name}/_search`, { sort: { k: 1 } })
    const rows: unknown[] = []

    for await (const row of res.rows()) {
      rows.push(row)
    }

    expect(rows).toEqual([{ _id: 'a', k: 'a' }, { _id: 'b', k: 'b' }])
  })

  it('raises a typed error with code and status', async () => {
    const err = await db.request('POST', `/nope${collName()}/_search`, {}).catch((e: unknown) => e)

    expect(err).toBeInstanceOf(NdbError)
    expect(err).toBeInstanceOf(NotFoundError)
    expect((err as NdbError).status).toBeGreaterThanOrEqual(400)
    expect((err as NdbError).code).toBeTruthy()
  })

  it('maps codes to subclasses', async () => {
    const name = collName()

    await db.request('PUT', `/${name}`, {})
    await db.request('POST', `/${name}/_bulk`, { docs: [{ k: 'a' }] })

    const err = await db
      .request('POST', `/${name}/_update_by_query`, { query: { k: 'a' }, update: { $rename: { x: 'y' } } })
      .catch((e: unknown) => e)

    expect(err).toBeInstanceOf(InvalidUpdateError)
    expect((err as InvalidUpdateError).code).toBe('unknown_operator')
  })
})

it('rejects a bad address', () => {
  expect(() => connect('ftp://x')).toThrow(/unix:\/\//)
})

describe('addresses', () => {
  it('talks over http://host:port (ndb behind a proxy)', async () => {
    const db = connect(inject('tcpAddr').replace('tcp://', 'http://'))

    try {
      await expect(db.health()).resolves.toBeUndefined()
    } finally {
      await db.close()
    }
  })

  it('refuses other schemes', () => {
    for (const bad of ['ftp://h', 'https://', 'h:1']) {
      expect(() => connect(bad)).toThrow(TypeError)
    }
  })
})
