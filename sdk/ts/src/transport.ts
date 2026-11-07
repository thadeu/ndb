import { Readable } from 'node:stream'
import { Pool, type Dispatcher } from 'undici'
import { NdbError, makeError } from './errors.js'
import { jsonRows } from './ndjson.js'

export type Method = 'GET' | 'POST' | 'PUT' | 'DELETE'

export interface Streamed {
  status: number
  headers: Record<string, string | string[] | undefined>
  body: Dispatcher.ResponseData["body"]
  rows(): AsyncGenerator<unknown>
  header(name: string): string | undefined
}

const staleSocket = new Set(['UND_ERR_SOCKET', 'ECONNRESET', 'EPIPE'])
const notConnected = new Set(['ECONNREFUSED', 'ENOENT'])

const retryBaseMs = 50
const retryCapMs = 1000
const defaultReadyTimeoutMs = 10_000
const readOps = /\/(_search|_count|_aggregate)$/

export interface ConnectOptions {
  // Bearer token for the TCP listener. Default: env NDB_TOKEN.
  token?: string
  // How long (ms) a call retries while the daemon starts or recovers
  // (503 not_ready, socket missing or refusing). Default 10000; 0 disables.
  readyTimeout?: number
}

// Bun has its own undici, whose Pool cannot dial a unix socket. On Bun the
// transport uses its fetch, which can.
const onBun = typeof (globalThis as { Bun?: unknown }).Bun !== 'undefined'

export class Transport {
  private readonly client: Pool | undefined
  private readonly base: string
  private readonly socketPath: string | undefined
  private readonly token: string | undefined
  private readonly readyTimeout: number

  // addr: unix:///path or tcp://host:port. Default: env NDB_URL.
  constructor(addr?: string, opts: ConnectOptions = {}) {
    const target = addr || process.env.NDB_URL || ''
    const url = new URL(target)

    this.token = opts.token || process.env.NDB_TOKEN || undefined
    this.readyTimeout = opts.readyTimeout ?? defaultReadyTimeoutMs

    if (url.protocol === 'unix:' && url.pathname !== '') {
      this.base = 'http://ndb'
      this.socketPath = decodeURIComponent(url.pathname)
    } else if (url.protocol === 'tcp:' && url.host !== '') {
      this.base = `http://${url.host}`
    } else {
      throw new TypeError(`ndb: address "${target}" must be unix:///path or tcp://host:port`)
    }

    if (!onBun) {
      this.client = new Pool(this.base, { socketPath: this.socketPath, connections: 16 })
    }
  }

  async stream(method: Method, path: string, body?: unknown, signal?: AbortSignal, contentType?: string): Promise<Streamed> {
    const res = await this.withReadyRetry(method, path, signal, async () => {
      const r = await this.send(method, path, body, signal, contentType)

      if (r.statusCode >= 300) {
        throw await apiError(r)
      }

      return r
    })

    return {
      status: res.statusCode,
      headers: res.headers,
      body: res.body,
      rows: () => jsonRows(res.body),
      header: (name) => headerOf(res.headers, name),
    }
  }

  // contentType sends body as raw bytes (blobs) instead of JSON.
  async request<T = unknown>(method: Method, path: string, body?: unknown, contentType?: string): Promise<T> {
    const res = await this.stream(method, path, body, undefined, contentType)
    const text = await res.body.setEncoding('utf8').toArray()

    return (text.length > 0 ? JSON.parse(text.join('')) : undefined) as T
  }

  close(): Promise<void> {
    return this.client?.close() ?? Promise.resolve()
  }

  // Runs fn again while the daemon is not ready, with exponential backoff and
  // jitter. It repeats only when the request certainly was not processed
  // (not_ready, a failed connect), or for a read whose connection dropped.
  // When the deadline passes, the last error is thrown as it was.
  private async withReadyRetry<T>(method: Method, path: string, signal: AbortSignal | undefined, fn: () => Promise<T>): Promise<T> {
    const deadline = Date.now() + this.readyTimeout
    const idempotent = isIdempotent(method, path)

    let delay = retryBaseMs

    for (;;) {
      try {
        return await fn()
      } catch (err) {
        const wait = delay / 2 + Math.random() * (delay / 2)

        if (this.readyTimeout <= 0 || signal?.aborted || !retryable(err, idempotent) || Date.now() + wait > deadline) {
          throw err
        }

        await sleep(wait, signal)

        delay = Math.min(delay * 2, retryCapMs)
      }
    }
  }

