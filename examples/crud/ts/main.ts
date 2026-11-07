// CRUD on the users collection: insert, get by _id, update with a _v guard, delete, count.
import { connect } from '@thadeu/ndb'

// #region connect
const ndb = connect(process.env.NDB_URL ?? 'unix:///tmp/ndb-examples.sock') // NDB_TOKEN is read by the SDK

const users = ndb.collection('users', {
  fields: {
    email: { type: 'string', unique: true },
    plan: { type: 'string', index: true },
  },
})
// #endregion connect

for (const id of ['u1', 'u2']) {
  await users.delete(id)
}

// #region insert
const results = await users.insert([
  { _id: 'u1', email: 'ada@example.com', plan: 'free' },
  { _id: 'u2', email: 'grace@example.com', plan: 'free' },
])

console.log(`insert u1, u2 -> v${results[0]?._v}, v${results[1]?._v}`)
// #endregion insert

// #region get
const ada = await users.get('u1')

console.log(`get u1: ${ada?.email} plan=${ada?.plan} v${ada?._v}`)
// #endregion get

// #region update
const saved = await users.update('u1', { $set: { plan: 'pro' } }, { guard: { _v: ada?._v } })

console.log(`update u1 plan=pro (_v ${ada?._v}) -> v${saved.doc?._v}`)
// #endregion update

// #region conflict
const stale = await users.update('u1', { $set: { plan: 'team' } }, { guard: { _v: ada?._v } })

if (stale.matched === 0) {
  console.log(`update u1 plan=team (_v ${ada?._v}) -> matched 0`)

  const fresh = await users.get('u1')
  const retried = await users.update('u1', { $set: { plan: 'team' } }, { guard: { _v: fresh?._v } })

  console.log(`reload u1: v${fresh?._v}, retry with _v ${fresh?._v} -> v${retried.doc?._v}`)
}
// #endregion conflict

// #region delete
await users.delete('u2')

console.log('delete u2')
// #endregion delete

// #region count
const total = await users.count()
const onTeam = await users.count({ plan: 'team' })

console.log(`count: ${total} user(s), ${onTeam} on plan team`)
// #endregion count

await ndb.close()
