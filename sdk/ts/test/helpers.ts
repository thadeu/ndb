import { inject } from 'vitest'
import { connect, type Ndb } from '../src/index.js'

let seq = 0

export function collName(): string {
  seq += 1

  return `t${process.pid}x${seq}`
}

export function unixDb(): Ndb {
  return connect(inject('unixAddr'))
}

export function tcpDb(): Ndb {
  return connect(inject('tcpAddr'))
}
