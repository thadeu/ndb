import { afterAll, describe, expect, it } from 'vitest'
import { NotFoundError, Rollback, TxConflictError } from '../src/index.js'
import { collName, unixDb } from './helpers.js'

const db = unixDb()

afterAll(() => db.close())

function accounts() {
  return db.collection(collName(), {
    fields: {
      balance: { type: 'number' },
      note: { type: 'string', optional: true },
    },
  })
}

type Accounts = ReturnType<typeof accounts>

function transfer(col: Accounts, from: string, to: string, amount: number) {
  return col.transaction([from, to], (tx, docs) => {
    const a = docs[from]!
    const b = docs[to]!

    if (a.balance < amount) throw new Rollback()

    a.balance -= amount
    b.balance += amount

    tx.update(a)
    tx.update(b)
  })
}

describe('transaction', () => {
  it('moves balances with locks, also from crossed callers', async () => {
    const col = accounts()

    await col.insert([{ _id: 'a1', balance: 100 }, { _id: 'a2', balance: 0 }])
    await Promise.all(Array.from({ length: 10 }, (_, i) => (i % 2 ? transfer(col, 'a2', 'a1', 1) : transfer(col, 'a1', 'a2', 1))))

    const a1 = await col.get('a1')
    const a2 = await col.get('a2')

    expect(a1!.balance + a2!.balance).toBe(100)

    await expect(transfer(col, 'a2', 'a1', 1000)).resolves.toBeUndefined()
    await col.lock('a1', () => {}, { wait: 0 })
  })

  it('writes nothing on an error and releases the locks', async () => {
    const col = accounts()

    await col.insert([{ _id: 'a1', balance: 100 }, { _id: 'a2', balance: 0 }])

    const failed = col.transaction(['a1'], (tx, docs) => {
      docs.a1!.balance = 0
      tx.update(docs.a1!)

      throw new Error('gateway down')
    })

    await expect(failed).rejects.toThrow('gateway down')

    const refused = col.transaction(['a1'], (tx, docs) => {
      docs.a1!.balance = 1
      tx.update(docs.a1!)
      tx.delete('nope')
    })

    await expect(refused).rejects.toBeInstanceOf(TxConflictError)
    expect((await col.get('a1'))?.balance).toBe(100)

    await expect(col.transaction(['a1', 'nope'], () => {})).rejects.toBeInstanceOf(NotFoundError)
    await col.lock('a1', () => {}, { wait: 0 })
  })

  it('is an atomic batch without locks, with the version guard', async () => {
    const col = accounts()

    await col.insert([{ _id: 'a1', balance: 5, note: 'x' }, { _id: 'a9', balance: 0 }])

    const stale = (await col.get('a1'))!

    await col.transaction([], (tx) => {
      tx.insert({ _id: 'a3', balance: 7 })
      tx.delete('a9')
      tx.update({ ...stale, balance: 6 })
    })

    expect((await col.get('a1'))?.balance).toBe(6)
    expect((await col.get('a3'))?.balance).toBe(7)
    expect(await col.get('a9')).toBeNull()

    await expect(col.transaction([], (tx) => { tx.update({ ...stale, balance: 0 }) })).rejects.toBeInstanceOf(TxConflictError)
    await col.transaction([], () => {})
  })
})
