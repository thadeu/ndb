# frozen_string_literal: true

# Outbox: a producer inserts events, a consumer watches them with a filter and acks after the work.
# Delivery is at least once, so the work must be safe to repeat: a receipt per event id does that.
require "ndb"
require "time"

ndb = Ndb::Client.new(url: ENV.fetch("NDB_URL", "unix:///tmp/ndb-examples.sock"))

ndb.ensure_collection("outbox", time: "created_at", indexes: ["topic"])
ndb.ensure_collection("receipts")

# region work
# The work of the consumer. The receipt id is the event id, so a second delivery of
# the same event fails with duplicate_id and the work is not done twice.
def charge(ndb, event_id)
  ndb.insert("receipts", [{ _id: event_id, done_at: Time.now.utc.iso8601(3) }])

  true
rescue Ndb::UniqueViolation
  false
end
# endregion work

# Make the example repeatable: remove what an earlier run left.
(1..5).each do |i|
  %w[outbox receipts].each do |coll|
    ndb.delete(coll, "evt-#{i}")
  end
end

# region produce
topics = %w[invoice.paid user.signup invoice.paid user.signup invoice.paid]

topics.each_with_index do |topic, i|
  id = "evt-#{i + 1}"

  ndb.insert("outbox", [{ _id: id, topic: topic, created_at: Time.now.utc.iso8601(3) }])

  puts "produce #{id} #{topic}"
end
# endregion produce

# region consume
paid = { topic: "invoice.paid" }

puts "consumer billing, run 1 (where topic = invoice.paid):"

ndb.watch("outbox", "billing", where: paid, limit: 2) do |ev|
  charge(ndb, ev["_id"])

  if ev["_id"] == "evt-3"
    puts "  evt-3 charged, crash before ack"

    break
  end

  ndb.ack("outbox", "billing", ev["pos"])

  puts "  #{ev["_id"]} charged, acked"
end
# endregion consume

# region restart
puts "consumer billing, run 2 (restart, resumes after the last ack):"

ndb.watch("outbox", "billing", where: paid, limit: 2) do |ev|
  if charge(ndb, ev["_id"])
    puts "  #{ev["_id"]} charged, acked"
  else
    puts "  #{ev["_id"]} delivered again, receipt exists, skipped, acked"
  end

  ndb.ack("outbox", "billing", ev["pos"])
end
# endregion restart

puts "receipts: #{ndb.find('receipts', count: true, limit: 1).total} (one per invoice, none twice)"
