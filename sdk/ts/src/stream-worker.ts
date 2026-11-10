import { NdbError, UnknownGroupError } from './errors.js'
import type { Entry, Group } from './stream.js'

// Does the work on one entry. It resolves to ack the entry. A throw leaves it
// pending: after claimIdleMs, a worker of the group claims it and tries again.
export type Handler<T> = (entry: Entry<T>, signal: AbortSignal) => unknown

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
  // Gets the errors that do not stop the worker: a failed read, a handler
  // error, a poison message (PoisonError). An error that onError throws is
  // ignored: it does not stop the worker or its acks.
  onError?: (err: unknown) => void
  // Stops the worker. Without it, the worker runs forever.
  signal?: AbortSignal
}

// What the worker appends to the dead-letter stream.
export interface DeadEntry {
  stream: string
  group: string
  id: string
  deliveries: number
  doc: Record<string, unknown>
}

// The error onError gets for a poison message, after its ack.
export class PoisonError extends NdbError {
  constructor(
    message: string,
    readonly id: string,
    readonly deliveries: number,
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
  report: (err: unknown) => void
  signal: AbortSignal
}

function settingsOf(opts: WorkOptions): Settings {
  const onError = opts.onError ?? (() => {})
  const claimIdleMs = opts.claimIdleMs === undefined || opts.claimIdleMs === 0 ? 60_000 : opts.claimIdleMs
  const claimEveryMs = opts.claimEveryMs && opts.claimEveryMs > 0 ? opts.claimEveryMs : Math.max(claimIdleMs / 2, 1000)

  return {
    count: opts.count && opts.count > 0 ? opts.count : 10,
    blockMs: opts.blockMs && opts.blockMs > 0 ? opts.blockMs : 5000,
    claimIdleMs,
    claimEveryMs,
    maxDeliveries: opts.maxDeliveries ?? 0,
    deadLetter: opts.deadLetter ?? '',
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
//  3. acks each entry the handler did without an error, in one call;
//  4. every claimEveryMs, claims the entries idle for claimIdleMs, and sends
//     the ones delivered maxDeliveries times to deadLetter.
//
// Delivery is at least once: the work on an entry must be safe to repeat.
// The group must exist (Group.ensure): when it does not exist at the start,
// the promise rejects with UnknownGroupError.
export async function workGroup<T>(g: Group<T>, consumer: string, h: Handler<T>, opts: WorkOptions): Promise<void> {
  const w = settingsOf(opts)

  await workPending(g, consumer, h, w)

  let nextClaim = Date.now() + w.claimEveryMs
  let backoff = 0

  while (!w.signal.aborted) {
    if (w.claimIdleMs > 0 && Date.now() >= nextClaim) {
      await workIdle(g, consumer, h, w)

      nextClaim = Date.now() + w.claimEveryMs
    }

    const block = w.claimIdleMs > 0 ? Math.min(w.blockMs, nextClaim - Date.now()) : w.blockMs

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

    await handle(g, h, w, entries)
  }
}

// workPending does the work on the consumer's own pending entries, in ID
// order. An entry that fails stays pending; the loop moves past it. A read
// that fails is reported and tried again after a wait, as in the main loop.
// Only a group that does not exist rejects: a retry does not fix it.
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

      if (err instanceof UnknownGroupError || (err instanceof NdbError && err.code === 'unknown_collection')) {
        throw err
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

    await handle(g, h, w, entries, await pendingCounts(g, consumer, w, entries))

    after = entries[entries.length - 1]!.id
  }
}

// workIdle claims the idle entries of the group, poison ones first to the
// dead letter.
async function workIdle<T>(g: Group<T>, consumer: string, h: Handler<T>, w: Settings): Promise<void> {
  let idle

  try {
    idle = await g.pendingList({ idleMs: w.claimIdleMs, count: w.count })
  } catch (err) {
    w.report(wrap(`pending ${g.stream.name}/${g.name}`, err))

    return
  }

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
      await bury(g, w, e, n)

      continue
    }

    // The claim is one more delivery.
    counts.set(e.id, n + 1)
    retry.push(e)
  }

  await handle(g, h, w, retry, counts)
}

// pendingCounts gives the delivery counts of the consumer's own pending
// entries. Only a worker with maxDeliveries needs them.
async function pendingCounts<T>(g: Group<T>, consumer: string, w: Settings, entries: Entry<T>[]): Promise<Map<string, number>> {
  const counts = new Map<string, number>()

  if (w.maxDeliveries <= 0 || entries.length === 0) {
    return counts
  }

  try {
    const rows = await g.pendingList({ start: entries[0]!.id, end: entries[entries.length - 1]!.id, count: entries.length, consumer })

    for (const p of rows) {
      counts.set(p.id, p.deliveries)
    }
  } catch (err) {
    w.report(wrap(`pending ${g.stream.name}/${g.name}`, err))
  }

  return counts
}

// bury sends a poison entry to the dead letter, acks it, then reports it.
async function bury<T>(g: Group<T>, w: Settings, e: Entry<T>, deliveries: number): Promise<void> {
  if (w.deadLetter !== '') {
    const dead = g.stream.db.stream<DeadEntry>(w.deadLetter)
    const entry = { stream: g.stream.name, group: g.name, id: e.id, deliveries, doc: e.doc as Record<string, unknown> }

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

  w.report(new PoisonError(`${g.stream.name}/${g.name} ${e.id} after ${deliveries} deliveries`, e.id, deliveries))
}

// handle runs h on each entry and acks the ones that worked, in one call. A
// deleted entry is acked without h: there is nothing to do. counts has the
// delivery count of an entry delivered before (no count is 1); an entry that
// fails on its maxDeliveries-th delivery goes to the dead letter at once.
async function handle<T>(g: Group<T>, h: Handler<T>, w: Settings, entries: Entry<T>[], counts = new Map<string, number>()): Promise<void> {
  const done: string[] = []

  for (const e of entries) {
    if (w.signal.aborted) {
      break
    }

    if (e.deleted) {
      done.push(e.id)

      continue
    }

    try {
      await h(e, w.signal)

      done.push(e.id)
    } catch (err) {
      // A stop (the signal aborted) is not a failure of the entry: it stays
      // pending, with no report and no bury, and the worker stops.
      if (w.signal.aborted) {
        break
      }

      w.report(wrap(`handler ${g.stream.name}/${g.name} ${e.id}`, err))

      const n = counts.get(e.id) ?? 1

      if (w.maxDeliveries > 0 && n >= w.maxDeliveries) {
        await bury(g, w, e, n)
      }
    }
  }

  if (done.length === 0) {
    return
  }

  // The acks go even when the signal aborted: the work on them is done.
  try {
    await g.ack(...done)
  } catch (err) {
    w.report(wrap(`ack ${g.stream.name}/${g.name}`, err))
  }
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
