import { execFileSync, spawn, type ChildProcess } from 'node:child_process'
import { mkdtempSync, rmSync } from 'node:fs'
import { createServer, type Server } from 'node:net'
import { createServer as createHttp, type Server as HttpServer } from 'node:http'
import { tmpdir } from 'node:os'
import { join, resolve } from 'node:path'
import { afterEach, describe, expect, it } from 'vitest'
import { NdbError, connect } from '../src/index.js'

const notReady = '{"error":{"code":"not_ready","message":"ndb is recovering"}}'

const closers: Array<() => Promise<void> | void> = []

afterEach(async () => {
  while (closers.length > 0) {
    await closers.pop()!()
  }
})

function listen(server: Server | HttpServer): Promise<number> {
  return new Promise((res) => {
    server.listen(0, '127.0.0.1', () => {
      closers.push(() => new Promise<void>((done) => {
        server.close(() => done())

        if ('closeAllConnections' in server) {
          server.closeAllConnections()
        }
      }))

      res((server.address() as { port: number }).port)
    })
  })
}

describe('ready retry', () => {
  it('waits for a daemon that starts after the request', async () => {
    const dir = mkdtempSync(join(tmpdir(), 'ndbr'))
    const sock = join(dir, 's.sock')
    let proc: ChildProcess | undefined

    closers.push(async () => {
      const exited = new Promise((r) => proc?.once('exit', r))

      proc?.kill('SIGTERM')
      await exited
      rmSync(dir, { recursive: true, force: true })
    })

    execFileSync('go', ['build', '-o', join(dir, 'ndb'), './cmd/ndb'], { cwd: resolve(import.meta.dirname, '../../..') })

    setTimeout(() => {
      proc = spawn(join(dir, 'ndb'), ['-config', join(dir, 'absent.yml'), '-data-dir', join(dir, 'd'), '-unix', sock], { stdio: 'ignore' })
    }, 300)

    const db = connect(`unix://${sock}`)

    closers.push(() => db.close())

    await expect(db.health()).resolves.toBeUndefined()
  }, 30_000)

  it('retries 503 not_ready then succeeds', async () => {
    let calls = 0
    const http = createHttp((_req, res) => {
      calls += 1

      if (calls <= 2) {
        res.writeHead(503, { 'content-type': 'application/json' }).end(notReady)

        return
      }

      res.writeHead(200, { 'content-type': 'application/json' }).end('{}')
    })
    const port = await listen(http)
    const db = connect(`tcp://127.0.0.1:${port}`)

    closers.push(() => db.close())

    await db.request('POST', '/x/_bulk', { docs: [] })

    expect(calls).toBe(3)
  })

  it('surfaces the original error at the deadline', async () => {
    const http = createHttp((_req, res) => {
      res.writeHead(503, { 'content-type': 'application/json' }).end(notReady)
    })
    const port = await listen(http)
    const db = connect(`tcp://127.0.0.1:${port}`, { readyTimeout: 200 })

    closers.push(() => db.close())

    const err = await db.health().catch((e: unknown) => e)

    expect(err).toBeInstanceOf(NdbError)
    expect((err as NdbError).code).toBe('not_ready')
  })

  it('does not retry when readyTimeout is 0', async () => {
    let calls = 0
    const http = createHttp((_req, res) => {
      calls += 1
      res.writeHead(503, { 'content-type': 'application/json' }).end(notReady)
    })
    const port = await listen(http)
    const db = connect(`tcp://127.0.0.1:${port}`, { readyTimeout: 0 })

    closers.push(() => db.close())

    await expect(db.health()).rejects.toBeInstanceOf(NdbError)
    expect(calls).toBe(1)
  })

  it('stops waiting when the signal aborts', async () => {
    const db = connect('unix:///tmp/ndb-missing-ts.sock')
    const ac = new AbortController()

    closers.push(() => db.close())
    setTimeout(() => ac.abort(), 100)

    const started = Date.now()

    await expect(db.stream('GET', '/_health', undefined, ac.signal)).rejects.toBeDefined()
    expect(Date.now() - started).toBeLessThan(2000)
  })

  async function dropServer(): Promise<{ db: ReturnType<typeof connect>; conns: () => number }> {
    let conns = 0
    const srv = createServer((sock) => {
      conns += 1
      sock.once('data', () => sock.destroy())
    })
    const port = await listen(srv)
    const db = connect(`tcp://127.0.0.1:${port}`, { readyTimeout: 500 })

    closers.push(() => db.close())

    return { db, conns: () => conns }
  }

  it('never repeats a write that was sent and dropped', async () => {
    const { db, conns } = await dropServer()

    await expect(db.request('POST', '/x/_bulk', { docs: [{ a: 1 }] })).rejects.toBeDefined()
    expect(conns()).toBe(1)
  })

  it('repeats a read that was sent and dropped', async () => {
    const { db, conns } = await dropServer()

    await expect(db.request('POST', '/x/_search', {})).rejects.toBeDefined()
    expect(conns()).toBeGreaterThan(1)
  })
})
