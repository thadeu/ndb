import { afterAll, describe, expect, it } from 'vitest'
import { callsModel } from './fixtures.js'
import { unixDb } from './helpers.js'

const db = unixDb()

afterAll(() => db.close())

describe('watch', () => {
  it('streams changes in order and resumes after ack', async () => {
    const calls = callsModel(db)

    await calls.insert([
      { call_id: 'a', status: 's', ts: new Date() },
      { call_id: 'b', status: 's', ts: new Date() },
    ])
    await calls.delete('b')

    const ctl = new AbortController()
    const seen: string[] = []

    for await (const ev of calls.watch('w1', { signal: ctl.signal })) {
      seen.push(`${ev.op}:${ev.id}`)

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

    await calls.insert({ call_id: 'seed', status: 's', ts: new Date() })

    const it = calls.watch('w2', { from: 'now' })
    const next = it.next()

    setTimeout(() => void calls.insert({ call_id: 'live', status: 's', ts: new Date() }), 100)

    const ev = await next

    expect(ev.value?.id).toBe('live')
    expect(ev.value?.doc?.status).toBe('s')

    await it.return(undefined)
  })
})
