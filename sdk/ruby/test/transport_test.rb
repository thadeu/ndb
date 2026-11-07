# frozen_string_literal: true

require "test_helper"

class TransportTest < Minitest::Test
  def setup
    @coll = NdbTestHelpers.next_name
  end

  def test_ensure_insert_find_roundtrip
    client.ensure_collection(@coll, indexes: ["a"])

    results = client.bulk(@coll, [{ _id: "1", a: "x" }, { _id: "2", a: "y" }])
    page = client.search(@coll, query: { a: "x" })

    assert_equal [{ "_id" => "1", "_v" => 1 }, { "_id" => "2", "_v" => 1 }], results
    assert_equal [{ "_id" => "1", "a" => "x" }], page.rows
    assert_equal "index:a", page.plan
    assert_equal 1, page.examined
  end

  def test_page_headers_next_and_total
    client.ensure_collection(@coll)
    client.bulk(@coll, (1..5).map { |i| { _id: "c#{i}", n: i } })

    page = client.search(@coll, sort: { n: 1 }, limit: 2, count: true)

    assert_equal 2, page.rows.size
    assert_equal 5, page.total
    refute_nil page.next

    rest = client.search(@coll, sort: { n: 1 }, limit: 10, after: page.next)

    assert_equal [3, 4, 5], rest.rows.map { |r| r["n"] }
    assert_nil rest.next
  end

  def test_large_chunked_response
    client.ensure_collection(@coll)
    client.bulk(@coll, (1..200).map { |i| { _id: "c#{i}", pad: "x" * 100 } })

    assert_equal 200, client.search(@coll, {}).rows.size
  end

  def test_keep_alive_reuses_one_connection
    fresh = Ndb::Client.new(socket: TestDaemon.socket)

    fresh.ensure_collection(@coll)
    20.times { fresh.search(@coll, {}) }

    assert_equal 1, fresh.pool.created
  end

  def test_error_codes_map_to_classes
    client.ensure_collection(@coll, unique: ["u"], time: "ts")
    client.bulk(@coll, [{ _id: "1", u: "a" }])

    assert_raises(Ndb::UniqueViolation) { client.bulk(@coll, [{ _id: "1" }]) }
    assert_raises(Ndb::UniqueViolation) { client.bulk(@coll, [{ _id: "2", u: "a" }]) }
    assert_raises(Ndb::UnknownOperator) { client.update_by(@coll, { _id: "1" }, { "$rename" => { a: "b" } }) }
    assert_raises(Ndb::NoUpdate) { client.update_by(@coll, { _id: "1", a: 1 }, {}) }
    assert_raises(Ndb::ConflictingUpdate) { client.update_by(@coll, { _id: "1" }, { "$set" => { a: 1 }, "$inc" => { a: 1 } }) }
    assert_raises(Ndb::UnindexedMany) { client.update_by(@coll, { a: 1 }, { "$set" => { b: 1 } }, many: true) }
    assert_raises(Ndb::UnknownCollection) { client.search("missing_coll", {}) }
    assert_raises(Ndb::InvalidQuery) { client.search(@coll, query: { a: { "$bogus" => 1 } }) }
    assert_raises(Ndb::CatalogConflict) { client.ensure_collection(@coll, time: "other") }
  end

  def test_error_carries_code_and_status
    client.ensure_collection(@coll)

    error = assert_raises(Ndb::NoUpdate) { client.update_by(@coll, { _id: "x" }, {}) }

    assert_equal "no_update", error.code
    assert_equal 400, error.status
  end

  def test_threads_share_the_pool
    client.ensure_collection(@coll)
    client.bulk(@coll, [{ _id: "1" }])

    fresh = Ndb::Client.new(socket: TestDaemon.socket)

    counts = 8.times.map do
      Thread.new { 25.times.map { fresh.search(@coll, {}).rows.size }.sum }
    end.map(&:value)

    assert_equal [25] * 8, counts
    assert_operator fresh.pool.created, :<=, 8
  end

  def test_connection_refused_is_connection_error
    dead = Ndb::Client.new(socket: "/tmp/ndb-no-such.sock")

    assert_raises(Ndb::ConnectionError) { dead.search("x", {}) }
  end

  def test_reconnects_after_the_server_closed_an_idle_connection
    client.ensure_collection(@coll)
    client.pool.idle_connections.each { |c| c.socket.close }

    assert_equal [], client.search(@coll, {}).rows
  end
end
