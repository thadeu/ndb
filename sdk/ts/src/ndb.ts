import { Collection } from './collection.js'
import { Transport, type ConnectOptions } from './transport.js'
import type { Blob, Fields, Schema } from './types.js'

export class Ndb {
  readonly transport: Transport

  constructor(addr?: string, opts?: ConnectOptions) {
    this.transport = new Transport(addr, opts)
  }

  // A collection of the daemon; see Collection. Nothing is sent until its
  // first call.
  collection<const F extends Fields>(name: string, schema: Schema<F>): Collection<F> {
    return new Collection<F>(this, name, schema)
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

  stream(...args: Parameters<Transport['stream']>): ReturnType<Transport['stream']> {
    return this.transport.stream(...args)
  }

  close(): Promise<void> {
    return this.transport.close()
  }
}

export function connect(addr?: string, opts?: ConnectOptions): Ndb {
  return new Ndb(addr, opts)
}
