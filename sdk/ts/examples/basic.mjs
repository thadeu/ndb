// Run after `pnpm build`, with a daemon: ndb -unix /tmp/ndb.sock -data-dir /tmp/ndb-data
import { connect, newId } from '@thadeu/ndb'

const ndb = connect(process.env.NDB_ADDR ?? 'unix:///tmp/ndb.sock')

const calls = ndb.model('calls', {
  time: 'ts',
  fields: {
    status: { type: 'string', index: true },
    dur: { type: 'number', optional: true },
    ts: { type: 'date' },
  },
})

const id = newId()

await calls.insert({ _id: id, status: 'answered', dur: 42, ts: new Date() })

const answered = await calls.where({ status: 'answered' }).sort({ ts: -1 }).limit(10)

console.warn(`answered calls: ${answered.length}`)

const row = await calls.get(id)

await calls.update({ _id: id, _v: row._v }, { $set: { status: 'archived' } })

const byStatus = await calls.aggregate([{ $group: { _id: '$status', n: { $count: {} } } }])

console.warn(byStatus.rows)

await ndb.close()
