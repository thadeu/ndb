import { afterAll, describe, expect, it } from 'vitest'
import { HistoryTruncatedError, makeError } from '../src/errors.js'
import { callsModel } from './fixtures.js'
import { unixDb } from './helpers.js'

const db = unixDb()

afterAll(() => db.close())

describe('watch', () => {
  it('streams changes in order and resumes after ack', async () => {
    const calls = callsModel(db)

    await calls.insert([
      { _id: 'a', status: 's', ts: new Date() },
      { _id: 'b', status: 's', ts: new Date() },
    ])
    await calls.delete({ _id: 'b' })

    const ctl = new AbortController()
    const seen: string[] = []

    for await (const ev of calls.watch('w1', { signal: ctl.signal })) {
      seen.push(`${ev.op}:${ev._id}`)

      await calls.ack(ev)

      if (seen.length === 3) {
        ctl.abort()

        break
      }
    }

    expect(seen).toEqual(['put:a', 'put:b', 'del:b'])
  })

  it('delivers live events and stops with the iterator', async () => {
    const calls = callsModel(db)

    await calls.insert({ _id: 'seed', status: 's', ts: new Date() })

    const it = calls.watch('w2', { from: 'now' })
    const next = it.next()

    setTimeout(() => void calls.insert({ _id: 'live', status: 's', ts: new Date() }), 100)

    const ev = await next

    expect(ev.value?._id).toBe('live')
    expect(ev.value?.doc?.status).toBe('s')

    await it.return(undefined)
  })

  it('delivers only the events that match where', async () => {
    const calls = callsModel(db)

    await calls.insert([
      { _id: 'a', status: 'pending', ts: new Date() },
      { _id: 'b', status: 'done', ts: new Date() },
      { _id: 'c', status: 'pending', ts: new Date() },
    ])

    const seen: string[] = []

    for await (const ev of calls.watch('w3', { where: { status: 'pending' }, limit: 2 })) {
      seen.push(ev._id)
    }

    expect(seen).toEqual(['a', 'c'])
  })

  it('fails with a typed error on a bad where', async () => {
    const calls = callsModel(db)

    await calls.insert({ _id: 'a', status: 'pending', ts: new Date() })

    const stream = calls.watch('w4', { where: { status: { $bogus: 1 } } as never })

    await expect(stream.next()).rejects.toMatchObject({ code: 'invalid_query' })
  })

  it('maps history_truncated to its own error class', () => {
    const err = makeError('history_truncated', 'gone', 410)

    expect(err).toBeInstanceOf(HistoryTruncatedError)
    expect(err.status).toBe(410)
  })
})
