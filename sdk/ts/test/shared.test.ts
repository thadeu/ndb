import { readdirSync, readFileSync } from 'node:fs'
import { join, resolve } from 'node:path'
import { afterAll, describe, expect, inject, it } from 'vitest'
import { NdbError, connect, type Ndb } from '../src/index.js'
import { collName, tcpDb, unixDb } from './helpers.js'

interface Step {
  op: string
  body: unknown
  database?: string
}

interface SharedCase {
  name: string
  setup: { catalog: unknown; writes: Step[] }
  request: Step
  expect: { ordered?: boolean; rows?: unknown[]; error?: string }
}

const casesDir = resolve(import.meta.dirname, '../../../spec/cases')

function loadCases(): [string, SharedCase][] {
  return readdirSync(casesDir, { recursive: true, encoding: 'utf8' })
    .filter((f) => f.endsWith('.json'))
    .sort()
    .map((f) => [f, JSON.parse(readFileSync(join(casesDir, f), 'utf8')) as SharedCase])
}

function canonical(v: unknown): unknown {
  if (Array.isArray(v)) {
    return v.map(canonical)
  }

  if (v !== null && typeof v === 'object') {
    return Object.fromEntries(
      Object.entries(v)
        .sort(([a], [b]) => (a < b ? -1 : 1))
        .map(([k, x]) => [k, canonical(x)]),
    )
  }

  return v
}

function normalize(rows: unknown[], ordered: boolean): string[] {
  const out = rows.map((r) => JSON.stringify(canonical(r)))

  return ordered ? out : out.sort()
}

const cases = loadCases()
const transports = [
  ['unix', unixDb(), inject('unixAddr')],
  ['tcp', tcpDb(), inject('tcpAddr')],
] as const

const others: Ndb[] = []

// within gives the handle of a database of a case: the same address with
// ?db=, the way an app names its database in NDB_URL.
function within(db: Ndb, addr: string, database: string | undefined): Ndb {
  if (!database) {
    return db
  }

  const other = connect(`${addr}?db=${database}`)

  others.push(other)

  return other
}

afterAll(() => Promise.all([...transports.map(([, db]) => db.close()), ...others.map((db) => db.close())]))

it('finds the shared cases', () => {
  expect(cases.length).toBeGreaterThan(40)
})

describe.each(transports)('shared cases over %s', (_t, db, addr) => {
  it.each(cases)('%s', async (_file, c) => {
    const base = `/${collName()}`

    await db.request('PUT', base, c.setup.catalog)

    for (const w of c.setup.writes) {
      await within(db, addr, w.database).request('POST', `${base}/_${w.op}`, w.body)
    }

    const run = async () => {
      const target = within(db, addr, c.request.database)
      const res = await target.stream('POST', `${base}/_${c.request.op}?decrypt=1`, c.request.body)
      const rows: unknown[] = []

      for await (const row of res.rows()) {
        rows.push(row)
      }

      return rows
    }

    if (c.expect.error) {
      const err = await run().catch((e: unknown) => e)

      expect(err).toBeInstanceOf(NdbError)
      expect((err as NdbError).code).toBe(c.expect.error)

      return
    }

    const got = normalize(await run(), c.expect.ordered ?? false)

    expect(got).toEqual(normalize(c.expect.rows ?? [], c.expect.ordered ?? false))
  })
})
