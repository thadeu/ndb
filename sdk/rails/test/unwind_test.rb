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
  end
end
