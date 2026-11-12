# frozen_string_literal: true

require "test_helper"

class UniquenessTest < Minitest::Test
  def setup
    @domain = model do
      attribute :name, :string, unique: true
      attribute :app_id, :string, index: true

      validates :name, uniqueness: true
    end
  end

  def test_a_taken_value_is_an_error_on_the_field
    @domain.create!(name: "a.com")

    record = @domain.new(name: "a.com")

    refute_predicate record, :valid?
    assert_equal ["has already been taken"], record.errors[:name]
  end

  def test_a_saved_record_does_not_take_its_own_value
    record = @domain.create!(name: "a.com")

    record.app_id = "x"

    assert_predicate record, :valid?
  end

  def test_scope
    scoped = model do
      attribute :name, :string
      attribute :app_id, :string, index: true

      validates :name, uniqueness: { scope: :app_id }
    end

    scoped.create!(name: "a.com", app_id: "1")

    assert_predicate scoped.new(name: "a.com", app_id: "2"), :valid?
    refute_predicate scoped.new(name: "a.com", app_id: "1"), :valid?
  end

  def test_the_taken_message_has_a_translation
    record = @domain.new

    record.errors.add(:name, :taken)

    assert_equal ["has already been taken"], record.errors[:name]
  end
end
