import { expectTypeOf, it } from 'vitest'
import { connect } from '../src/index.js'

const db = connect('tcp://127.0.0.1:1')

const calls = db.model('calls', {
  id: 'call_id',
  time: 'ts',
  fields: {
    call_id: { type: 'string' },
    status: { type: 'string', index: true },
    dur: { type: 'number', optional: true },
    ts: { type: 'date' },
    meta: { type: 'json', optional: true },
  },
})

it('infers row types from the schema', async () => {
  const row = await calls.find('x')

  expectTypeOf(row).toEqualTypeOf<
    | (({ call_id: string; status: string; ts: Date } & { dur?: number; meta?: unknown }) & { _v: number })
    | null
  >()
})

it('rejects wrong field names and wrong value types', () => {
  // @ts-expect-error unknown field in where
  calls.where({ statuz: 'x' })

  // @ts-expect-error wrong value type
  calls.where({ dur: 'ten' })

  // @ts-expect-error unknown field in sort
  calls.sort({ nope: 1 })

  // @ts-expect-error unknown field in insert
  void calls.insert({ nope: 1 })

  // @ts-expect-error id must be a field
  db.model('bad', { id: 'missing', fields: { a: { type: 'string' } } })

  // @ts-expect-error unknown field type
  db.model('bad', { fields: { a: { type: 'text' } } })
})

it('accepts operators, dot paths on json fields and Date operands', () => {
  calls.where({ dur: { $gt: 1, $lt: 9 }, 'meta.city': 'Rio', ts: { $gte: new Date() } })
})

it('allows RegExp on string fields only', () => {
  void calls.where({ status: /^a/i })
  void calls.where({ status: { $regex: /^a/ } })
  void calls.where({ status: { $regex: '^a', $options: 'i' } })

  // @ts-expect-error a RegExp is not a number
  calls.where({ dur: /1/ })
})
