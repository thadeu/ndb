# frozen_string_literal: true

require "test_helper"

begin
  require "redis"
rescue LoadError
  nil
end

# The x* commands, with the answers that redis-rb gives. Every test here runs
# on ndb (RedisCommandsTest) and, when NDB_REDIS_ADDR is set and the redis gem
# loads, on a real Redis (RedisParityTest): the same assertions prove that the
# shapes are equal. Each test uses its own keys; no test flushes the server.
module RedisCommandCases
  ID = /\A\d+-\d+\z/

  def test_xadd
    key = new_key
    id = backend.xadd(key, { order: 1, total: 9.9, note: "new" })

    assert_match ID, id
    assert_equal "5-1", backend.xadd(new_key, { "a" => "b" }, id: "5-1")
    assert_equal [[id, { "order" => "1", "total" => "9.9", "note" => "new" }]], backend.xrange(key)

    backend.xadd(key, { order: 2 })
    backend.xadd(key, { order: 3 }, maxlen: 2)

    assert_equal 2, backend.xlen(key)

    old = new_key

    backend.xadd(old, { n: 1 }, id: "7-0")
    backend.xadd(old, { n: 2 }, id: "9-0", minid: "8-0")

    assert_equal ["9-0"], backend.xrange(old).map(&:first)

    assert_match ID, backend.xadd(key, { order: 4 }, maxlen: 1000, approximate: true)
    assert_match ID, backend.xadd(key, { order: 5 }, nomkstream: true)
    assert_nil backend.xadd(new_key, { order: 1 }, nomkstream: true)

    assert_raises(ArgumentError) { backend.xadd(key, { order: 6 }, maxlen: 1, minid: "1-0") }
  end

  def test_xlen
    key = new_key

    assert_equal 0, backend.xlen(key)

    add(key, 3)

    assert_equal 3, backend.xlen(key)
  end

  def test_xrange_and_xrevrange
    key = new_key
    ids = add(key, 4)

    assert_empty backend.xrange(new_key)
    assert_empty backend.xrevrange(new_key)

    assert_equal ids, backend.xrange(key).map(&:first)
    assert_equal [ids[0], { "n" => "1" }], backend.xrange(key).first
    assert_equal ids[1..2], backend.xrange(key, ids[1], ids[2]).map(&:first)
    assert_equal ids[1..], backend.xrange(key, ids[1]).map(&:first)
    assert_equal ids[0..1], backend.xrange(key, "-", "+", count: 2).map(&:first)
    assert_equal ids[1..2], backend.xrange(key, "(#{ids[0]}", "(#{ids[3]}").map(&:first)

    assert_equal ids.reverse, backend.xrevrange(key).map(&:first)
    assert_equal [ids[3], { "n" => "4" }], backend.xrevrange(key).first
    assert_equal [ids[2], ids[1]], backend.xrevrange(key, ids[2], ids[1]).map(&:first)
    assert_equal [ids[3]], backend.xrevrange(key, "+", "-", count: 1).map(&:first)
  end

  def test_xread
    one = new_key
    two = new_key
    ids = add(one, 3)
    other = add(two, 1)

    assert_equal({}, backend.xread(new_key, "0-0"))
    assert_equal({}, backend.xread(one, "$"))
    assert_equal({}, backend.xread(one, ids.last))

    assert_equal({ one => [[ids[0], { "n" => "1" }], [ids[1], { "n" => "2" }]] }, backend.xread(one, "0-0", count: 2))
    assert_equal({ one => [[ids[2], { "n" => "3" }]] }, backend.xread([one], [ids[1]]))

    many = backend.xread([one, two, new_key], [ids[1], "0", "0"])

    assert_equal [one, two], many.keys
    assert_equal [[other[0], { "n" => "1" }]], many[two]
  end

  def test_xread_with_block
    key = new_key

    add(key, 1)

    started = now

    assert_equal({}, backend.xread(key, "$", block: 200))
    assert_operator now - started, :>=, 0.15

    writer = Thread.new do
      sleep 0.2

      writer_backend.xadd(key, { n: 2 })
    end

    got = backend.xread(key, "$", block: 5000)
    id = writer.value

    assert_equal({ key => [[id, { "n" => "2" }]] }, got)
  end

  def test_xdel
    key = new_key
    ids = add(key, 4)

    assert_equal 1, backend.xdel(key, ids[0])
    assert_equal 2, backend.xdel(key, ids[1], ids[2])
    assert_equal 1, backend.xdel(key, [ids[3], ids[0]])
    assert_equal 0, backend.xdel(new_key, "1-1")
    assert_equal 0, backend.xlen(key)
  end

  def test_xtrim
    key = new_key
    ids = add(key, 5)

    assert_equal 2, backend.xtrim(key, 3)
    assert_equal 0, backend.xtrim(key, 3, approximate: true)
    assert_equal 1, backend.xtrim(key, ids[3], strategy: "MINID")
    assert_equal 1, backend.xtrim(key, 1, strategy: :maxlen)
    assert_equal 0, backend.xtrim(new_key, 1)
    assert_equal [ids[4]], backend.xrange(key).map(&:first)
  end

  def test_xgroup
    key = new_key

    assert_equal "OK", backend.xgroup(:create, key, "g", "$", mkstream: true)

    busy = assert_raises(error_class) { backend.xgroup(:create, key, "g", "$", mkstream: true) }

    assert_match(/\ABUSYGROUP/, busy.message)

    no_key = assert_raises(error_class) { backend.xgroup(:create, new_key, "g", "$") }

    assert_match(/requires the key to exist/, no_key.message)

    assert_equal "OK", backend.xgroup("create", key, "g2", "0")
    assert_equal "OK", backend.xgroup(:setid, key, "g", "0")

    no_group = assert_raises(error_class) { backend.xgroup(:setid, key, "nope", "0") }

    assert_match(/\ANOGROUP/, no_group.message)

    assert_equal 1, backend.xgroup(:createconsumer, key, "g", "w1")
    assert_equal 0, backend.xgroup(:createconsumer, key, "g", "w1")

    add(key, 2)
    backend.xreadgroup("g", "w1", key, ">")

    assert_equal 2, backend.xgroup(:delconsumer, key, "g", "w1")
    assert_equal 0, backend.xgroup(:delconsumer, key, "g", "w1")

    assert_equal 1, backend.xgroup(:destroy, key, "g")
    assert_equal 0, backend.xgroup(:destroy, key, "g")
  end

  def test_xreadgroup
    key = new_key
    ids = add(key, 3)

    backend.xgroup(:create, key, "g", "0")

    assert_equal({ key => [] }, backend.xreadgroup("g", "w1", key, "0"))

    got = backend.xreadgroup("g", "w1", key, ">", count: 2)

    assert_equal({ key => [[ids[0], { "n" => "1" }], [ids[1], { "n" => "2" }]] }, got)

    assert_equal got, backend.xreadgroup("g", "w1", [key], ["0"])
    assert_equal({ key => [[ids[1], { "n" => "2" }]] }, backend.xreadgroup("g", "w1", key, ids[0]))
    assert_equal({ key => [[ids[0], { "n" => "1" }]] }, backend.xreadgroup("g", "w1", key, "0", count: 1))

    assert_equal [ids[2]], backend.xreadgroup("g", "w2", key, ">", noack: true)[key].map(&:first)
    assert_equal 2, backend.xpending(key, "g")["size"]

    assert_equal({}, backend.xreadgroup("g", "w1", key, ">"))
    assert_equal({}, backend.xreadgroup("g", "w1", key, ">", block: 100))

    backend.xdel(key, ids[0])

    assert_equal [ids[0], nil], backend.xreadgroup("g", "w1", key, "0")[key].first

    no_group = assert_raises(error_class) { backend.xreadgroup("nope", "w1", key, ">") }

    assert_match(/\ANOGROUP/, no_group.message)

    no_key = assert_raises(error_class) { backend.xreadgroup("g", "w1", new_key, ">") }

    assert_match(/\ANOGROUP/, no_key.message)
  end

  def test_xreadgroup_with_many_streams
    one = new_key
    two = new_key
    first = add(one, 1)
    second = add(two, 1)

    backend.xgroup(:create, one, "g", "0")
    backend.xgroup(:create, two, "g", "0")

    got = backend.xreadgroup("g", "w1", [one, two], [">", ">"])

    assert_equal({ one => [[first[0], { "n" => "1" }]], two => [[second[0], { "n" => "1" }]] }, got)
  end

  def test_xreadgroup_with_block
    key = new_key

    backend.xgroup(:create, key, "g", "$", mkstream: true)

    writer = Thread.new do
      sleep 0.2

      writer_backend.xadd(key, { n: 1 })
    end

    got = backend.xreadgroup("g", "w1", key, ">", block: 5000)
    id = writer.value

    assert_equal({ key => [[id, { "n" => "1" }]] }, got)
  end

  def test_xack
    key = new_key
    ids = add(key, 3)

    backend.xgroup(:create, key, "g", "0")
    backend.xreadgroup("g", "w1", key, ">")

    assert_equal 1, backend.xack(key, "g", ids[0])
    assert_equal 0, backend.xack(key, "g", ids[0])
    assert_equal 2, backend.xack(key, "g", [ids[1], ids[2]])
    assert_equal 0, backend.xack(key, "nope", ids[0])
  end

  def test_xpending
    key = new_key
    ids = add(key, 3)

    backend.xgroup(:create, key, "g", "0")

    empty = { "size" => 0, "min_entry_id" => nil, "max_entry_id" => nil, "consumers" => {} }

    assert_equal empty, backend.xpending(key, "g")
    assert_empty backend.xpending(key, "g", "-", "+", 10)

    backend.xreadgroup("g", "w1", key, ">", count: 2)
    backend.xreadgroup("g", "w2", key, ">")

    summary = { "size" => 3, "min_entry_id" => ids[0], "max_entry_id" => ids[2], "consumers" => { "w1" => "2", "w2" => "1" } }

    assert_equal summary, backend.xpending(key, "g")

    list = backend.xpending(key, "g", "-", "+", 10)

    assert_equal ids, list.map { |row| row["entry_id"] }
    assert_equal %w[w1 w1 w2], list.map { |row| row["consumer"] }
    assert_equal %w[consumer count elapsed entry_id], list.first.keys.sort
    assert_equal 1, list.first["count"]
    assert_kind_of Integer, list.first["elapsed"]

    assert_equal [ids[0]], backend.xpending(key, "g", "-", "+", 1).map { |row| row["entry_id"] }
    assert_equal [ids[2]], backend.xpending(key, "g", "-", "+", 10, "w2").map { |row| row["entry_id"] }
    assert_equal [ids[1]], backend.xpending(key, "g", ids[1], ids[1], 10).map { |row| row["entry_id"] }
    assert_empty backend.xpending(key, "g", "-", "+", 10, idle: 60_000)
    assert_equal 3, backend.xpending(key, "g", "-", "+", 10, idle: 0).size

    assert_raises(ArgumentError) { backend.xpending(key, "g", "-", "+") }

    no_group = assert_raises(error_class) { backend.xpending(key, "nope") }

    assert_match(/\ANOGROUP/, no_group.message)
  end

  def test_xclaim
    key = new_key
    ids = add(key, 3)

    backend.xgroup(:create, key, "g", "0")
    backend.xreadgroup("g", "w1", key, ">", count: 2)

    assert_equal [[ids[0], { "n" => "1" }]], backend.xclaim(key, "g", "w2", 0, ids[0])
    assert_equal [ids[0], ids[1]], backend.xclaim(key, "g", "w3", 0, [ids[0], ids[1]], justid: true)
    assert_empty backend.xclaim(key, "g", "w4", 60_000, ids[0], ids[1])
    assert_empty backend.xclaim(key, "g", "w4", 0, ids[2])

    assert_equal [ids[2]], backend.xclaim(key, "g", "w4", 0, ids[2], force: true, justid: true)
    assert_equal [ids[0]], backend.xclaim(key, "g", "w4", 0, ids[0], retrycount: 7, idle: 90_000).map(&:first)

    row = backend.xpending(key, "g", ids[0], ids[0], 1).first

    assert_equal "w4", row["consumer"]
    assert_equal 7, row["count"]
    assert_operator row["elapsed"], :>=, 90_000

    no_group = assert_raises(error_class) { backend.xclaim(key, "nope", "w1", 0, ids[0]) }

    assert_match(/\ANOGROUP/, no_group.message)
  end

  def test_xautoclaim
    key = new_key
    ids = add(key, 3)

    backend.xgroup(:create, key, "g", "0")
    backend.xreadgroup("g", "w1", key, ">")

    assert_equal({ "next" => "0-0", "entries" => [] }, backend.xautoclaim(key, "g", "w2", 60_000, "0-0"))

    first = backend.xautoclaim(key, "g", "w2", 0, "0-0", count: 2)

    assert_equal ids[2], first["next"]
    assert_equal [[ids[0], { "n" => "1" }], [ids[1], { "n" => "2" }]], first["entries"]

    rest = backend.xautoclaim(key, "g", "w2", 0, first["next"], justid: true)

    assert_equal({ "next" => "0-0", "entries" => [ids[2]] }, rest)
    assert_equal({ "w2" => "3" }, backend.xpending(key, "g")["consumers"])

    no_group = assert_raises(error_class) { backend.xautoclaim(key, "nope", "w1", 0, "0-0") }

    assert_match(/\ANOGROUP/, no_group.message)
  end

  def test_xinfo_stream
    key = new_key

    backend.xgroup(:create, key, "g", "$", mkstream: true)

    empty = backend.xinfo(:stream, key)

    assert_equal 0, empty["length"]
    assert_equal 1, empty["groups"]
    assert_equal "0-0", empty["last-generated-id"]
    assert_nil empty["first-entry"]
    assert_nil empty["last-entry"]

    ids = add(key, 3)

    backend.xdel(key, ids[0])

    info = backend.xinfo("stream", key)

    assert_equal 2, info["length"]
    assert_equal 3, info["entries-added"]
    assert_equal ids[2], info["last-generated-id"]
    assert_equal ids[0], info["max-deleted-entry-id"]
    assert_equal ids[1], info["recorded-first-entry-id"]
    assert_equal [ids[1], %w[n 2]], info["first-entry"]
    assert_equal [ids[2], %w[n 3]], info["last-entry"]

    no_key = assert_raises(error_class) { backend.xinfo(:stream, new_key) }

    assert_match(/no such key/, no_key.message)
  end

  def test_xinfo_groups_and_consumers
    key = new_key
    ids = add(key, 3)

    assert_empty backend.xinfo(:groups, key)

    backend.xgroup(:create, key, "g", "0")
    backend.xreadgroup("g", "w1", key, ">", count: 2)

    group = backend.xinfo(:groups, key).first
    expected = { "name" => "g", "consumers" => 1, "pending" => 2, "last-delivered-id" => ids[1], "entries-read" => 2, "lag" => 1 }

    assert_equal expected, group.slice(*expected.keys)

    consumer = backend.xinfo(:consumers, key, "g").first

    assert_equal %w[idle inactive name pending], consumer.keys.sort
    assert_equal "w1", consumer["name"]
    assert_equal 2, consumer["pending"]
    assert_kind_of Integer, consumer["idle"]
    assert_kind_of Integer, consumer["inactive"]

    no_group = assert_raises(error_class) { backend.xinfo(:consumers, key, "nope") }

    assert_match(/\ANOGROUP/, no_group.message)
  end

  private

  # Appends count entries { n: 1 }, { n: 2 }, ... and returns their IDs.
  def add(key, count)
    (1..count).map { |n| backend.xadd(key, { n: n }) }
  end

  def now = Process.clock_gettime(Process::CLOCK_MONOTONIC)
