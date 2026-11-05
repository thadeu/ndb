import { afterEach, describe, expect, inject, it, vi } from 'vitest'
import { UnauthorizedError, NdbError, connect, type Ndb } from '../src/index.js'

const open: Ndb[] = []

function track(db: Ndb): Ndb {
  open.push(db)

  return db
}

afterEach(async () => {
  vi.unstubAllEnvs()
  await Promise.all(open.splice(0).map((db) => db.close()))
})

describe('token over tcp', () => {
  it('accepts the right token', async () => {
    const db = track(connect(inject('tokenTcpAddr'), { token: inject('token') }))

    await expect(db.request('GET', '/v1/c')).resolves.toBeDefined()
  })

  it.each([
    ['wrong', { token: 'nope' }],
    ['missing', {}],
  ])('rejects a %s token as unauthorized', async (_name, opts) => {
    vi.stubEnv('NDB_TOKEN', '')

    const db = track(connect(inject('tokenTcpAddr'), opts))
    const err = await db.request('GET', '/v1/c').catch((e: unknown) => e)

    expect(err).toBeInstanceOf(UnauthorizedError)
    expect(err).toBeInstanceOf(NdbError)
    expect((err as NdbError).code).toBe('unauthorized')
    expect((err as NdbError).status).toBe(401)
  })

  it('falls back to NDB_TOKEN and lets the option win', async () => {
    vi.stubEnv('NDB_TOKEN', inject('token'))

    const fromEnv = track(connect(inject('tokenTcpAddr')))

    await expect(fromEnv.request('GET', '/v1/c')).resolves.toBeDefined()

    const explicit = track(connect(inject('tokenTcpAddr'), { token: 'nope' }))

    await expect(explicit.request('GET', '/v1/c')).rejects.toBeInstanceOf(UnauthorizedError)
  })
})

describe('NDB_URL', () => {
  it('is used when no address is given', async () => {
    vi.stubEnv('NDB_URL', inject('tcpAddr'))
    await expect(track(connect()).health()).resolves.toBeUndefined()

    vi.stubEnv('NDB_URL', inject('unixAddr'))
    await expect(track(connect()).health()).resolves.toBeUndefined()
  })

  it('loses to an explicit address', async () => {
    vi.stubEnv('NDB_URL', 'tcp://127.0.0.1:1')

    await expect(track(connect(inject('unixAddr'))).health()).resolves.toBeUndefined()
  })

  it('is an error when neither is set', () => {
    vi.stubEnv('NDB_URL', '')

    expect(() => connect()).toThrow(TypeError)
  })
})
