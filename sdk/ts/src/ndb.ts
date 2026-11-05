import { Model } from './model.js'
import { Transport } from './transport.js'
import type { Fields, Schema } from './types.js'

export class Ndb {
  readonly transport: Transport

  constructor(addr: string) {
    this.transport = new Transport(addr)
  }

  model<const F extends Fields>(name: string, schema: Schema<F>): Model<F> {
    return new Model<F>(this, name, schema)
  }

  async health(): Promise<void> {
    await this.transport.request('GET', '/health')
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

export function connect(addr: string): Ndb {
  return new Ndb(addr)
}
