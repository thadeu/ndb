# frozen_string_literal: true

# Direct mode: other processes append NDJSON lines to files, ndb only reads them.
# The collection is declared in ndb.yml, so this client passes no definition.
require "json"
require "ndb"

ndb = Ndb.connect(ENV.fetch("NDB_URL", "unix:///tmp/ndb-examples.sock"))

logs = ndb.collection("app_logs")

HISTORY = { "ts" => { "$lt" => "2026-11-02T00:00:00Z" } }.freeze

# The daemon sees a new line within a moment (file events, with a 5 s poll as the fallback).
def wait_for(timeout: 15)
  deadline = Time.now + timeout

  until yield
    raise "timeout waiting for the daemon to read the files" if Time.now > deadline

    sleep 0.2
  end
end

wait_for { logs.count(HISTORY) == 12 }

puts "history: 12 lines from 2 files"

# region filter
failures = logs.query(HISTORY).query(level: "error").sort(ts: -1).all

puts "errors, newest first:"

failures.each do |l|
  puts format("  %s %-6s %s", l["ts"][11, 8], l["app"], l["msg"])
end

slow = logs.query(HISTORY).query(ms: 800..).count

puts "slow (ms >= 800): #{slow}"
# endregion filter

# region regex
payments = logs.query(HISTORY).query(path: { "$regex" => "^/api/v[0-9]+/payments" }).count

puts "payment paths ($regex): #{payments}"
# endregion regex

# region cursor
pages = []
query = logs.query(HISTORY).sort(ts: 1).limit(5)

loop do
  page = query.page

  pages << page.rows.size

  break if page.next.nil?

  query = query.after(page.next)
end

puts "pages (limit 5): #{pages.join(", ")}"
# endregion cursor

# region append
msg = "hello from ruby #{(Time.now.to_f * 1000).to_i}"
line = { ts: Time.now.utc.strftime("%Y-%m-%dT%H:%M:%SZ"), app: "cli", level: "info", msg: msg }

system(File.expand_path("../append.sh", __dir__), "cli", "3003", JSON.generate(line), exception: true)

puts "append: cli wrote one line"

wait_for { logs.count(msg: msg) == 1 }

puts "the new line is in the collection"

added = logs.query(msg: msg).first

puts "it has no _id, so it has no _doc/{id} routes: _id is #{added['_id'].to_s.inspect}"
# endregion append
