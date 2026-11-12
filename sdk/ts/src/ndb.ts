import { Collection } from './collection.js'
import { Stream, type Doc } from './stream.js'
import { Transport, type ConnectOptions, type Streamed } from './transport.js'
import type { Blob, Fields, Schema } from './types.js'

export class Ndb {
  readonly transport: Transport
  readonly prefix: string

  // The collections used since the last clean (testing.ts). A pattern
  // (logs-*) is not one.
  readonly used = new Set<string>()

  constructor(addr?: string, opts?: ConnectOptions) {
    this.transport = new Transport(addr, opts)
    this.prefix = opts?.prefix ?? process.env.NDB_PREFIX ?? ''
  }

  // A collection of the daemon; see Collection. Nothing is sent until its
  // first call.
  collection<const F extends Fields>(name: string, schema: Schema<F>): Collection<F> {
    const full = this.prefix + name

    if (!full.includes('*')) this.used.add(full)

    return new Collection<F>(this, full, schema)
  }

  // Resolves when the daemon is ready (GET /_health).
  async health(): Promise<void> {
    await this.transport.request('GET', '/_health')
  }

  // The catalog: one definition per collection (GET /_collections).
  async collections(): Promise<Record<string, unknown>[]> {
    return (await this.transport.request<{ collections: Record<string, unknown>[] }>('GET', '/_collections')).collections
  }

  // Every collection, totals, version and uptime (GET /_stats).
  stats(): Promise<Record<string, unknown>> {
    return this.transport.request('GET', '/_stats')
  }

  // The settings of the daemon (GET /_cluster/settings): the encryption key,
  // masked, and the log settings, each with where its value comes from.
  settings(): Promise<Record<string, unknown>> {
    return this.transport.request('GET', '/_cluster/settings')
  }

  // Changes settings and stores them in the data dir (PUT
  // /_cluster/settings), such as { log: { level: 'debug' } }.
  putSettings(settings: Record<string, unknown>): Promise<Record<string, unknown>> {
    return this.transport.request('PUT', '/_cluster/settings', settings)
  }

  // Disk use per collection and per part (GET /_size).
  size(): Promise<Record<string, unknown>> {
    return this.transport.request('GET', '/_size')
  }

  // Stores bytes in the blob store (POST /_blobs) and returns the reference
  // to put in a document field.
  putBlob(data: Uint8Array | string, contentType = 'application/octet-stream'): Promise<Blob> {
    return this.transport.request<Blob>('POST', '/_blobs', data, contentType)
  }

  // The bytes of a blob (GET /_blobs/{ref}).
  async getBlob(blob: Blob | string): Promise<Uint8Array> {
    const ref = typeof blob === 'string' ? blob : blob.$blob
    const res = await this.transport.stream('GET', `/_blobs/${encodeURIComponent(ref)}`)

    return new Uint8Array(await res.body.arrayBuffer())
  }

  request<T = unknown>(method: 'GET' | 'POST' | 'PUT' | 'DELETE', path: string, body?: unknown): Promise<T> {
    return this.transport.request<T>(method, path, body)
  }

  // With one argument: the stream of that name (see Stream). Nothing is sent
  // until its first call. With a method and a path: a raw request whose
  // answer is read as a stream of bytes or NDJSON rows.
  stream<T = Doc>(name: string): Stream<T>
  stream(...args: Parameters<Transport['stream']>): Promise<Streamed>
  stream<T = Doc>(...args: unknown[]): Stream<T> | Promise<Streamed> {
    if (args.length === 1) {
      const full = this.prefix + String(args[0])

      this.used.add(full)

      return new Stream<T>(this, full)
    }

    return this.transport.stream(...(args as Parameters<Transport['stream']>))
  }

  close(): Promise<void> {
    return this.transport.close()
  }
}

export function connect(addr?: string, opts?: ConnectOptions): Ndb {
  return new Ndb(addr, opts)
}
