import { mkdtempSync, readFileSync, rmSync } from 'node:fs'
import { createServer, type IncomingHttpHeaders, type Server } from 'node:http'
import { tmpdir } from 'node:os'
import { join } from 'node:path'
import { afterEach, describe, expect, it } from 'vitest'
import { VERSION, connect } from '../src/index.js'
import { runtimeOf, userAgent } from '../src/useragent.js'

const pkg = JSON.parse(readFileSync(new URL('../package.json', import.meta.url), 'utf8')) as { version: string }

const sdkAgent = `ndb-ts/${pkg.version} (node v${process.versions.node}; ${process.platform})`

const closers: Array<() => Promise<void> | void> = []

afterEach(async () => {
  while (closers.length > 0) {
    await closers.pop()!()
  }
})

// fakeDaemon answers {} to every request and keeps the request headers.
function fakeDaemon(): { server: Server, seen: IncomingHttpHeaders[] } {
  const seen: IncomingHttpHeaders[] = []

  const server = createServer((req, res) => {
    seen.push(req.headers)

    req.resume()
    res.setHeader('content-type', 'application/json')
    res.end('{}')
  })

  closers.push(() => new Promise<void>((done) => {
    server.close(() => done())
    server.closeAllConnections()
  }))

  return { server, seen }
}

describe('user agent', () => {
  it('VERSION is the version of package.json', () => {
    expect(VERSION).toBe(pkg.version)
  })

  it('every request over TCP sends the SDK user agent', async () => {
    const { server, seen } = fakeDaemon()

    await new Promise<void>((res) => server.listen(0, '127.0.0.1', res))

    const port = (server.address() as { port: number }).port
    const db = connect(`tcp://127.0.0.1:${port}`, { readyTimeout: 0 })

    await db.health()
    await db.putBlob(new TextEncoder().encode('x'), 'text/plain').catch(() => undefined)

    expect(seen.length).toBe(2)

    for (const headers of seen) {
      expect(headers['user-agent']).toBe(sdkAgent)
    }
  })

  it('userAgent appends the app token, over a unix socket', async () => {
    const dir = mkdtempSync(join(tmpdir(), 'ndbua'))
    const sock = join(dir, 's.sock')
    const { server, seen } = fakeDaemon()

    closers.push(() => rmSync(dir, { recursive: true, force: true }))

    await new Promise<void>((res) => server.listen(sock, res))

    const db = connect(`unix://${sock}`, { readyTimeout: 0, userAgent: 'myapp/1.2' })

    await db.health()

    expect(seen[0]?.['user-agent']).toBe(`${sdkAgent} myapp/1.2`)
  })

  it('names Bun and Deno, and sends no header in a browser', () => {
    expect(runtimeOf({ Bun: { version: '1.3.2' }, process: { platform: 'linux' } })).toEqual({ runtime: 'bun v1.3.2', platform: 'linux' })
    expect(runtimeOf({ Deno: { version: { deno: '2.5.0' }, build: { os: 'darwin' } } })).toEqual({ runtime: 'deno v2.5.0', platform: 'darwin' })
    expect(userAgent('myapp/1.2', {})).toBeUndefined()
  })
})
