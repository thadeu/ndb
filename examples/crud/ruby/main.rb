# frozen_string_literal: true

# CRUD on the users collection: insert, get by _id, update with a _v guard, delete, count.
require "ndb"

# region connect
ndb = Ndb::Client.new(url: ENV.fetch("NDB_URL", "unix:///tmp/ndb-examples.sock")) # NDB_TOKEN is read by the SDK

ndb.ensure_collection("users", indexes: ["plan"], unique: ["email"])
# endregion connect

%w[u1 u2].each { |id| ndb.delete("users", id) }

# region insert
results = ndb.insert("users", [
  { _id: "u1", email: "ada@example.com", plan: "free" },
  { _id: "u2", email: "grace@example.com", plan: "free" }
])

puts "insert u1, u2 -> v#{results[0]['v']}, v#{results[1]['v']}"
# endregion insert

# region get
ada = ndb.get("users", "u1")

puts "get u1: #{ada['email']} plan=#{ada['plan']} v#{ada['_v']}"
# endregion get

# region update
saved = ndb.update("users", { _id: "u1", _v: ada["_v"] }, { "$set" => { plan: "pro" } })

puts "update u1 plan=pro (_v #{ada['_v']}) -> v#{saved['doc']['_v']}"
# endregion update

# region conflict
stale = ndb.update("users", { _id: "u1", _v: ada["_v"] }, { "$set" => { plan: "team" } })

if stale["matched"].zero?
  puts "update u1 plan=team (_v #{ada['_v']}) -> matched 0"

  fresh = ndb.get("users", "u1")
  retried = ndb.update("users", { _id: "u1", _v: fresh["_v"] }, { "$set" => { plan: "team" } })

  puts "reload u1: v#{fresh['_v']}, retry with _v #{fresh['_v']} -> v#{retried['doc']['_v']}"
end
# endregion conflict

# region delete
ndb.delete("users", "u2")

puts "delete u2"
# endregion delete

# region count
total = ndb.find("users", count: true, limit: 1).total
on_team = ndb.find("users", where: { plan: "team" }, count: true, limit: 1).total

puts "count: #{total} user(s), #{on_team} on plan team"
# endregion count
