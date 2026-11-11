import { afterAll, describe, expect, it } from 'vitest'
import { NotFoundError, StaleObjectError } from '../src/index.js'
import { collName, unixDb } from './helpers.js'

const db = unixDb()

afterAll(() => db.close())

function wallets() {
  return db.collection(collName(), {
    fields: {
      balance_cents: { type: 'number' },
      note: { type: 'string', optional: true },
    },
  })
}

describe('withLock', () => {
  it('reads again and calls the function again after a conflict', async () => {
    const w = wallets()

    await w.insert({ _id: 'w1', balance_cents: 1000, note: 'x' })

    let calls = 0

    const doc = await w.withLock('w1', async (wallet) => {
      calls++

      if (calls === 1) await w.update('w1', { $inc: { balance_cents: -300 } })

      wallet.balance_cents -= 500
      delete wallet.note
    })

    expect(calls).toBe(2)
    expect(doc.balance_cents).toBe(200)
    expect(doc.note).toBeUndefined()

    const stored = await w.get('w1')

    expect(stored?.balance_cents).toBe(200)
    expect(stored?.note).toBeUndefined()
  })

  it('lands every concurrent debit', async () => {
    const w = wallets()

    await w.insert({ _id: 'w1', balance_cents: 1000 })

    const debit = () => w.withLock('w1', (wallet) => {
      wallet.balance_cents -= 100
    }, { retries: 20 })

    await Promise.all([debit(), debit(), debit(), debit(), debit()])

    expect((await w.get('w1'))?.balance_cents).toBe(500)
  })

  it('stops on an error of the function and writes nothing', async () => {
    const w = wallets()

    await w.insert({ _id: 'w1', balance_cents: 100 })

    const debit = w.withLock('w1', (wallet) => {
      if (wallet.balance_cents < 500) throw new Error('insufficient funds')

      wallet.balance_cents -= 500
    })

    await expect(debit).rejects.toThrow('insufficient funds')
    expect((await w.get('w1'))?._v).toBe(1)
    await expect(w.withLock('nope', () => {})).rejects.toBeInstanceOf(NotFoundError)
  })

  it('gives up with StaleObjectError after its retries', async () => {
    const w = wallets()

    await w.insert({ _id: 'w1', balance_cents: 100 })

    let calls = 0

    const lost = w.withLock('w1', async (wallet) => {
      calls++
      await w.update('w1', { $inc: { balance_cents: 1 } })
      wallet.balance_cents = 0
    }, { retries: 3 })

    await expect(lost).rejects.toBeInstanceOf(StaleObjectError)
    expect(calls).toBe(3)
  })

  it('writes nothing when the function changes nothing', async () => {
    const w = wallets()

    await w.insert({ _id: 'w1', balance_cents: 100 })
    await w.withLock('w1', () => {})

    expect((await w.get('w1'))?._v).toBe(1)
  })
})
