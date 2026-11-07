// Direct mode: other processes append NDJSON lines to files, ndb only reads them.
// Run it with Node 24: node logs/ts/main.ts. It starts ../append.sh as the "other process".
import { execFileSync } from 'node:child_process'
import { connect } from '@thadeu/ndb'

const ndb = connect(process.env.NDB_URL ?? 'unix:///tmp/ndb-examples.sock')

const logs = ndb.collection('app_logs', {
  time: 'ts',
  fields: {
    ts: { type: 'date' },
    app: { type: 'string', index: true },
    level: { type: 'string', index: true },
    msg: { type: 'string' },
    path: { type: 'string', optional: true },
    ms: { type: 'number' },
  },
})

const history = { ts: { $lt: new Date('2026-11-02T00:00:00Z') } }

// The daemon sees a new line within a moment (file events, with a 5 s poll as the fallback).
async function waitFor(ready: () => Promise<boolean>) {
  const deadline = Date.now() + 15_000

  while (!(await ready())) {
    if (Date.now() > deadline) {
      throw new Error('timeout waiting for the daemon to read the files')
    }

    await new Promise((resolve) => setTimeout(resolve, 200))
  }
}

await waitFor(async () => (await logs.count(history)) === 12)

console.log('history: 12 lines from 2 files')

// #region filter
const failures = await logs.query(history).query({ level: 'error' }).sort({ ts: -1 })

console.log('errors, newest first:')

for (const l of failures) {
  console.log(`  ${l.ts.toISOString().slice(11, 19)} ${l.app.padEnd(6)} ${l.msg}`)
}

const slow = await logs.query(history).query({ ms: { $gte: 800 } }).count()

console.log(`slow (ms >= 800): ${slow}`)
// #endregion filter

// #region regex
const payments = await logs.query(history).query({ path: { $regex: '^/api/v[0-9]+/payments' } }).count()

console.log(`payment paths ($regex): ${payments}`)
// #endregion regex

// #region cursor
const pages: number[] = []
let query = logs.query(history).sort({ ts: 1 }).limit(5)

for (;;) {
  const rows = await query

  pages.push(rows.length)

  if (query.nextCursor === '') {
    break
  }

  query = query.after(query.nextCursor)
}

console.log(`pages (limit 5): ${pages.join(', ')}`)
// #endregion cursor

// #region append
const msg = `hello from ts ${Date.now()}`
const line = JSON.stringify({ ts: new Date().toISOString().slice(0, 19) + 'Z', app: 'cli', level: 'info', msg })

execFileSync(new URL('../append.sh', import.meta.url).pathname, ['cli', '3003', line])

console.log('append: cli wrote one line')

await waitFor(async () => (await logs.count({ msg })) === 1)

console.log('the new line is in the collection')

const added = await logs.query({ msg }).first()

console.log(`it has no _id, so it has no _doc/{id} routes: _id is "${added?._id ?? ''}"`)
// #endregion append

await ndb.close()
