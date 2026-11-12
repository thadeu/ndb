// The pessimistic locks that this console holds on documents. The token of
// POST /{coll}/_lock/{id} stays here, on the server: the browser never gets
// it. The next save or delete of that document sends it as ?lock=<token>.
// The locks are in memory, as in ndb: a console that restarts holds none.

export interface HeldLock {
  token: string;
  until: string;
}

const held = new Map<string, HeldLock>();

// lockKey names one document of one daemon.
export function lockKey(url: string, coll: string, id: string): string {
  return [url, coll, id].join('\n');
}

function ended(lock: HeldLock, now: number): boolean {
  const until = Date.parse(lock.until);

  return Number.isNaN(until) || until <= now;
}

// heldLock is the lock of key, or undefined when there is none or its time
// is over.
export function heldLock(key: string, now = Date.now()): HeldLock | undefined {
  const lock = held.get(key);

  if (!lock) return undefined;

  if (ended(lock, now)) {
    held.delete(key);

    return undefined;
  }

  return lock;
}

// keepLock stores a new lock and forgets the ones whose time is over.
export function keepLock(key: string, lock: HeldLock, now = Date.now()): void {
  for (const [other, old] of held) {
    if (ended(old, now)) held.delete(other);
  }

  held.set(key, lock);
}

export function dropLock(key: string): void {
  held.delete(key);
}
