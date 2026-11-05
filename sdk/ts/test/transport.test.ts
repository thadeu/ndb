import { afterAll, describe, expect, it } from 'vitest'
import { NdbError, NotFoundError, VersionConflictError, connect } from '../src/index.js'
import { collName, tcpDb, unixDb } from './helpers.js'

const dbs = [unixDb(), tcpDb()]

afterAll(() => Promise.all(dbs.map((db) => db.close())))

describe.each([
  ['unix', dbs[0]!],
  ['tcp', dbs[1]!],
])('transport over %s', (_name, db) => {
  it('streams NDJSON rows as an async iterator', async () => {
    const name = collName()

    await db.request('PUT', `/v1/c/${name}`, { id: 'k' })
    await db.request('POST', `/v1/c/${name}/insert`, { insert: [{ k: 'a' }, { k: 'b' }] })

    const res = await db.stream('POST', `/v1/c/${name}/find`, { sort: { k: 1 } })
    const rows: unknown[] = []

    for await (const row of res.rows()) {
      rows.push(row)
    }

    expect(rows).toEqual([{ k: 'a' }, { k: 'b' }])
  })

  it('raises a typed error with code and status', async () => {
    const err = await db.request('POST', `/v1/c/nope${collName()}/find`, {}).catch((e: unknown) => e)

    expect(err).toBeInstanceOf(NdbError)
    expect(err).toBeInstanceOf(NotFoundError)
    expect((err as NdbError).status).toBeGreaterThanOrEqual(400)
    expect((err as NdbError).code).toBeTruthy()
  })

  it('maps codes to subclasses', async () => {
    const name = collName()

    await db.request('PUT', `/v1/c/${name}`, { id: 'k' })
    await db.request('POST', `/v1/c/${name}/insert`, { insert: [{ k: 'a' }] })

    const err = await db
      .request('POST', `/v1/c/${name}/update`, { update: { id: 'a', set: { x: 1 }, if_version: 99 } })
      .catch((e: unknown) => e)

    expect(err).toBeInstanceOf(VersionConflictError)
    expect((err as VersionConflictError).code).toBe('version_conflict')
  })
})

it('rejects a bad address', () => {
  expect(() => connect('ftp://x')).toThrow(/unix:\/\//)
})
