import { NdbError } from './errors.js'
import type { Entry, Group, PendingEntry } from './stream.js'

// Does the work on one entry. It resolves to ack the entry. A throw leaves it
// pending: after claimIdleMs, a worker of the group claims it and tries again.
// With the retry option, a throw nacks it instead: it comes back after the
// wait of retry.
export type Handler<T> = (entry: Entry<T>, signal: AbortSignal) => unknown

// The most a nack can wait: 30 days.
const maxDelayMs = 30 * 24 * 3600 * 1000

// One step of tiers: the wait (ms) of each try up to the try upTo, after
// the steps before it. A step with no upTo is the last one.
export interface TierStep {
  waitMs: number
  upTo?: number
}

// tiers gives a retry function for WorkOptions.retry: the wait (ms) after a
// failed try, by its delivery count (1 is the first try). Each number is the
// wait of one try; a TierStep holds for every try up to its upTo. After the
// last step, its wait repeats.
//
//   tiers(15_000, 60_000, 600_000)   try 1: 15 s, try 2: 1 min, then 10 min
//   tiers({ waitMs: 15_000, upTo: 1 }, { waitMs: 60_000, upTo: 5 }, { waitMs: 600_000 })
//                                    try 1: 15 s, tries 2-5: 1 min, then 10 min
export function tiers(...steps: (number | TierStep)[]): (deliveries: number) => number {
  if (steps.length === 0) {
    throw new Error('ndb: tiers needs at least one wait')
  }

  const bounds: { waitMs: number; upTo: number }[] = []

  let last = 0

  for (const step of steps) {
    const { waitMs, upTo } = typeof step === 'number' ? { waitMs: step, upTo: last + 1 } : step

    if (!Number.isFinite(waitMs) || waitMs < 0) {
      throw new Error(`ndb: tiers: bad wait ${waitMs}`)
    }

    last = upTo === undefined ? Infinity : Math.max(upTo, last)

    bounds.push({ waitMs, upTo: last })
  }

  return (deliveries) => {
    const step = bounds.find((b) => deliveries <= b.upTo) ?? bounds[bounds.length - 1]!

    return step.waitMs
  }
}

export interface WorkOptions {
  // The most entries per read: default 10.
  count?: number
  // How long (ms) a read waits for new entries: default 5000.
  blockMs?: number
  // The entries pending for at least this long (ms), of any consumer of the
  // group, are claimed and tried again. Default 60000; a negative value
  // turns the claim off.
  claimIdleMs?: number
  // How often (ms) the worker looks for idle entries: default claimIdleMs / 2,
  // at least 1000.
  claimEveryMs?: number
  // An entry delivered this many times is a poison message. When the handler
  // fails on that delivery, it goes to deadLetter (when set) and is acked at
  // once. 0 is no limit.
  maxDeliveries?: number
  // The stream that gets the poison messages, as a DeadEntry.
  deadLetter?: string
  // The wait (ms) before an entry whose handler failed comes back: a
  // function of its delivery count (see tiers), or an array of waits (the
  // wait of try n is the n-th one; the last one repeats). The worker nacks
  // the entry with this wait, so it has no owner while it waits, and any
  // worker of the group takes it when it is due. Without retry, a failed
  // entry stays pending until claimIdleMs.
  retry?: ((deliveries: number) => number) | number[]
  // An entry older than this (ms, by the time of its ID) is not tried
  // again: it goes to deadLetter (when set), with reason max_age, and is
  // acked. 0 is no limit.
  maxAgeMs?: number
  // How often (ms) the worker claims the nacked entries that are due:
  // default 1000 with retry. Without retry, the due claim runs only when
  // this is set; a negative value turns it off.
  dueEveryMs?: number
  // Gets the errors that do not stop the worker: a failed read, a handler
  // error, a poison message (PoisonError). An error that onError throws is
  // ignored: it does not stop the worker or its acks.
  onError?: (err: unknown) => void
  // Stops the worker. Without it, the worker runs forever.
  signal?: AbortSignal
}

