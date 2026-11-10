// The real Redis for the parity runs of the adapter tests: NDB_REDIS_ADDR is
// "host:port" or a redis:// URL. Without it, the tests run on ndb only.
const addr = process.env.NDB_REDIS_ADDR ?? ''

export const redisUrl = addr === '' || addr.includes('://') ? addr : `redis://${addr}`

let seq = 0

// A key that no other run uses: the tests never delete what is not theirs.
export function streamKey(): string {
  seq += 1

  return `ndbts${process.pid}x${Date.now().toString(36)}x${seq}`
}

export function later(ms: number, fn: () => Promise<unknown>): void {
  setTimeout(() => void fn(), ms)
}

// pairs turns a flat [name, value, name, value] answer into an object.
export function pairs(flat: unknown): Record<string, unknown> {
  const list = flat as unknown[]
  const out: Record<string, unknown> = {}

  for (let i = 0; i < list.length; i += 2) {
    out[String(list[i])] = list[i + 1]
  }

  return out
}