end

class RedisCommandsTest < Minitest::Test
  include RedisCommandCases

  def backend = client

  def writer_backend = client

  def error_class = Ndb::Error

  def new_key = NdbTestHelpers.next_name

  def test_errors_stay_ndb_errors
    key = new_key

    client.xgroup(:create, key, "g", "$", mkstream: true)

    busy = assert_raises(Ndb::GroupExists) { client.xgroup(:create, key, "g", "$") }

    assert_equal "group_exists", busy.code
    assert_equal 409, busy.status

    assert_raises(Ndb::UnknownGroup) { client.xreadgroup("nope", "w1", key, ">") }
    assert_raises(Ndb::UnknownCollection) { client.xgroup(:create, new_key, "g", "$") }
  end

  def test_values_come_back_as_strings
    key = new_key
    doc = { name: "ana", count: 2, price: 9.5, paid: true, note: nil, tags: %w[a b], address: { city: "Recife" } }

    id = client.xadd(key, doc)

    fields = {
      "name" => "ana",
      "count" => "2",
      "price" => "9.5",
      "paid" => "true",
      "note" => "",
      "tags" => '["a","b"]',
      "address" => '{"city":"Recife"}'
    }

    assert_equal [[id, fields]], client.xrange(key)
    assert_equal({ key => [[id, fields]] }, client.xread(key, "0"))

    stored = client.stream(key).range.first.doc

    assert_equal 2, stored["count"]
    assert_equal({ "city" => "Recife" }, stored["address"])
  end

  def test_block_with_many_streams_is_not_supported
    error = assert_raises(Ndb::NotSupported) { client.xread(%w[a b], %w[0 0], block: 100) }

    assert_match(/one stream/, error.message)
    assert_raises(Ndb::NotSupported) { client.xreadgroup("g", "w1", %w[a b], %w[> >], block: 100) }
    assert_raises(ArgumentError) { client.xread(%w[a b], ["0"]) }
  end

  def test_xclaim_options_that_ndb_does_not_have
    assert_raises(Ndb::NotSupported) { client.xclaim("s", "g", "w1", 0, "1-1", time: 1_700_000_000_000) }
    assert_raises(ArgumentError) { client.xclaim("s", "g", "w1", 0, "1-1", lastid: "1-1") }
  end

  def test_unknown_subcommands
    assert_raises(ArgumentError) { client.xgroup(:nope, "s", "g") }
    assert_raises(ArgumentError) { client.xinfo(:nope, "s") }
    assert_raises(ArgumentError) { client.xtrim("s", 1, strategy: "NOPE") }
  end

  def test_xread_block_waits_for_a_stream_that_does_not_exist
    key = new_key

    writer = Thread.new do
      sleep 0.2

      client.xadd(key, { n: 1 })
    end

    got = client.xread(key, "$", block: 5000)

    assert_equal({ key => [[writer.value, { "n" => "1" }]] }, got)
  end

  # A wait with no limit reads in FOREVER_STEP steps. "$" is the last ID
  # before the first step, so an entry added between two steps is given.
  def test_xread_dollar_with_block_zero_keeps_an_entry_added_between_steps
    key = new_key
    other = client
    steps = 0
    reader = Ndb::Client.new(socket: TestDaemon.socket, timeout: 10)

    client.xadd(key, { seed: 1 })

    reader.define_singleton_method(:stream) do |name|
      stream = super(name)

      stream.define_singleton_method(:read) do |**options|
        out = super(**options)
        steps += 1

        other.xadd(name, { in_gap: 1 }) if steps == 1 && out.empty?

        out
      end

      stream
    end

    later = Thread.new do
      sleep 1

      other.xadd(key, { later: 1 })
    end

    got = with_forever_step(0.2) { reader.xread(key, "$", block: 0) }

    later.join

    assert_equal [{ "in_gap" => "1" }], got[key].map(&:last)
  end

  def test_block_zero_waits_with_no_limit
    key = new_key

    client.xgroup(:create, key, "g", "$", mkstream: true)

    writer = Thread.new do
      sleep 0.2

      client.xadd(key, { n: 1 })
    end

    got = client.xreadgroup("g", "w1", key, ">", block: 0)

    assert_equal [writer.value], got[key].map(&:first)
  end

  private

  # Runs the block with a shorter FOREVER_STEP, then sets the old one back.
  def with_forever_step(seconds)
    old = Ndb::RedisCommands::FOREVER_STEP

    Ndb::RedisCommands.send(:remove_const, :FOREVER_STEP)
    Ndb::RedisCommands.const_set(:FOREVER_STEP, seconds)

    yield
  ensure
    Ndb::RedisCommands.send(:remove_const, :FOREVER_STEP)
    Ndb::RedisCommands.const_set(:FOREVER_STEP, old)
  end
end

# The same cases on a real Redis, to prove that the shapes are equal. It runs
# only with NDB_REDIS_ADDR (for example redis://127.0.0.1:6391) and the redis
# gem. It deletes only the keys that it made.
class RedisParityTest < Minitest::Test
  include RedisCommandCases

  def setup
    skip "set NDB_REDIS_ADDR to run the cases on a real Redis" if ENV["NDB_REDIS_ADDR"].to_s.empty?
    skip "the redis gem is not installed" unless defined?(::Redis)

    @keys = []
  end

  def teardown
    return if @keys.nil? || @keys.empty?

    backend.del(*@keys)
    backend.close
    @writer&.close
  end

  def backend
    @backend ||= ::Redis.new(url: ENV["NDB_REDIS_ADDR"])
  end

  def writer_backend
    @writer ||= ::Redis.new(url: ENV["NDB_REDIS_ADDR"])
  end

  def error_class = ::Redis::CommandError

  def new_key
    key = "ndb_parity_#{NdbTestHelpers.next_name}"

    @keys << key

    key
  end
end