  // Rows come as NDJSON: the SDK reads them one by one (jsonRows).
  private headers(contentType = 'application/json'): Record<string, string> {
    const headers: Record<string, string> = { 'content-type': contentType, accept: 'application/x-ndjson' }

    if (this.token) {
      headers.authorization = `Bearer ${this.token}`
    }

    return headers
  }

  private async send(
    method: Method,
    path: string,
    body: unknown,
    signal?: AbortSignal,
    contentType?: string,
  ): Promise<Dispatcher.ResponseData> {
    const raw = contentType !== undefined
    const opts = {
      method,
      path,
      signal,
      headers: this.headers(contentType),
      body: body === undefined ? undefined : raw ? (body as Uint8Array | string) : JSON.stringify(body),
    }

    const client = this.client

    if (!client) {
      return this.fetchSend(opts)
    }

    try {
      return await client.request(opts)
    } catch (err) {
      if (!isIdempotent(method, path) || !staleSocket.has((err as { code?: string }).code ?? '') || signal?.aborted) {
        throw err
      }

      return await client.request(opts)
    }
  }

  // fetchSend is send with fetch (Bun). The answer has the shape of an undici
  // answer: the rest of the SDK reads it the same way.
  private async fetchSend(opts: {
    method: Method
    path: string
    signal?: AbortSignal
    headers: Record<string, string>
    body?: Uint8Array | string
  }): Promise<Dispatcher.ResponseData> {
    const init = { method: opts.method, headers: opts.headers, body: opts.body, signal: opts.signal, unix: this.socketPath }
    const res = await fetch(this.base + opts.path, init as RequestInit)
    const headers: Record<string, string> = {}

    res.headers.forEach((value, name) => {
      headers[name] = value
    })

    return { statusCode: res.status, headers, body: readableOf(res) } as unknown as Dispatcher.ResponseData
  }
}

// readableOf turns a fetch body into a Node Readable with the arrayBuffer and
// dump methods of an undici body.
function readableOf(res: Response): Readable {
  const stream = res.body ? Readable.fromWeb(res.body as never) : Readable.from([])

  return Object.assign(stream, {
    arrayBuffer: async () => {
      const bytes = Buffer.concat(await stream.toArray())

      return bytes.buffer.slice(bytes.byteOffset, bytes.byteOffset + bytes.byteLength)
    },
    dump: async () => {
      stream.resume()
    },
  })
}

function isIdempotent(method: Method, path: string): boolean {
  return method === 'GET' || readOps.test(path.split('?')[0] ?? path)
}

function errorCodes(err: unknown): string[] {
  const e = err as { code?: string; cause?: unknown; errors?: unknown[] }
  const nested = [...(e.errors ?? []), ...(e.cause ? [e.cause] : [])]

  return [e.code ?? '', ...nested.flatMap(errorCodes)]
}

function retryable(err: unknown, idempotent: boolean): boolean {
  if (err instanceof NdbError) {
    return err.code === 'not_ready'
  }

  const codes = errorCodes(err)

  if (codes.some((c) => notConnected.has(c))) {
    return true
  }

  return idempotent && codes.some((c) => staleSocket.has(c))
}

function sleep(ms: number, signal?: AbortSignal): Promise<void> {
  return new Promise((resolve) => {
    const done = () => {
      clearTimeout(timer)
      signal?.removeEventListener('abort', done)
      resolve()
    }
    const timer = setTimeout(done, ms)

    signal?.addEventListener('abort', done, { once: true })
  })
}

function headerOf(headers: Record<string, string | string[] | undefined>, name: string): string | undefined {
  const v = headers[name.toLowerCase()]

  return Array.isArray(v) ? v[0] : v
}

async function apiError(res: Dispatcher.ResponseData): Promise<Error> {
  const text = (await res.body.setEncoding('utf8').toArray()).join('')

  try {
    const { error, status } = JSON.parse(text) as { error?: { code?: string; message?: string }; status?: string }

    if (res.statusCode === 503 && status === 'recovering') {
      return makeError('not_ready', 'ndb is recovering', 503)
    }

    if (error?.code) {
      return makeError(error.code, error.message ?? '', res.statusCode)
    }
  } catch {
    return makeError('http_status', text.trim(), res.statusCode)
  }

  return makeError('http_status', text.trim(), res.statusCode)
}
