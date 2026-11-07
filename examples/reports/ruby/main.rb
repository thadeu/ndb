# frozen_string_literal: true

# Reports with aggregate: $match, $group by hour with $dateTrunc, $sum, $avg, $sort, and a filtered count.
require "ndb"

ndb = Ndb.connect(ENV.fetch("NDB_URL", "unix:///tmp/ndb-examples.sock"))

orders = ndb.collection("orders", time: "created_at", indexes: ["status"])

# Seed the twelve orders once. A second run finds them and does nothing.
def seed(orders)
  return if orders.count.positive?

  rows = [
    ["o01", "09:05:00", "paid", 50], ["o02", "09:20:00", "paid", 70], ["o03", "09:45:00", "refunded", 30],
    ["o04", "10:02:00", "paid", 120], ["o05", "10:15:00", "paid", 80], ["o06", "10:30:00", "pending", 60],
    ["o07", "10:55:00", "paid", 160], ["o08", "11:05:00", "paid", 40], ["o09", "11:25:00", "refunded", 90],
    ["o10", "11:40:00", "paid", 60], ["o11", "11:50:00", "pending", 20], ["o12", "11:59:00", "paid", 110]
  ]

  orders.insert(rows.map do |id, clock, status, amount|
    { _id: id, created_at: "2026-11-01T#{clock}Z", status: status, amount: amount }
  end)
end

seed(orders)

puts "orders: #{orders.count}"

# region hourly
per_hour = orders.aggregate([
  { "$match" => { "status" => "paid" } },
  { "$group" => {
    "_id" => { "$dateTrunc" => { "date" => "$created_at", "unit" => "hour" } },
    "orders" => { "$count" => {} },
    "revenue" => { "$sum" => "$amount" },
    "avg_ticket" => { "$avg" => "$amount" }
  } },
  { "$sort" => { "revenue" => -1 } }
])

puts "paid orders per hour, best revenue first:"

per_hour.rows.each do |row|
  puts "  #{row['_id']} orders=#{row['orders']} revenue=#{row['revenue']} avg=#{row['avg_ticket']}"
end
# endregion hourly

# region by-status
by_status = orders.aggregate([
  { "$group" => {
    "_id" => "$status",
    "orders" => { "$count" => {} },
    "revenue" => { "$sum" => "$amount" }
  } },
  { "$sort" => { "_id" => 1 } }
])

puts "by status:"

by_status.rows.each do |row|
  puts format("  %-8s orders=%s revenue=%s", row["_id"], row["orders"], row["revenue"])
end
# endregion by-status

# region count
big = orders.count(status: "paid", amount: 100..)

puts "paid orders of 100 or more: #{big}"
# endregion count