// Why an entry went to the dead letter: it failed maxDeliveries times, or
// it is older than maxAgeMs.
export type DeadReason = 'max_deliveries' | 'max_age'

// What the worker appends to the dead-letter stream.
export interface DeadEntry {
  stream: string
  group: string
  id: string
  deliveries: number
  reason: DeadReason
  doc: Record<string, unknown>
}

// The error onError gets for a poison message, after its ack.
export class PoisonError extends NdbError {
  constructor(
    message: string,
    readonly id: string,
    readonly deliveries: number,
    readonly reason: DeadReason = 'max_deliveries',
  ) {
    super('poison', message)
  }
}

interface Settings {
  count: number
  blockMs: number
  claimIdleMs: number
  claimEveryMs: number
  maxDeliveries: number
  deadLetter: string
  retry: ((deliveries: number) => number) | null
  maxAgeMs: number
  dueEveryMs: number
  report: (err: unknown) => void
  signal: AbortSignal
}

function settingsOf(opts: WorkOptions): Settings {
  const onError = opts.onError ?? (() => {})
  const claimIdleMs = opts.claimIdleMs === undefined || opts.claimIdleMs === 0 ? 60_000 : opts.claimIdleMs
  const claimEveryMs = opts.claimEveryMs && opts.claimEveryMs > 0 ? opts.claimEveryMs : Math.max(claimIdleMs / 2, 1000)
  const retry = Array.isArray(opts.retry) ? tiers(...opts.retry) : (opts.retry ?? null)
  const dueEveryMs = opts.dueEveryMs === undefined || opts.dueEveryMs === 0 ? (retry ? 1000 : -1) : opts.dueEveryMs

  return {
    count: opts.count && opts.count > 0 ? opts.count : 10,
    blockMs: opts.blockMs && opts.blockMs > 0 ? opts.blockMs : 5000,
    claimIdleMs,
    claimEveryMs,
    maxDeliveries: opts.maxDeliveries ?? 0,
    deadLetter: opts.deadLetter ?? '',
    retry,
    maxAgeMs: opts.maxAgeMs && opts.maxAgeMs > 0 ? opts.maxAgeMs : 0,
    dueEveryMs,
    report: (err) => {
      // A throw in onError must not drop the acks of the batch.
      try {
        onError(err)
      } catch {
        // Nothing to do: onError is the place for errors.
      }
    },
    signal: opts.signal ?? new AbortController().signal,
  }
}

function wrap(what: string, err: unknown): Error {
  const message = err instanceof Error ? err.message : String(err)

  return new Error(`ndb: ${what}: ${message}`, { cause: err })
}


// workGroup runs a worker of the group as consumer until the signal aborts,
// and then resolves. It:
//
//  1. reads its own pending entries first (the ones it took before a restart
//     and did not ack), and does the work on them;
//  2. reads new entries, waiting up to blockMs for them;
//  3. acks each entry the handler did without an error, in one call; with
//     retry, nacks each one that failed, with the wait of its try;
//  4. every claimEveryMs, claims the entries idle for claimIdleMs, and sends
//     the ones delivered maxDeliveries times to deadLetter;
//  5. every dueEveryMs, claims the nacked entries that are due.
//
// Delivery is at least once: the work on an entry must be safe to repeat.
// The group must exist (Group.ensure). A read that fails, also of a group or
// stream that does not exist yet, goes to onError and is tried again after a
// backoff (100 ms, doubled up to 5 s): a worker that starts before its group
// waits for it. Only the signal ends the worker.
export async function workGroup<T>(g: Group<T>, consumer: string, h: Handler<T>, opts: WorkOptions): Promise<void> {
  const w = settingsOf(opts)

  await workPending(g, consumer, h, w)

  let nextClaim = Date.now() + w.claimEveryMs
  let nextDue = Date.now() + Math.max(w.dueEveryMs, 0)
  let dueCursor = '0-0'
  let backoff = 0

  while (!w.signal.aborted) {
    if (w.claimIdleMs > 0 && Date.now() >= nextClaim) {
      await workIdle(g, consumer, h, w)

      nextClaim = Date.now() + w.claimEveryMs
    }

    if (w.dueEveryMs > 0 && Date.now() >= nextDue) {
      dueCursor = await workDue(g, consumer, h, w, dueCursor)

      // A scan that is not complete goes on at the next turn.
      nextDue = dueCursor === '0-0' ? Date.now() + w.dueEveryMs : Date.now()
    }

    let block = w.blockMs

    if (w.claimIdleMs > 0) {
      block = Math.min(block, nextClaim - Date.now())
    }

    if (w.dueEveryMs > 0) {
      block = Math.min(block, nextDue - Date.now())
    }

    let entries: Entry<T>[]

    try {
      entries = await g.read(consumer, { count: w.count, blockMs: Math.max(block, 1), signal: w.signal })
    } catch (err) {
      if (w.signal.aborted) {
        break
      }

      w.report(wrap(`read ${g.stream.name}/${g.name}`, err))

      backoff = Math.min(Math.max(backoff * 2, 100), 5000)

      await sleep(backoff, w.signal)

      continue
    }

    backoff = 0

    await handle(g, consumer, h, w, entries)
  }
}

