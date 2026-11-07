# frozen_string_literal: true

# A tiny sample app: every 5 seconds it records one visit in the shared
# "visits" collection and prints how many visits each app has.
# NDB_URL (default unix:///run/ndb/ndb.sock, or tcp://host:port) sets the daemon;
# NDB_TOKEN is the bearer token the SDK sends on TCP. With
# NDB_DIRECT_DIR set it appends the visit to a file instead (direct mode).

require "json"
require "securerandom"
require "socket"
require "ndb"

$stdout.sync = true

APP = "ruby"
POD = Socket.gethostname

ndb = Ndb.connect(ENV.fetch("NDB_URL", "unix:///run/ndb/ndb.sock"))

visits = ndb.collection("visits", indexes: ["app"], time: "ts")

def record(visits)
  dir = ENV["NDB_DIRECT_DIR"]
  id = "#{APP}-#{POD}-#{SecureRandom.hex(6)}"
  ts = Time.now.utc

  if dir.nil? || dir.empty?
    visits.insert(_id: id, app: APP, pod: POD, ts: ts.iso8601(6))
  else
    line = { _id: id, app: APP, pod: POD, ts: ts.iso8601(6) }.to_json

    File.open(File.join(dir, "#{APP}-#{POD}.ndjson"), "a") { |f| f.write("#{line}\n") }
  end
end

loop do
  begin
    record(visits)

    puts "#{APP} visits: #{visits.count(app: APP)}, all apps: #{visits.count}"
  rescue Ndb::Error, SystemCallError => e
    puts "waiting for ndb: #{e.message}"
  end

  sleep 5
end
