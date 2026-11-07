# frozen_string_literal: true

# CRUD on the users collection: insert, get by _id, update with a _v guard, delete, count.
require "ndb"

# region connect
ndb = Ndb.connect(ENV.fetch("NDB_URL", "unix:///tmp/ndb-examples.sock")) # NDB_TOKEN is read by the SDK

users = ndb.collection("users", indexes: ["plan"], unique: ["email"])
# endregion connect

%w[u1 u2].each { |id| users.delete(id) }

# region insert
results = users.insert([
  { _id: "u1", email: "ada@example.com", plan: "free" },
  { _id: "u2", email: "grace@example.com", plan: "free" }
])

puts "insert u1, u2 -> v#{results[0]['_v']}, v#{results[1]['_v']}"
# endregion insert

# region get
ada = users.get("u1")

puts "get u1: #{ada['email']} plan=#{ada['plan']} v#{ada['_v']}"
# endregion get

# region update
saved = users.update("u1", { "$set" => { plan: "pro" } }, guard: { _v: ada["_v"] })

puts "update u1 plan=pro (_v #{ada['_v']}) -> v#{saved['doc']['_v']}"
# endregion update

# region conflict
stale = users.update("u1", { "$set" => { plan: "team" } }, guard: { _v: ada["_v"] })

if stale["matched"].zero?
  puts "update u1 plan=team (_v #{ada['_v']}) -> matched 0"

  fresh = users.get("u1")
  retried = users.update("u1", { "$set" => { plan: "team" } }, guard: { _v: fresh["_v"] })

  puts "reload u1: v#{fresh['_v']}, retry with _v #{fresh['_v']} -> v#{retried['doc']['_v']}"
end
# endregion conflict

# region delete
users.delete("u2")

puts "delete u2"
# endregion delete

# region count
total = users.count
on_team = users.count(plan: "team")

puts "count: #{total} user(s), #{on_team} on plan team"
# endregion count
