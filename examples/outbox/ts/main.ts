// Outbox: a producer inserts events, a consumer watches them with a filter and acks after the work.
// Delivery is at least once, so the work must be safe to repeat: a receipt per event id does that.
import { connect, DuplicateIdError, NotFoundError } from '@thadeu/ndb'

const ndb = connect(process.env.NDB_URL ?? 'unix:///tmp/ndb-examples.sock')

const outbox = ndb.model('outbox', {
  id: 'event_id',
  time: 'created_at',
  fields: {
    event_id: { type: 'string' },
    topic: { type: 'string', index: true },
    created_at: { type: 'date' },
  },
})

const receipts = ndb.model('receipts', {
  id: 'event_id',
  fields: {
    event_id: { type: 'string' },
    done_at: { type: 'date' },
  },
})

// #region work
// The work of the consumer. The receipt id is the event id, so a second delivery of
// the same event fails with duplicate_id and the work is not done twice.
async function charge(eventId: string): Promise<boolean> {
  try {
    await receipts.insert({ event_id: eventId, done_at: new Date() })

    return true
  } catch (err) {
    if (err instanceof DuplicateIdError) {
      return false
    }

    throw err
  }
}
// #endregion work

// Make the example repeatable: remove what an earlier run left.
for (let i = 1; i <= 5; i++) {
  for (const model of [outbox, receipts]) {
    await model.delete(`evt-${i}`).catch((err: unknown) => {
      if (!(err instanceof NotFoundError)) {
        throw err
      }
    })
  }
}

// #region produce
const topics = ['invoice.paid', 'user.signup', 'invoice.paid', 'user.signup', 'invoice.paid']

for (const [i, topic] of topics.entries()) {
  const id = `evt-${i + 1}`

  await outbox.insert({ event_id: id, topic, created_at: new Date() })

  console.log(`produce ${id} ${topic}`)
}
// #endregion produce

// #region consume
const paid = { topic: 'invoice.paid' }

console.log('consumer billing, run 1 (where topic = invoice.paid):')

for await (const ev of outbox.watch('billing', { where: paid, limit: 2 })) {
  await charge(ev.id)

  if (ev.id === 'evt-3') {
    console.log('  evt-3 charged, crash before ack')

    break
  }

  await outbox.ack(ev)

  console.log(`  ${ev.id} charged, acked`)
}
// #endregion consume

// #region restart
console.log('consumer billing, run 2 (restart, resumes after the last ack):')

for await (const ev of outbox.watch('billing', { where: paid, limit: 2 })) {
  if (await charge(ev.id)) {
    console.log(`  ${ev.id} charged, acked`)
  } else {
    console.log(`  ${ev.id} delivered again, receipt exists, skipped, acked`)
  }

  await outbox.ack(ev)
}
// #endregion restart

console.log(`receipts: ${await receipts.count()} (one per invoice, none twice)`)

await ndb.close()
