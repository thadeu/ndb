import { afterAll, describe, expect, it } from 'vitest'
import { NotFoundError, TtlError } from '../src/index.js'
import { collName, unixDb } from './helpers.js'

const db = unixDb()

afterAll(() => db.close())

function sessions() {
  return db.model(collName(), {
    id: 'sid',
    expires: 'exp',
    fields: { sid: { type: 'string' }, user: { type: 'string', optional: true }, exp: { type: 'date', optional: true } },
  })
}

describe('expiry like Redis', () => {
  it('insert and update take expireIn', async () => {
    const s = sessions()

    await s.insert([{ sid: 'a' }, { sid: 'b' }], { expireIn: '1h' })
    await s.create({ sid: 'c' }, { expireIn: 90 })

    expect(await s.ttl('a')).toBeGreaterThan(3590)
    expect(await s.ttl('b')).toBeLessThanOrEqual(3600)
    expect(await s.ttl('c')).toBeLessThanOrEqual(90)

    await s.update({ sid: 'a' }, { $set: { user: 'u' } }, { expireIn: '30m' })

    expect(await s.ttl('a')).toBeLessThanOrEqual(1800)

    const row = await s.find('a')

    expect(row?.exp).toBeInstanceOf(Date)
    expect(row?.user).toBe('u')
  })

  it('expire, expireAt, persist and ttl', async () => {
    const s = sessions()

    await s.insert({ sid: 's1' })

    expect(await s.ttl('s1')).toBe(-1)

    const res = await s.expire('s1', '30m')

    expect(res.v).toBe(2)
    expect(res.expiresAt.getTime()).toBeGreaterThan(Date.now())
    expect(await s.ttl('s1')).toBeLessThanOrEqual(1800)

    expect((await s.expireAt('s1', new Date(Date.now() + 7_200_000))).v).toBe(3)
    expect(await s.ttl('s1')).toBeGreaterThan(7190)

    expect((await s.persist('s1')).v).toBe(4)
    expect(await s.ttl('s1')).toBe(-1)
  })

  it('a missing or expired document is null or NotFoundError', async () => {
    const s = sessions()

    await s.insert({ sid: 's1' })
    await s.expireAt('s1', new Date(Date.now() - 60_000))

    expect(await s.ttl('s1')).toBeNull()
    expect(await s.ttl('nope')).toBeNull()
    await expect(s.expire('s1', 60)).rejects.toBeInstanceOf(NotFoundError)
    await expect(s.persist('nope')).rejects.toBeInstanceOf(NotFoundError)
  })

  it('a schema without expires fails with TtlError', async () => {
    const plain = db.model(collName(), { fields: { n: { type: 'number' } } })

    await expect(plain.insert({ n: 1 }, { expireIn: 60 })).rejects.toBeInstanceOf(TtlError)
    await expect(plain.expire('x', 60)).rejects.toMatchObject({ code: 'ttl_not_enabled' })
  })
})
