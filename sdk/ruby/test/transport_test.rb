# frozen_string_literal: true

require "test_helper"

class TransportTest < Minitest::Test
  def setup
    @coll = NdbTestHelpers.next_name
  end

  def test_ensure_insert_find_roundtrip
    client.ensure_collection(@coll, id: "cid", indexes: ["a"])

    results = client.insert(@coll, [{ cid: "1", a: "x" }, { cid: "2", a: "y" }])
    page = client.find(@coll, where: { a: "x" })

    assert_equal [{ "id" => "1", "v" => 1 }, { "id" => "2", "v" => 1 }], results
    assert_equal [{ "cid" => "1", "a" => "x" }], page.rows
    assert_equal "index:a", page.plan
    assert_equal 1, page.examined
  end

  def test_page_headers_next_and_total
    client.ensure_collection(@coll, id: "cid")
    client.insert(@coll, (1..5).map { |i| { cid: "c#{i}", n: i } })

    page = client.find(@coll, sort: { n: 1 }, limit: 2, count: true)

    assert_equal 2, page.rows.size
    assert_equal 5, page.total
    refute_nil page.next_cursor

    rest = client.find(@coll, sort: { n: 1 }, limit: 10, after: page.next_cursor)

    assert_equal [3, 4, 5], rest.rows.map { |r| r["n"] }
    assert_nil rest.next_cursor
  end

  def test_large_chunked_response
    client.ensure_collection(@coll, id: "cid")
    client.insert(@coll, (1..200).map { |i| { cid: "c#{i}", pad: "x" * 100 } })

    assert_equal 200, client.find(@coll, {}).rows.size
  end

  def test_keep_alive_reuses_one_connection
    fresh = NdbRecord::Client.new(socket: TestDaemon.socket)

    fresh.ensure_collection(@coll, id: "cid")
    20.times { fresh.find(@coll, {}) }

    assert_equal 1, fresh.pool.created
  end

  def test_error_codes_map_to_classes
    client.ensure_collection(@coll, id: "cid", unique: ["u"])
    client.insert(@coll, [{ cid: "1", u: "a" }])

    assert_raises(NdbRecord::RecordNotUnique) { client.insert(@coll, [{ cid: "1" }]) }
    assert_raises(NdbRecord::RecordNotUnique) { client.insert(@coll, [{ cid: "2", u: "a" }]) }
    assert_raises(NdbRecord::NotFound) { client.update(@coll, "nope", set: { a: 1 }) }
    assert_raises(NdbRecord::StaleObjectError) { client.update(@coll, "1", set: { a: 1 }, if_version: 99) }
    assert_raises(NdbRecord::UnknownCollection) { client.find("missing_coll", {}) }
    assert_raises(NdbRecord::InvalidQuery) { client.find(@coll, where: { a: { "$bogus" => 1 } }) }
    assert_raises(NdbRecord::CatalogConflict) { client.ensure_collection(@coll, id: "other") }
  end

  def test_error_carries_code_and_status
    client.ensure_collection(@coll, id: "cid")

    error = assert_raises(NdbRecord::NotFound) { client.update(@coll, "x", set: { a: 1 }) }

    assert_equal "not_found", error.code
    assert_equal 404, error.status
  end

  def test_threads_share_the_pool
    client.ensure_collection(@coll, id: "cid")
    client.insert(@coll, [{ cid: "1" }])

    fresh = NdbRecord::Client.new(socket: TestDaemon.socket)

    counts = 8.times.map do
      Thread.new { 25.times.map { fresh.find(@coll, {}).rows.size }.sum }
    end.map(&:value)

    assert_equal [25] * 8, counts
    assert_operator fresh.pool.created, :<=, 8
  end

  def test_connection_refused_is_connection_error
    dead = NdbRecord::Client.new(socket: "/tmp/ndb-no-such.sock")

    assert_raises(NdbRecord::ConnectionError) { dead.find("x", {}) }
  end

  def test_reconnects_after_the_server_closed_an_idle_connection
    client.ensure_collection(@coll, id: "cid")
    client.pool.idle_connections.each { |c| c.socket.close }

    assert_equal [], client.find(@coll, {}).rows
  end
end