// workPending does the work on the consumer's own pending entries, in ID
// order. An entry that fails stays pending (or is nacked, with retry); the
// loop moves past it. A nacked entry is not in this read: it has no owner,
// so a restart does not take it before its retry time. A read that fails
// is reported and tried again after a wait, as in the main loop, also of a
// group or stream that does not exist yet.
async function workPending<T>(g: Group<T>, consumer: string, h: Handler<T>, w: Settings): Promise<void> {
  let after = '0'
  let backoff = 0

  while (!w.signal.aborted) {
    let entries: Entry<T>[]

    try {
      entries = await g.read(consumer, { pending: true, after, count: w.count, signal: w.signal })
    } catch (err) {
      if (w.signal.aborted) {
        return
      }

      w.report(wrap(`pending read ${g.stream.name}/${g.name}`, err))

      backoff = Math.min(Math.max(backoff * 2, 100), 5000)

      await sleep(backoff, w.signal)

      continue
    }

    backoff = 0

    if (entries.length === 0) {
      return
    }

    const counts = await pendingCounts(g, consumer, w, entries)
    const todo = await buryWorn(g, w, entries, counts)

    await handle(g, consumer, h, w, todo, counts)

    after = entries[entries.length - 1]!.id
  }
}

// workIdle claims the idle entries of the group, poison ones first to the
// dead letter.
async function workIdle<T>(g: Group<T>, consumer: string, h: Handler<T>, w: Settings): Promise<void> {
  const idle = await idleRows(g, w)

  if (idle.length === 0) {
    return
  }

  const deliveries = new Map(idle.map((p) => [p.id, p.deliveries]))

  let claimed

  try {
    claimed = await g.claim(consumer, w.claimIdleMs, [...deliveries.keys()])
  } catch (err) {
    w.report(wrap(`claim ${g.stream.name}/${g.name}`, err))

    return
  }

  const retry: Entry<T>[] = []
  const counts = new Map<string, number>()

  for (const e of claimed.entries) {
    const n = deliveries.get(e.id) ?? 0

    // A process that stopped before the bury left it pending.
    if (w.maxDeliveries > 0 && n >= w.maxDeliveries) {
      await bury(g, w, e, n, 'max_deliveries')

      continue
    }

    // The claim is one more delivery.
    counts.set(e.id, n + 1)
    retry.push(e)
  }

  await handle(g, consumer, h, w, retry, counts)
}

