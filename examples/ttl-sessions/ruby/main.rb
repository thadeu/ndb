# frozen_string_literal: true

# Expiry like Redis: expire_in on insert, ttl, expire, persist. An expired document is hidden at once.
require "ndb"
require "time"

ndb = Ndb::Client.new(url: ENV.fetch("NDB_URL", "unix:///tmp/ndb-examples.sock"))

# region declare
ndb.ensure_collection("sessions", time: "created_at", expires_field: "expires_at")
# endregion declare

%w[s-short s-long s-keep].each { |id| ndb.delete("sessions", id) }

# region insert
now = Time.now.utc.iso8601(3)

ndb.insert("sessions", [{ _id: "s-short", created_at: now }], expire_in: 2)
ndb.insert("sessions", [{ _id: "s-long", created_at: now }, { _id: "s-keep", created_at: now }], expire_in: "1h")

puts "insert s-short (expire_in 2s), s-long and s-keep (expire_in 1h)"
# endregion insert

# region ttl
puts "ttl s-short: #{ndb.ttl('sessions', 's-short')}s, s-long: #{ndb.ttl('sessions', 's-long')}s"
# endregion ttl

# region extend
ndb.expire("sessions", "s-long", in: "30m")

puts "expire s-long 30m -> ttl #{ndb.ttl('sessions', 's-long')}s"
# endregion extend

# region persist
ndb.persist("sessions", "s-keep")

puts "persist s-keep -> ttl -1 (never expires)" if ndb.ttl("sessions", "s-keep") == -1
# endregion persist

puts "waiting 3s..."
sleep 3

# region after
alive = ndb.find("sessions", {}).rows

puts "live sessions: #{alive.size} (#{alive.map { |s| s['_id'] }.join(', ')})"

puts "ttl s-short: gone" if ndb.ttl("sessions", "s-short").nil?
# endregion after
