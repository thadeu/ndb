import { NdbError } from './errors.js'
import type { Ndb } from './ndb.js'

// Test support, like Ndb::Testing in Rails and ndb.Testing in Go. ndb has no
// transactions, so a test cannot roll back: clean the collections a test
// used after it, or after the suite. A truncate takes a few ms at any size
// and keeps the definition and the indexes. It works in any runner:
//
//   afterEach(() => clean(db))          // vitest, jest, node:test, bun test
//
// Every clean refuses to run outside a test: NODE_ENV=test (jest, bun
// test), VITEST, JEST_WORKER_ID or NODE_TEST_CONTEXT (node --test). Set
// NDB_ALLOW_CLEAN=1 to force it.

export interface CleanOptions {
  // Truncate these collections, not the ones the db used.
  only?: string[]
  // Leave these out.
  except?: string[]
}

export class UnsafeCleanError extends Error {
  constructor() {
    super('ndb: test clean refused: this is not a test run (set NDB_ALLOW_CLEAN=1 to force it)')
  }
}

// Truncates the collections that db used since the last clean (or only),
// less except, and gives their names. A collection that does not exist, or
// is in direct mode, is left out without an error.
export async function clean(db: Ndb, opts: CleanOptions = {}): Promise<string[]> {
  guard()

  const names = opts.only ?? [...db.used].sort()

  if (!opts.only) db.used.clear()

  const cleaned: string[] = []

  for (const name of names) {
    if (opts.except?.includes(name)) continue

    await truncate(db, name)

    cleaned.push(name)
  }

  return cleaned
}

// Truncates every engine collection whose name has the prefix of db, less
// except: for the start of a run. Direct collections and partitions are
// left out.
export async function cleanAll(db: Ndb, opts: Omit<CleanOptions, 'only'> = {}): Promise<string[]> {
  guard()

  const names = (await db.collections())
    .filter((d) => d.write !== 'direct' && d.partition_of == null)
    .map((d) => String(d.name))
    .filter((name) => name.startsWith(db.prefix))

  return clean(db, { ...opts, only: names })
}

const leftOut = new Set(['unknown_collection', 'direct_collection', 'not_supported'])

async function truncate(db: Ndb, name: string): Promise<void> {
  const path = `/${encodeURIComponent(name)}/_truncate?confirm=${encodeURIComponent(name)}`

  try {
    await db.request('POST', path)
  } catch (err) {
    if (err instanceof NdbError && leftOut.has(err.code)) return

    throw err
  }
}

function guard(): void {
  const env = process.env

  const testRun = env.NODE_ENV === 'test' || env.VITEST !== undefined || env.JEST_WORKER_ID !== undefined || env.NODE_TEST_CONTEXT !== undefined

  if (testRun || env.NDB_ALLOW_CLEAN === '1') return

  throw new UnsafeCleanError()
}
