import { makeError } from './errors.js'

interface WatchLine {
  pos?: string
  op?: 'put' | 'del'
  _id?: string
  v?: number
  doc?: Record<string, unknown>
  heartbeat?: boolean
  error?: { code: string; message: string }
}

export interface RawEvent {
  consumer: string
  pos: string
  op: 'put' | 'del'
  _id: string
  v: number
  doc: Record<string, unknown> | undefined
}

export async function* watchEvents(lines: AsyncIterable<unknown>, consumer: string): AsyncGenerator<RawEvent> {
  for await (const raw of lines) {
    const line = raw as WatchLine

    if (line.heartbeat) {
      continue
    }

    if (line.error) {
      throw makeError(line.error.code, line.error.message)
    }

    yield { consumer, pos: line.pos!, op: line.op!, _id: line._id!, v: line.v!, doc: line.doc }
  }
}
