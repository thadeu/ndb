// Run after `pnpm build`, with a daemon: ndb -unix /tmp/ndb.sock -data-dir /tmp/ndb-data
import { connect } from 'ndb'

const ndb = connect(process.env.NDB_ADDR ?? 'unix:///tmp/ndb.sock')

const calls = ndb.model('calls', {
  id: 'call_id',
  time: 'ts',
  fields: {
    call_id: { type: 'string' },
    status: { type: 'string', index: true },
    dur: { type: 'number', optional: true },
    ts: { type: 'date' },
  },
})

const id = `c${Date.now()}`

await calls.insert({ call_id: id, status: 'answered', dur: 42, ts: new Date() })

const answered = await calls.where({ status: 'answered' }).sort({ ts: -1 }).limit(10)

console.warn(`answered calls: ${answered.length}`)

const row = await calls.find(id)

await calls.update(id, { status: 'archived' }, { ifVersion: row._v })

const byStatus = await calls.aggregate([{ $group: { _id: '$status', n: { $count: {} } } }])

console.warn(byStatus.rows)

await ndb.close()
