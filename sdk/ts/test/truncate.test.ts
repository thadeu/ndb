import { afterAll, describe, expect, it } from 'vitest'
import { CollectionExistsError, NotFoundError, PitrError } from '../src/errors.js'
import { callsModel } from './fixtures.js'
import { unixDb } from './helpers.js'

const db = unixDb()

afterAll(() => db.close())

describe('truncate and drop', () => {
  it('truncate removes every document and watch gets one truncate event', async () => {
    const calls = callsModel(db)

    await calls.insert([
      { _id: 'a', status: 's', ts: new Date() },
      { _id: 'b', status: 's', ts: new Date() },
    ])

    await calls.truncate()

    expect(await calls.get('a')).toBeNull()

    await calls.insert({ _id: 'c', status: 's', ts: new Date() })

    const seen: string[] = []

    for await (const ev of calls.watch('w1', { limit: 2 })) {
      seen.push(`${ev.op}:${ev._id}`)
    }

    expect(seen).toEqual(['truncate:', 'put:c'])
  })

  it('drop removes the collection and the next call declares it again', async () => {
    const calls = callsModel(db)

    await calls.insert({ _id: 'a', status: 's', ts: new Date() })
    await calls.drop()

    await expect(db.transport.request('DELETE', calls.path() + `?confirm=${calls.name}`)).rejects.toBeInstanceOf(NotFoundError)

    expect(await calls.get('a')).toBeNull()
  })

  it('rename moves the documents, and replace puts them in place of another', async () => {
    const calls = callsModel(db)
    const other = callsModel(db)

    await calls.insert({ _id: 'a', status: 's', ts: new Date() })
    await other.insert({ _id: 'old', status: 's', ts: new Date() })

    await expect(calls.rename(other.name)).rejects.toBeInstanceOf(CollectionExistsError)

    await calls.rename(other.name, { replace: true })

    expect(await other.get('a')).not.toBeNull()
    expect(await other.get('old')).toBeNull()
  })

  it('restore makes a copy as of an instant, and pitr reports the window', async () => {
    const calls = callsModel(db)
    const into = callsModel(db)

    await calls.insert({ _id: 'a', status: 's', ts: new Date() })
    await new Promise((r) => setTimeout(r, 5))

    const until = new Date()

    await new Promise((r) => setTimeout(r, 5))
    await calls.delete('a')
    await calls.restore({ until, into: into.name })

    expect(await into.get('a')).not.toBeNull()

    await expect(calls.restore({ until: new Date(Date.now() - 3 * 86_400_000), into: `${into.name}x` })).rejects.toBeInstanceOf(PitrError)

    const info = await calls.pitr()

    expect(info.window).toBe('1d')
  })
})
