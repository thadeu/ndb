# frozen_string_literal: true

require "test_helper"

class AggregateTest < Minitest::Test
  def setup
    @coll = collection_name

    client.ensure_collection(@coll, id: "cid")
    client.insert(@coll, [
                    { cid: "a", status: "answered", duration: 10 },
                    { cid: "b", status: "answered", duration: 30 },
                    { cid: "c", status: "failed", duration: 5 }
                  ])
  end

  def test_group_and_sum
    page = client.aggregate(@coll, pipeline: [
                              { "$group" => { "_id" => "$status", "total" => { "$sum" => "$duration" } } },
                              { "$sort" => { "_id" => 1 } }
                            ])

    assert_equal [40, 5], page.rows.map { |row| row["total"] }
  end

  def test_count_uses_find_with_count_true
    page = client.find(@coll, where: { status: "answered" }, count: true, limit: 1)

    assert_equal 2, page.total
  end
end
