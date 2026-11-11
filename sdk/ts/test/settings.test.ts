import { afterAll, describe, expect, it } from 'vitest'
import { unixDb } from './helpers.js'

const db = unixDb()

afterAll(() => db.close())

describe('settings', () => {
  it('reads the settings with the key masked, and stores a change', async () => {
    const got = (await db.settings()) as { encryption: { key: string; key_id: string } }

    expect(got.encryption.key).toContain('****')
    expect(got.encryption.key_id).toBe('k1')

    const put = (await db.putSettings({ log: { slow_ms: 900 } })) as { log: { slow_ms: { value: number; source: string } } }

    expect(put.log.slow_ms).toEqual({ value: 900, source: 'stored' })
    await expect(db.putSettings({ encryption: { key: 'x' } })).rejects.toThrow()
  })
})
