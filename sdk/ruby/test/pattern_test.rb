# frozen_string_literal: true

require "test_helper"

class PatternTest < Minitest::Test
  def setup
    @base = "#{NdbTestHelpers.next_name}-logs-"

    client.bulk("#{@base}1", [{ _id: "a1", at: 1, lvl: "info" }, { _id: "a2", at: 3, lvl: "error" }])
    client.bulk("#{@base}2", [{ _id: "b1", at: 2, lvl: "error" }])
  end

  def test_find_reads_every_collection_of_the_pattern
    page = client.search("#{@base}*", sort: { at: -1 }, count: true)

    assert_equal %w[a2 b1 a1], page.rows.map { |r| r["_id"] }
    assert_equal ["#{@base}1", "#{@base}2", "#{@base}1"], page.rows.map { |r| r["_coll"] }
    assert_equal 3, page.total
  end

  def test_aggregate_groups_over_every_collection
    page = client.aggregate("#{@base}*", pipeline: [{ "$group" => { "_id" => "$lvl", "n" => { "$count" => {} } } }])

    assert_equal [{ "_id" => "error", "n" => 2 }, { "_id" => "info", "n" => 1 }], page.rows
  end

  def test_writes_on_a_pattern_fail
    err = assert_raises(Ndb::Error) { client.bulk("#{@base}*", [{ a: 1 }]) }

    assert_equal "pattern_read_only", err.code
  end
end
