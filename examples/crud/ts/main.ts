// CRUD on the users collection: insert, get, update with if_version, delete, count.
import { connect, NotFoundError, VersionConflictError } from '@thadeu/ndb'

// #region connect
const ndb = connect(process.env.NDB_URL ?? 'unix:///tmp/ndb-examples.sock') // NDB_TOKEN is read by the SDK

const users = ndb.model('users', {
  id: 'user_id',
  fields: {
    user_id: { type: 'string' },
    email: { type: 'string' },
    plan: { type: 'string', index: true },
  },
})
// #endregion connect

for (const id of ['u1', 'u2']) {
  try {
    await users.delete(id)
  } catch (err) {
    if (!(err instanceof NotFoundError)) {
      throw err
    }
  }
}

// #region insert
const results = await users.insert([
  { user_id: 'u1', email: 'ada@example.com', plan: 'free' },
  { user_id: 'u2', email: 'grace@example.com', plan: 'free' },
])

console.log(`insert u1, u2 -> v${results[0]?.v}, v${results[1]?.v}`)
// #endregion insert

// #region get
const ada = await users.find('u1')

console.log(`get u1: ${ada?.email} plan=${ada?.plan} v${ada?._v}`)
// #endregion get

// #region update
const saved = await users.update('u1', { plan: 'pro' }, { ifVersion: ada?._v })

console.log(`update u1 plan=pro (if_version ${ada?._v}) -> v${saved.v}`)
// #endregion update

// #region conflict
try {
  await users.update('u1', { plan: 'team' }, { ifVersion: ada?._v })
} catch (err) {
  if (!(err instanceof VersionConflictError)) {
    throw err
  }

  console.log(`update u1 plan=team (if_version ${ada?._v}) -> version_conflict`)

  const fresh = await users.find('u1')
  const retried = await users.update('u1', { plan: 'team' }, { ifVersion: fresh?._v })

  console.log(`reload u1: v${fresh?._v}, retry with if_version ${fresh?._v} -> v${retried.v}`)
}
// #endregion conflict

// #region delete
await users.delete('u2')

console.log('delete u2')
// #endregion delete

// #region count
const total = await users.count()
const onTeam = await users.where({ plan: 'team' }).count()

console.log(`count: ${total} user(s), ${onTeam} on plan team`)
// #endregion count

await ndb.close()
