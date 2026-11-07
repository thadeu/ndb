import { readdirSync, readFileSync } from 'node:fs'
import { join, resolve } from 'node:path'
import { afterAll, describe, expect, it } from 'vitest'
import { NdbError } from '../src/index.js'
import { collName, tcpDb, unixDb } from './helpers.js'

interface Step {
  op: string
  body: unknown
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
  ['unix', unixDb()],
  ['tcp', tcpDb()],
] as const

afterAll(() => Promise.all(transports.map(([, db]) => db.close())))

it('finds the shared cases', () => {
  expect(cases.length).toBeGreaterThan(40)
})

describe.each(transports)('shared cases over %s', (_t, db) => {
  it.each(cases)('%s', async (_file, c) => {
    const base = `/${collName()}`

    await db.request('PUT', base, c.setup.catalog)

    for (const w of c.setup.writes) {
      await db.request('POST', `${base}/_${w.op}`, w.body)
    }

    const run = async () => {
      const res = await db.stream('POST', `${base}/_${c.request.op}`, c.request.body)
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
