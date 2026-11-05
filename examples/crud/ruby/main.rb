# frozen_string_literal: true

# CRUD on the users collection: insert, get, update with if_version, delete, count.
require "ndb"

# region connect
ndb = Ndb::Client.new(url: ENV.fetch("NDB_URL", "unix:///tmp/ndb-examples.sock")) # NDB_TOKEN is read by the SDK

ndb.ensure_collection("users", id: "user_id", indexes: ["plan"])
# endregion connect

%w[u1 u2].each do |id|
  ndb.delete("users", id)
rescue Ndb::NotFound
  nil
end

# region insert
results = ndb.insert("users", [
  { user_id: "u1", email: "ada@example.com", plan: "free" },
  { user_id: "u2", email: "grace@example.com", plan: "free" }
])

puts "insert u1, u2 -> v#{results[0]['v']}, v#{results[1]['v']}"
# endregion insert

# region get
ada = ndb.find("users", where: { user_id: "u1" }, versions: true).rows.first

puts "get u1: #{ada['email']} plan=#{ada['plan']} v#{ada['_v']}"
# endregion get

# region update
saved = ndb.update("users", "u1", set: { plan: "pro" }, if_version: ada["_v"])

puts "update u1 plan=pro (if_version #{ada['_v']}) -> v#{saved['v']}"
# endregion update

# region conflict
begin
  ndb.update("users", "u1", set: { plan: "team" }, if_version: ada["_v"])
rescue Ndb::VersionConflict
  puts "update u1 plan=team (if_version #{ada['_v']}) -> version_conflict"

  fresh = ndb.find("users", where: { user_id: "u1" }, versions: true).rows.first
  saved = ndb.update("users", "u1", set: { plan: "team" }, if_version: fresh["_v"])

  puts "reload u1: v#{fresh['_v']}, retry with if_version #{fresh['_v']} -> v#{saved['v']}"
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
