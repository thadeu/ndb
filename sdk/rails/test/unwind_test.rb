# frozen_string_literal: true

require "test_helper"

class UnwindTest < Minitest::Test
  def test_unwind_with_group_and_sum
    basket = model do
      attribute :status, :string
      attribute :items
    end

    basket.create!(_id: "b1", status: "paid", items: [{ "sku" => "a", "qty" => 2 }, { "sku" => "b", "qty" => 1 }])
    basket.create!(_id: "b2", status: "paid", items: [{ "sku" => "a", "qty" => 5 }])
    basket.create!(_id: "b3", status: "open", items: [{ "sku" => "a", "qty" => 100 }])
    basket.create!(_id: "b4", status: "paid")

    assert_equal({ "a" => 7, "b" => 1 }, basket.where(status: "paid").unwind(:items).group("items.sku").sum("items.qty"))
    assert_equal({ "a" => 2, "b" => 1 }, basket.where(status: "paid").unwind(:items).group("items.sku").count)
    assert_equal 108, basket.unwind(:items).sum("items.qty")
    assert_equal 4, basket.unwind(:items).count
    assert_equal 3, basket.where(status: "paid").unwind(:items).count
    assert_equal 0, basket.where(status: "nope").unwind(:items).count
    assert_raises(ArgumentError) { basket.unwind(:items).to_a }
  end
end
