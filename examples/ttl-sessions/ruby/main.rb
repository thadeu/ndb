# frozen_string_literal: true

# Expiry like Redis: expire_in on insert, ttl, expire, persist. An expired document is hidden at once.
require "ndb"
require "time"

ndb = Ndb.connect(ENV.fetch("NDB_URL", "unix:///tmp/ndb-examples.sock"))

# region declare
sessions = ndb.collection("sessions", time: "created_at", expires_field: "expires_at")
# endregion declare

%w[s-short s-long s-keep].each { |id| sessions.delete(id) }

# region insert
now = Time.now.utc.iso8601(3)

sessions.insert({ _id: "s-short", created_at: now }, expire_in: 2)
sessions.insert([{ _id: "s-long", created_at: now }, { _id: "s-keep", created_at: now }], expire_in: "1h")

puts "insert s-short (expire_in 2s), s-long and s-keep (expire_in 1h)"
# endregion insert

# region ttl
puts "ttl s-short: #{sessions.ttl('s-short')}s, s-long: #{sessions.ttl('s-long')}s"
# endregion ttl

# region extend
sessions.expire("s-long", in: "30m")

puts "expire s-long 30m -> ttl #{sessions.ttl('s-long')}s"
# endregion extend

# region persist
sessions.persist("s-keep")

puts "persist s-keep -> ttl -1 (never expires)" if sessions.ttl("s-keep") == -1
# endregion persist

puts "waiting 3s..."
sleep 3

# region after
alive = sessions.query.all

puts "live sessions: #{alive.size} (#{alive.map { |s| s['_id'] }.join(', ')})"

puts "ttl s-short: gone" if sessions.ttl("s-short").nil?
# endregion after
