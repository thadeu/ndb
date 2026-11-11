# frozen_string_literal: true

require "test_helper"

class CompoundTest < Minitest::Test
  def test_index_and_unique_index_of_many_attributes
    member = model do
      attribute :acc, :string
      attribute :email, :string
      attribute :n, :integer

      index :acc, :n
      index :acc, :email, unique: true
      index "items.sku"
    end

    assert_equal ["acc,n", "items.sku"], member.collection_definition["indexes"]
    assert_equal ["acc,email"], member.collection_definition["unique"]

    member.create!(_id: "m1", acc: "a", email: "x@x", n: 1)
    member.create!(_id: "m2", acc: "b", email: "x@x", n: 2)

    assert_raises(Ndb::RecordNotUnique) { member.create!(_id: "m3", acc: "a", email: "x@x") }
    assert_equal ["m1"], member.where(acc: "a", n: 1..5).map(&:id)
    assert_raises(ArgumentError) { member.index }
    assert_raises(ArgumentError) { member.index :a, :b, :c, :d, :e }
  end
end
