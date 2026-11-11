# frozen_string_literal: true

require "test_helper"

class UnwindTest < Minitest::Test
  def test_groups_by_the_elements_of_an_array
    baskets = client.collection(NdbTestHelpers.next_name)

    baskets.insert([
      { _id: "b1", items: [{ sku: "a", qty: 2 }, { sku: "b", qty: 1 }] },
      { _id: "b2", items: [{ sku: "a", qty: 5 }] },
      { _id: "b3" }
    ])

    page = baskets.aggregate([
      { "$unwind" => "$items" },
      { "$group" => { _id: "$items.sku", qty: { "$sum" => "$items.qty" } } },
      { "$sort" => { _id: 1 } }
    ])

    assert_equal [{ "_id" => "a", "qty" => 7 }, { "_id" => "b", "qty" => 1 }], page.rows
  end
end
