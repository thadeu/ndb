// A tiny sample app: every 5 seconds it records one visit in the shared
// "visits" collection and prints how many visits each app has.
// NDB_URL (default unix:///run/ndb/ndb.sock) sets the daemon. With
// NDB_DIRECT_DIR set it appends the visit to a file instead (direct mode).
import { appendFile } from 'node:fs/promises'
import { hostname } from 'node:os'
import { randomUUID } from 'node:crypto'
import { connect } from '@thadeu/ndb'

const APP = 'node'
const POD = hostname()

const db = connect(process.env.NDB_URL ?? 'unix:///run/ndb/ndb.sock')

const visits = db.collection('visits', {
  time: 'ts',
  fields: {
    app: { type: 'string', index: true },
    pod: { type: 'string' },
    ts: { type: 'date' },
  },
})

async function record() {
  const visit = { _id: `${APP}-${POD}-${randomUUID()}`, app: APP, pod: POD, ts: new Date() }
  const dir = process.env.NDB_DIRECT_DIR

  if (!dir) {
    await visits.insert(visit)

    return
  }

  await appendFile(`${dir}/${APP}-${POD}.ndjson`, `${JSON.stringify(visit)}\n`)
}

for (;;) {
  try {
    await record()

    const mine = await visits.count({ app: APP })
    const all = await visits.count()

    console.warn(`${APP} visits: ${mine}, all apps: ${all}`)
  } catch (err) {
    console.warn(`waiting for ndb: ${err.message}`)
  }

  await new Promise((resolve) => setTimeout(resolve, 5000))
}
