import { execFileSync, spawn, type ChildProcess } from 'node:child_process'
import { mkdtempSync, rmSync, writeFileSync } from 'node:fs'
import { createServer } from 'node:net'
import { tmpdir } from 'node:os'
import { join, resolve } from 'node:path'
import type { TestProject } from 'vitest/node'

declare module 'vitest' {
  export interface ProvidedContext {
    unixAddr: string
    tcpAddr: string
    tokenTcpAddr: string
    token: string
  }
}

const repoRoot = resolve(import.meta.dirname, '../../..')

const token = 'ts-sdk-secret'

let daemon: ChildProcess | undefined
let tokenDaemon: ChildProcess | undefined
let dir = ''

function freePort(): Promise<number> {
  return new Promise((resolvePort, reject) => {
    const srv = createServer()

    srv.once('error', reject)
    srv.listen(0, '127.0.0.1', () => {
      const { port } = srv.address() as { port: number }

      srv.close(() => resolvePort(port))
    })
  })
}

async function waitReady(sock: string): Promise<void> {
  const { Client } = await import('undici')
  const client = new Client('http://ndb', { socketPath: sock })

  let last: unknown

  for (let i = 0; i < 200; i++) {
    try {
      const res = await client.request({ path: '/health', method: 'GET' })

      await res.body.dump()

      if (res.statusCode === 200) {
        await client.close()

        return
      }
    } catch (err) {
      last = err
    }

    await new Promise((r) => setTimeout(r, 50))
  }

  throw new Error(`daemon did not become ready: ${String(last)}`)
}

export default async function setup(project: TestProject): Promise<() => Promise<void>> {
  dir = mkdtempSync(join(tmpdir(), 'ndbts'))

  const bin = join(dir, 'ndb')
  const sock = join(dir, 'ndb.sock')
  const port = await freePort()

  execFileSync('go', ['build', '-o', bin, './cmd/ndb'], { cwd: repoRoot, stdio: 'inherit' })

  daemon = spawn(
    bin,
    ['-config', join(dir, 'absent.yml'), '-data-dir', join(dir, 'data'), '-unix', sock, '-tcp', `127.0.0.1:${port}`],
    { stdio: 'inherit' },
  )

  const tokenSock = join(dir, 'tok.sock')
  const tokenPort = await freePort()
  const tokenFile = join(dir, 'token')

  writeFileSync(tokenFile, `${token}\n`)

  tokenDaemon = spawn(
    bin,
    [
      '-config', join(dir, 'absent.yml'),
      '-data-dir', join(dir, 'tokdata'),
      '-unix', tokenSock,
      '-tcp', `127.0.0.1:${tokenPort}`,
      '-token-file', tokenFile,
    ],
    { stdio: 'inherit' },
  )

  try {
    await Promise.all([waitReady(sock), waitReady(tokenSock)])
  } catch (err) {
    daemon.kill('SIGTERM')
    tokenDaemon.kill('SIGTERM')

    throw err
  }

  project.provide('unixAddr', `unix://${sock}`)
  project.provide('tcpAddr', `tcp://127.0.0.1:${port}`)
  project.provide('tokenTcpAddr', `tcp://127.0.0.1:${tokenPort}`)
  project.provide('token', token)

  return async () => {
    await Promise.all([daemon, tokenDaemon].map(stopDaemon))

    rmSync(dir, { recursive: true, force: true })
  }
}

async function stopDaemon(proc: ChildProcess | undefined): Promise<void> {
  const exited = new Promise((r) => proc?.once('exit', r))

  proc?.kill('SIGTERM')
  await exited
}
