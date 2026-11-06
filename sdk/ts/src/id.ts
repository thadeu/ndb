let lastMs = 0
let counter = 0

// A new UUIDv7 (RFC 9562), lowercase and hyphenated: the same kind of id that
// the daemon makes. Ids from one process sort in the order of their creation.
export function newId(): string {
  const bytes = new Uint8Array(16)

  crypto.getRandomValues(bytes)

  const now = Date.now()

  if (now > lastMs) {
    lastMs = now
    counter = seedCounter()
  } else if (counter === 0x0fff) {
    lastMs += 1
    counter = seedCounter()
  } else {
    counter += 1
  }

  const ms = BigInt(lastMs)

  for (let i = 0; i < 6; i++) {
    bytes[i] = Number((ms >> BigInt(8 * (5 - i))) & 0xffn)
  }

  bytes[6] = 0x70 | (counter >> 8)
  bytes[7] = counter & 0xff
  bytes[8] = 0x80 | (bytes[8]! & 0x3f)

  const hex = Array.from(bytes, (b) => b.toString(16).padStart(2, '0')).join('')

  return `${hex.slice(0, 8)}-${hex.slice(8, 12)}-${hex.slice(12, 16)}-${hex.slice(16, 20)}-${hex.slice(20)}`
}

function seedCounter(): number {
  const r = new Uint16Array(1)

  crypto.getRandomValues(r)

  return r[0]! & 0x07ff
}
