# frozen_string_literal: true

# Outbox: a producer inserts events, a consumer watches them with a filter and acks after the work.
# Delivery is at least once, so the work must be safe to repeat: a receipt per event id does that.
require "ndb"
require "time"

ndb = Ndb.connect(ENV.fetch("NDB_URL", "unix:///tmp/ndb-examples.sock"))

outbox = ndb.collection("outbox", time: "created_at", indexes: ["topic"])
receipts = ndb.collection("receipts")

# region work
# The work of the consumer. The receipt id is the event id, so a second delivery of
# the same event fails with duplicate_id and the work is not done twice.
def charge(receipts, event_id)
  receipts.insert(_id: event_id, done_at: Time.now.utc.iso8601(3))

  true
rescue Ndb::UniqueViolation
  false
end
# endregion work

# Make the example repeatable: remove what an earlier run left.
(1..5).each do |i|
  [outbox, receipts].each { |coll| coll.delete("evt-#{i}") }
end

# region produce
topics = %w[invoice.paid user.signup invoice.paid user.signup invoice.paid]

topics.each_with_index do |topic, i|
  id = "evt-#{i + 1}"

  outbox.insert(_id: id, topic: topic, created_at: Time.now.utc.iso8601(3))

  puts "produce #{id} #{topic}"
end
# endregion produce

# region consume
paid = { topic: "invoice.paid" }

puts "consumer billing, run 1 (query topic = invoice.paid):"

outbox.watch("billing", query: paid, limit: 2) do |ev|
  charge(receipts, ev["_id"])

  if ev["_id"] == "evt-3"
    puts "  evt-3 charged, crash before ack"

    break
  end

  outbox.ack(ev, "billing")

  puts "  #{ev["_id"]} charged, acked"
end
# endregion consume

# region restart
puts "consumer billing, run 2 (restart, resumes after the last ack):"

outbox.watch("billing", query: paid, limit: 2) do |ev|
  if charge(receipts, ev["_id"])
    puts "  #{ev["_id"]} charged, acked"
  else
    puts "  #{ev["_id"]} delivered again, receipt exists, skipped, acked"
  end

  outbox.ack(ev, "billing")
end
# endregion restart

puts "receipts: #{receipts.count} (one per invoice, none twice)"
