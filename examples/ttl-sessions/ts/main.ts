// Expiry like Redis: expireIn on insert, ttl, expire, persist. An expired document is hidden at once.
import { connect } from '@thadeu/ndb'

const ndb = connect(process.env.NDB_URL ?? 'unix:///tmp/ndb-examples.sock')

// #region schema
const sessions = ndb.model('sessions', {
  time: 'created_at',
  expires: 'expires_at',
  fields: {
    created_at: { type: 'date' },
    expires_at: { type: 'date', optional: true },
  },
})
// #endregion schema

for (const id of ['s-short', 's-long', 's-keep']) {
  await sessions.delete(id)
}

// #region insert
const now = new Date()

await sessions.insert({ _id: 's-short', created_at: now }, { expireIn: 2 })
await sessions.insert([{ _id: 's-long', created_at: now }, { _id: 's-keep', created_at: now }], { expireIn: '1h' })

console.log('insert s-short (expire_in 2s), s-long and s-keep (expire_in 1h)')
// #endregion insert

// #region ttl
console.log(`ttl s-short: ${await sessions.ttl('s-short')}s, s-long: ${await sessions.ttl('s-long')}s`)
// #endregion ttl

// #region extend
await sessions.expire('s-long', '30m')

console.log(`expire s-long 30m -> ttl ${await sessions.ttl('s-long')}s`)
// #endregion extend

// #region persist
await sessions.persist('s-keep')

if ((await sessions.ttl('s-keep')) === -1) {
  console.log('persist s-keep -> ttl -1 (never expires)')
}
// #endregion persist

console.log('waiting 3s...')
await new Promise((resolve) => setTimeout(resolve, 3_000))

// #region after
const alive = await sessions.all()

console.log(`live sessions: ${alive.length} (${alive.map((s) => s._id).join(', ')})`)

if ((await sessions.ttl('s-short')) === null) {
  console.log('ttl s-short: gone')
}
// #endregion after

await ndb.close()
