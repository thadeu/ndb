# frozen_string_literal: true

require "test_helper"

class FiltersTest < Minitest::Test
  def setup
    @coll = NdbTestHelpers.next_name
    now = Time.now.utc

    client.ensure_collection(@coll, time: "ts")
    client.insert(@coll, [
                    { _id: "o1", ts: (now - (48 * 3600)).iso8601, items: [{ sku: "a", qty: 1 }, { sku: "b", qty: 5 }] },
                    { _id: "o2", ts: (now - 60).iso8601, items: [{ sku: "a", qty: 3 }] }
                  ])
  end

  def test_date_math
    assert_equal ["o2"], ids(ts: { "$gte" => "now-1d" })
  end

  def test_elem_match
    assert_equal ["o2"], ids(items: { "$elemMatch" => { sku: "a", qty: { "$gt" => 2 } } })
  end

  private

  def ids(where)
    client.find(@coll, where: where).rows.map { |r| r["_id"] }
  end
end
