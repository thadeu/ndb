import { expectTypeOf, it } from 'vitest'
import { connect } from '../src/index.js'

const db = connect('tcp://127.0.0.1:1')

const calls = db.collection('calls', {
  time: 'ts',
  fields: {
    status: { type: 'string', index: true },
    dur: { type: 'number', optional: true },
    ts: { type: 'date' },
    meta: { type: 'json', optional: true },
  },
})

it('infers row types from the schema', async () => {
  const row = await calls.get('x')

  expectTypeOf(row).toEqualTypeOf<
    | (({ status: string; ts: Date } & { dur?: number; meta?: unknown }) & { _id: string; _v: number; _coll?: string })
    | null
  >()
})

it('rejects wrong field names and wrong value types', () => {
  // @ts-expect-error unknown field in where
  calls.query({ statuz: 'x' })

  // @ts-expect-error wrong value type
  calls.query({ dur: 'ten' })

  // @ts-expect-error unknown field in sort
  calls.sort({ nope: 1 })

  // @ts-expect-error unknown field in insert
  void calls.insert({ nope: 1 })

  // @ts-expect-error the id option is gone, the key is _id
  db.collection('bad', { id: 'a', fields: { a: { type: 'string' } } })

  // @ts-expect-error unknown field type
  db.collection('bad', { fields: { a: { type: 'text' } } })
})

it('accepts operators, dot paths on json fields and Date operands', () => {
  calls.query({ dur: { $gt: 1, $lt: 9 }, 'meta.city': 'Rio', ts: { $gte: new Date() } })
})

it('allows RegExp on string fields only', () => {
  void calls.query({ status: /^a/i })
  void calls.query({ status: { $regex: /^a/ } })
  void calls.query({ status: { $regex: '^a', $options: 'i' } })

  // @ts-expect-error a RegExp is not a number
  calls.query({ dur: /1/ })
})
