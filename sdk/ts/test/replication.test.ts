import { afterAll, describe, expect, inject, it } from 'vitest'
import { connect } from '../src/index.js'
import { collName } from './helpers.js'

// A copy forwards a write to its owner and answers after it has applied
// it, so the next read on the copy sees the write.
const copy = connect(inject('copyUnixAddr'))
const owner = connect(inject('ownerTcpAddr'))

afterAll(async () => {
  await Promise.all([copy.close(), owner.close()])
})

describe('a copy', () => {
  it('reads a write just after it went through the copy', async () => {
    const name = collName()
    const notes = copy.collection(name, { fields: { n: { type: 'number' } } })

    for (let i = 0; i < 10; i++) {
      await notes.insert({ _id: `d${i}`, n: i })

      const row = await notes.get(`d${i}`)

      expect(row?.n).toBe(i)
    }

    const onOwner = owner.collection(name, { fields: { n: { type: 'number' } } })

    expect(await onOwner.count()).toBe(10)
  })
})
