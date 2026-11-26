import { readFileSync } from 'node:fs'
import { resolve } from 'node:path'
import { inspect } from 'node:util'
import { afterEach, describe, expect, it, vi } from 'vitest'
import { Transport } from '../src/transport.js'

interface UrlCase {
  name: string
  url: string
  env: string | null
  option: string | null
  secret?: string
  want: { token?: string | null; error?: boolean }
}

const file = resolve(import.meta.dirname, '../../../spec/url/cases.json')
const { cases } = JSON.parse(readFileSync(file, 'utf8')) as { cases: UrlCase[] }

function sentToken(t: Transport): string | null {
  const headers = (t as unknown as { headers(): Record<string, string> }).headers()
  const auth = headers.authorization

  return auth ? auth.replace(/^Bearer /, '') : null
}

afterEach(() => {
  vi.unstubAllEnvs()
})

describe('spec/url/cases.json', () => {
  it.each(cases.map((c) => [c.name, c] as const))('%s', (_name, c) => {
    vi.stubEnv('NDB_TOKEN', c.env ?? '')

    const opts = c.option === null ? {} : { token: c.option }

    if (c.want.error) {
      let err: unknown

      try {
        new Transport(c.url, opts)
      } catch (e) {
        err = e
      }

      expect(err).toBeInstanceOf(TypeError)

      if (c.secret) {
        expect(String((err as Error).message)).not.toContain(c.secret)
        expect(inspect(err)).not.toContain(c.secret)
      }

      return
    }

    const t = new Transport(c.url, opts)

    expect(sentToken(t)).toBe(c.want.token ?? null)

    if (c.secret) {
      expect(inspect(t)).not.toContain(c.secret)
    }
  })
})
