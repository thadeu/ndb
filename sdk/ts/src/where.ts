import { InvalidQueryError } from './errors.js'

const supportedFlags = new Set(['i', 'm', 's'])

function isPlain(v: unknown): v is Record<string, unknown> {
  if (typeof v !== 'object' || v === null) {
    return false
  }

  const proto = Object.getPrototypeOf(v) as unknown

  return proto === Object.prototype || proto === null
}

// regexParts converts a RegExp to the $regex pattern and $options letters.
// The daemon uses Go RE2, so only the flags i, m and s have a meaning: any
// other flag would change the match in a way the daemon cannot repeat.
export function regexParts(re: RegExp): { $regex: string; $options?: string } {
  const bad = [...re.flags].filter((f) => !supportedFlags.has(f))

  if (bad.length > 0) {
    throw new InvalidQueryError(
      'invalid_query',
      `RegExp flag "${bad.join('')}" is not supported by $regex (use i, m or s): ${String(re)}`,
    )
  }

  return re.flags === '' ? { $regex: re.source } : { $regex: re.source, $options: re.flags }
}

function operators(o: Record<string, unknown>): Record<string, unknown> {
  const out: Record<string, unknown> = {}

  for (const [key, value] of Object.entries(o)) {
    if (key === '$regex' && value instanceof RegExp) {
      const parts = regexParts(value)

      if (parts.$options !== undefined && typeof o.$options === 'string' && o.$options !== '') {
        throw new InvalidQueryError('invalid_query', 'use the RegExp flags or $options, not both')
      }

      out.$regex = parts.$regex

      if (parts.$options !== undefined) {
        out.$options = parts.$options
      }

      continue
    }

    out[key] = toWhere(value)
  }

  return out
}

// toWhere returns a copy of a where object in which every RegExp is the
// wire form of $regex: a RegExp as a field value, or as the value of $regex.
export function toWhere(v: unknown): unknown {
  if (v instanceof RegExp) {
    return regexParts(v)
  }

  if (Array.isArray(v)) {
    return v.map(toWhere)
  }

  if (isPlain(v)) {
    return operators(v)
  }

  return v
}