// idleRows lists up to count entries idle for claimIdleMs. A nacked entry
// that still waits is not one (no claim takes it before its retry time), so
// it is skipped, and the list goes on past it: a long row of waiting
// entries does not hide the idle ones after them.
async function idleRows<T>(g: Group<T>, w: Settings): Promise<PendingEntry[]> {
  const rows: PendingEntry[] = []
  const page = Math.max(w.count, 100)

  let start = '-'

  while (rows.length < w.count) {
    let list: PendingEntry[]

    try {
      list = await g.pendingList({ start, idleMs: w.claimIdleMs, count: page })
    } catch (err) {
      w.report(wrap(`pending ${g.stream.name}/${g.name}`, err))

      break
    }

    const now = Date.now()

    for (const p of list) {
      if (p.retryAt === null || p.retryAt.getTime() <= now) {
        rows.push(p)
      }
    }

    if (list.length < page) {
      break
    }

    start = nextId(list[list.length - 1]!.id)
  }

  return rows.slice(0, w.count)
}

// workDue claims up to count nacked entries that are due, from cursor, and
// does the work on them. It gives the cursor of the next call: "0-0" when
// the scan is complete.
async function workDue<T>(g: Group<T>, consumer: string, h: Handler<T>, w: Settings, cursor: string): Promise<string> {
  let claimed

  try {
    claimed = await g.claimDue(consumer, { cursor, count: w.count })
  } catch (err) {
    w.report(wrap(`due claim ${g.stream.name}/${g.name}`, err))

    return '0-0'
  }

  const counts = await pendingCounts(g, consumer, w, claimed.entries)
  const todo = await buryWorn(g, w, claimed.entries, counts)

  await handle(g, consumer, h, w, todo, counts)

  return claimed.cursor || '0-0'
}

// buryWorn sends to the dead letter the entries delivered more than
// maxDeliveries times with no ack: the process stopped in the handler (a
// crash, a kill). They do not run again. It gives the other entries.
async function buryWorn<T>(g: Group<T>, w: Settings, entries: Entry<T>[], counts: Map<string, number>): Promise<Entry<T>[]> {
  const todo: Entry<T>[] = []

  for (const e of entries) {
    const n = counts.get(e.id) ?? 1

    if (w.maxDeliveries > 0 && n > w.maxDeliveries && !e.deleted) {
      await bury(g, w, e, n - 1, 'max_deliveries')

      continue
    }

    todo.push(e)
  }

  return todo
}

// pendingCounts gives the delivery counts of the consumer's own pending
// entries. Only a worker with maxDeliveries or retry needs them.
async function pendingCounts<T>(g: Group<T>, consumer: string, w: Settings, entries: Entry<T>[]): Promise<Map<string, number>> {
  const counts = new Map<string, number>()

  if ((w.maxDeliveries <= 0 && !w.retry) || entries.length === 0) {
    return counts
  }

  // Other own pending entries can sit between the first and the last ID
  // (claimed entries are not in a row): leave room for them.
  const query = {
    start: entries[0]!.id,
    end: entries[entries.length - 1]!.id,
    count: Math.min(entries.length + 100, 10_000),
    consumer,
  }

  try {
    const rows = await g.pendingList(query)

    for (const p of rows) {
      counts.set(p.id, p.deliveries)
    }
  } catch (err) {
    w.report(wrap(`pending ${g.stream.name}/${g.name}`, err))
  }

  return counts
}

// bury sends a poison entry to the dead letter, acks it, then reports it.
async function bury<T>(g: Group<T>, w: Settings, e: Entry<T>, deliveries: number, reason: DeadReason): Promise<void> {
  if (w.deadLetter !== '') {
    const dead = g.stream.db.stream<DeadEntry>(w.deadLetter)
    const entry = { stream: g.stream.name, group: g.name, id: e.id, deliveries, reason, doc: e.doc as Record<string, unknown> }

    try {
      await dead.append(entry)
    } catch (err) {
      w.report(wrap(`dead letter ${w.deadLetter}`, err))

      return
    }
  }

  try {
    await g.ack(e.id)
  } catch (err) {
    w.report(wrap(`ack ${g.stream.name}/${g.name}`, err))

    return
  }

  const why = reason === 'max_age' ? `older than ${w.maxAgeMs} ms` : `after ${deliveries} deliveries`

  w.report(new PoisonError(`${g.stream.name}/${g.name} ${e.id} ${why}`, e.id, deliveries, reason))
}

