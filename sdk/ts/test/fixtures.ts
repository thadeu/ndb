import type { Ndb } from '../src/index.js'
import { collName } from './helpers.js'

export function callsModel(db: Ndb) {
  return db.collection(collName(), {
    time: 'ts',
    fields: {
      status: { type: 'string', index: true },
      dur: { type: 'number', optional: true },
      vip: { type: 'boolean', optional: true },
      ts: { type: 'date' },
      meta: { type: 'json', optional: true },
    },
  })
}
