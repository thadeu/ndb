import { afterAll, describe, expect, it } from 'vitest'
import { LockExpiredError, LockedError, NotFoundError } from '../src/index.js'
import { collName, unixDb } from './helpers.js'

const db = unixDb()

afterAll(() => db.close())

function wallets() {
  return db.collection(collName(), { fields: { balance_cents: { type: 'number' } } })
}

const pause = (ms: number) => new Promise((resolve) => setTimeout(resolve, ms))

describe('lock', () => {
  it('runs one holder at a time, each once', async () => {
    const w = wallets()

    await w.insert({ _id: 'w1', balance_cents: 1000 })

    let inside = 0
    let worst = 0
    let calls = 0

    const debit = () => w.lock('w1', async (wallet) => {
      worst = Math.max(worst, ++inside)
      calls++

      await pause(5)

      wallet.balance_cents -= 100
      inside--
    })

    await Promise.all([debit(), debit(), debit(), debit(), debit()])

    expect(worst).toBe(1)
    expect(calls).toBe(5)
    expect((await w.get('w1'))?.balance_cents).toBe(500)
  })

  it('writes nothing and releases the lock when the function throws', async () => {
    const w = wallets()

    await w.insert({ _id: 'w1', balance_cents: 100 })

    const failed = w.lock('w1', async (wallet) => {
      wallet.balance_cents = 0

      await expect(w.lock('w1', () => {}, { wait: 0 })).rejects.toBeInstanceOf(LockedError)

      throw new Error('insufficient funds')
    })

    await expect(failed).rejects.toThrow('insufficient funds')
    expect((await w.get('w1'))?.balance_cents).toBe(100)

    await w.lock('w1', () => {}, { wait: 0 })
    await expect(w.lock('nope', () => {})).rejects.toBeInstanceOf(NotFoundError)
  })

  it('refuses the write of a lock that expired', async () => {
    const w = wallets()

    await w.insert({ _id: 'w1', balance_cents: 100 })

    const late = w.lock('w1', async (wallet) => {
      await pause(150)

      wallet.balance_cents = 1
    }, { ttl: 0.1 })

    await expect(late).rejects.toBeInstanceOf(LockExpiredError)
    expect((await w.get('w1'))?.balance_cents).toBe(100)
  })
})