// handle runs h on each entry and acks the ones that worked, in one call. A
// deleted entry is acked without h: there is nothing to do. counts has the
// delivery count of an entry delivered before (no count is 1). An entry
// older than maxAgeMs goes to the dead letter without h. An entry that
// fails on its maxDeliveries-th delivery goes to the dead letter at once;
// with retry, any other one that fails is nacked with the wait of its try.
async function handle<T>(g: Group<T>, consumer: string, h: Handler<T>, w: Settings, entries: Entry<T>[], counts = new Map<string, number>()): Promise<void> {
  const done: string[] = []
  const nacks = new Map<number, string[]>()

  for (const e of entries) {
    if (w.signal.aborted) {
      break
    }

    if (e.deleted) {
      done.push(e.id)

      continue
    }

    const n = counts.get(e.id) ?? 1

    if (tooOld(w, e)) {
      await bury(g, w, e, n, 'max_age')

      continue
    }

    try {
      await h(e, w.signal)

      done.push(e.id)
    } catch (err) {
      // A stop (the signal aborted) is not a failure of the entry: it stays
      // pending, with no report, no nack and no bury, and the worker stops.
      if (w.signal.aborted) {
        break
      }

      w.report(wrap(`handler ${g.stream.name}/${g.name} ${e.id}`, err))

      if (w.maxDeliveries > 0 && n >= w.maxDeliveries) {
        await bury(g, w, e, n, 'max_deliveries')

        continue
      }

      // No use to wait: the next try is too old to run.
      if (tooOld(w, e)) {
        await bury(g, w, e, n, 'max_age')

        continue
      }

      const delay = delayOf(g, w, e, n)

      if (delay !== null) {
        nacks.set(delay, [...(nacks.get(delay) ?? []), e.id])
      }
    }
  }

  // The acks and nacks go even when the signal aborted: the work on them is
  // done.
  if (done.length > 0) {
    try {
      await g.ack(...done)
    } catch (err) {
      w.report(wrap(`ack ${g.stream.name}/${g.name}`, err))
    }
  }

  for (const [delay, ids] of nacks) {
    try {
      await g.nack(consumer, ids, delay)
    } catch (err) {
      w.report(wrap(`nack ${g.stream.name}/${g.name}`, err))
    }
  }
}

// tooOld tells if the entry is older than maxAgeMs, by the time of its ID.
function tooOld<T>(w: Settings, e: Entry<T>): boolean {
  return w.maxAgeMs > 0 && Date.now() - e.time.getTime() > w.maxAgeMs
}

// delayOf is the wait (ms) of the nack after try n, in 0 to 30 days, or
// null with no retry. A retry that throws is reported, and the entry stays
// pending.
function delayOf<T>(g: Group<T>, w: Settings, e: Entry<T>, n: number): number | null {
  if (!w.retry) {
    return null
  }

  let ms: number

  try {
    ms = Math.round(w.retry(n))
  } catch (err) {
    w.report(wrap(`retry ${g.stream.name}/${g.name} ${e.id}`, err))

    return null
  }

  if (!Number.isFinite(ms) || ms < 0) {
    return 0
  }

  return Math.min(ms, maxDelayMs)
}

// nextId is the lowest ID after id: the same ms, the next seq.
function nextId(id: string): string {
  const [ms, seq] = id.split('-')

  return `${ms}-${BigInt(seq ?? '0') + 1n}`
}

function sleep(ms: number, signal: AbortSignal): Promise<void> {
  return new Promise((resolve) => {
    const done = () => {
      clearTimeout(timer)
      signal.removeEventListener('abort', done)
      resolve()
    }

    const timer = setTimeout(done, ms)

    signal.addEventListener('abort', done, { once: true })
  })
}
