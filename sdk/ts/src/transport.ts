import { Pool, type Dispatcher } from 'undici'
import { makeError } from './errors.js'
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

export interface ConnectOptions {
  // Bearer token for the TCP listener. Default: env NDB_TOKEN.
  token?: string
}

export class Transport {
  private readonly client: Pool
  private readonly token: string | undefined

  // addr: unix:///path or tcp://host:port. Default: env NDB_URL.
  constructor(addr?: string, opts: ConnectOptions = {}) {
    const target = addr || process.env.NDB_URL || ''
    const url = new URL(target)

    this.token = opts.token || process.env.NDB_TOKEN || undefined

    if (url.protocol === 'unix:' && url.pathname !== '') {
      this.client = new Pool('http://ndb', { socketPath: decodeURIComponent(url.pathname), connections: 16 })
    } else if (url.protocol === 'tcp:' && url.host !== '') {
      this.client = new Pool(`http://${url.host}`, { connections: 16 })
    } else {
      throw new TypeError(`ndb: address "${target}" must be unix:///path or tcp://host:port`)
    }
  }

  async stream(method: Method, path: string, body?: unknown, signal?: AbortSignal): Promise<Streamed> {
    const res = await this.send(method, path, body, signal)

    if (res.statusCode >= 300) {
      throw await apiError(res)
    }

    return {
      status: res.statusCode,
      headers: res.headers,
      body: res.body,
      rows: () => jsonRows(res.body),
      header: (name) => headerOf(res.headers, name),
    }
  }

  async request<T = unknown>(method: Method, path: string, body?: unknown): Promise<T> {
    const res = await this.stream(method, path, body)
    const text = await res.body.setEncoding('utf8').toArray()

    return (text.length > 0 ? JSON.parse(text.join('')) : undefined) as T
  }

  close(): Promise<void> {
    return this.client.close()
  }

  private headers(): Record<string, string> {
    const headers: Record<string, string> = { 'content-type': 'application/json' }

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
  ): Promise<Dispatcher.ResponseData> {
    const opts = {
      method,
      path,
      signal,
      headers: this.headers(),
      body: body === undefined ? undefined : JSON.stringify(body),
    }

    try {
      return await this.client.request(opts)
    } catch (err) {
      if (!staleSocket.has((err as { code?: string }).code ?? '') || signal?.aborted) {
        throw err
      }

      return await this.client.request(opts)
    }
  }
}

function headerOf(headers: Record<string, string | string[] | undefined>, name: string): string | undefined {
  const v = headers[name.toLowerCase()]

  return Array.isArray(v) ? v[0] : v
}

async function apiError(res: Dispatcher.ResponseData): Promise<Error> {
  const text = (await res.body.setEncoding('utf8').toArray()).join('')

  try {
    const { error } = JSON.parse(text) as { error?: { code?: string; message?: string } }

    if (error?.code) {
      return makeError(error.code, error.message ?? '', res.statusCode)
    }
  } catch {
    return makeError('http_status', text.trim(), res.statusCode)
  }

  return makeError('http_status', text.trim(), res.statusCode)
}
