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

    assert_equal ["created_at", "updated_at", "acc,n", "items.sku"], member.collection_definition["indexes"]
    assert_equal ["acc,email"], member.collection_definition["unique"]

    member.create!(_id: "m1", acc: "a", email: "x@x", n: 1)
    member.create!(_id: "m2", acc: "b", email: "x@x", n: 2)

    assert_raises(Ndb::RecordNotUnique) { member.create!(_id: "m3", acc: "a", email: "x@x") }
    assert_equal ["m1"], member.where(acc: "a", n: 1..5).map(&:id)
    assert_raises(ArgumentError) { member.index }
    assert_raises(ArgumentError) { member.index :a, :b, :c, :d, :e }
  end

  # A blind field in a compound index or unique key: the daemon keys it by
  # the hash, with the normalization of the field.
  def test_a_blind_field_in_a_compound_index_and_unique_key
    msg = model do
      attribute :app_id, :string, index: true
      attribute :idempotency_key, :string
      attribute :rcpt_to, :string, blind: :lower

      index :app_id, :rcpt_to
      index :app_id, :idempotency_key, :rcpt_to, unique: true
    end

    msg.create!(id: "m1", app_id: "a", idempotency_key: "k", rcpt_to: "Ana@Dest.test")

    assert_raises(Ndb::RecordNotUnique) { msg.create!(id: "m2", app_id: "a", idempotency_key: "k", rcpt_to: "ana@dest.test") }

    msg.create!(id: "m3", app_id: "a", idempotency_key: "k2", rcpt_to: "ana@dest.test")

    assert_equal %w[m1 m3], msg.where(app_id: "a", rcpt_to: "ANA@dest.test").order(_id: :asc).map(&:id)
  end
end
