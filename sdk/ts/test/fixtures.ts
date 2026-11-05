import type { Ndb } from '../src/index.js'
import { collName } from './helpers.js'

export function callsModel(db: Ndb) {
  return db.model(collName(), {
    id: 'call_id',
    time: 'ts',
    fields: {
      call_id: { type: 'string' },
      status: { type: 'string', index: true },
      dur: { type: 'number', optional: true },
      vip: { type: 'boolean', optional: true },
      ts: { type: 'date' },
      meta: { type: 'json', optional: true },
    },
  })
}
