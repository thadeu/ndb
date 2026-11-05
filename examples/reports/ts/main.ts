// Reports with aggregate: $match, $group by hour with $dateTrunc, $sum, $avg, $sort, and a filtered count.
import { connect } from '@thadeu/ndb'

const ndb = connect(process.env.NDB_URL ?? 'unix:///tmp/ndb-examples.sock')

const orders = ndb.model('orders', {
  id: 'order_id',
  time: 'created_at',
  fields: {
    order_id: { type: 'string' },
    created_at: { type: 'date' },
    status: { type: 'string', index: true },
    amount: { type: 'number' },
  },
})

// Seed the twelve orders once. A second run finds them and does nothing.
if ((await orders.count()) === 0) {
  const rows: [string, string, string, number][] = [
    ['o01', '09:05:00', 'paid', 50], ['o02', '09:20:00', 'paid', 70], ['o03', '09:45:00', 'refunded', 30],
    ['o04', '10:02:00', 'paid', 120], ['o05', '10:15:00', 'paid', 80], ['o06', '10:30:00', 'pending', 60],
    ['o07', '10:55:00', 'paid', 160], ['o08', '11:05:00', 'paid', 40], ['o09', '11:25:00', 'refunded', 90],
    ['o10', '11:40:00', 'paid', 60], ['o11', '11:50:00', 'pending', 20], ['o12', '11:59:00', 'paid', 110],
  ]

  await orders.insert(
    rows.map(([order_id, clock, status, amount]) => ({
      order_id,
      created_at: new Date(`2026-11-01T${clock}Z`),
      status,
      amount,
    })),
  )
}

console.log(`orders: ${await orders.count()}`)

// #region hourly
const perHour = await orders.aggregate([
  { $match: { status: 'paid' } },
  {
    $group: {
      _id: { $dateTrunc: { date: '$created_at', unit: 'hour' } },
      orders: { $count: {} },
      revenue: { $sum: '$amount' },
      avg_ticket: { $avg: '$amount' },
    },
  },
  { $sort: { revenue: -1 } },
])

console.log('paid orders per hour, best revenue first:')

for (const row of perHour.rows) {
  console.log(`  ${row._id} orders=${row.orders} revenue=${row.revenue} avg=${row.avg_ticket}`)
}
// #endregion hourly

// #region by-status
const byStatus = await orders.aggregate([
  { $group: { _id: '$status', orders: { $count: {} }, revenue: { $sum: '$amount' } } },
  { $sort: { _id: 1 } },
])

console.log('by status:')

for (const row of byStatus.rows) {
  console.log(`  ${String(row._id).padEnd(8)} orders=${row.orders} revenue=${row.revenue}`)
}
// #endregion by-status

// #region count
const big = await orders.where({ status: 'paid', amount: { $gte: 100 } }).count()

console.log(`paid orders of 100 or more: ${big}`)
// #endregion count

await ndb.close()
