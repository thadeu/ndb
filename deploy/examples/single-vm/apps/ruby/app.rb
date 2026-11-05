# frozen_string_literal: true

# A tiny sample app: every 5 seconds it records one visit in the shared
# "visits" collection and prints how many visits each app has.
# NDB_URL (default unix:///run/ndb/ndb.sock) sets the daemon. With
# NDB_DIRECT_DIR set it appends the visit to a file instead (direct mode).

require "json"
require "securerandom"
require "socket"
require "ndb_record"

$stdout.sync = true

APP = "ruby"
POD = Socket.gethostname

url = ENV.fetch("NDB_URL", "unix:///run/ndb/ndb.sock")

abort "ndb: the Ruby SDK has no TCP transport yet (E13.6); use a unix:// URL" unless url.start_with?("unix://")

NdbRecord.configure { |c| c.socket = url.delete_prefix("unix://") }

class Visit < NdbRecord::Base
  self.collection_name = "visits"

  primary_key :id

  attribute :app, :string, index: true
  attribute :pod, :string
  attribute :ts, :datetime, time: true
end

def record
  dir = ENV["NDB_DIRECT_DIR"]
  id = "#{APP}-#{POD}-#{SecureRandom.hex(6)}"
  ts = Time.now.utc

  if dir.nil? || dir.empty?
    Visit.create!(id: id, app: APP, pod: POD, ts: ts)
  else
    line = { id: id, app: APP, pod: POD, ts: ts.iso8601(6) }.to_json

    File.open(File.join(dir, "#{APP}-#{POD}.ndjson"), "a") { |f| f.write("#{line}\n") }
  end
end

loop do
  begin
    record

    puts "#{APP} visits: #{Visit.where(app: APP).count}, all apps: #{Visit.count}"
  rescue NdbRecord::Error, SystemCallError => e
    puts "waiting for ndb: #{e.message}"
  end

  sleep 5
end
