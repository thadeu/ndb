# frozen_string_literal: true

require "test_helper"

class CallbacksTest < Minitest::Test
  def test_before_validation_runs_before_the_validations
    domain = model do
      attribute :name, :string

      validates :name, format: { with: /\A[a-z.]+\z/ }

      before_validation { self.name = name.to_s.strip.downcase }
    end

    record = domain.create!(name: "  Example.COM ")

    assert_equal "example.com", record.name
    assert_equal "example.com", domain.find(record.id).name
  end

  def test_after_validation_runs
    seen = []

    domain = model do
      attribute :name, :string

      after_validation { seen << errors.size }
    end

    domain.create!(name: "a")

    assert_equal [0], seen
  end
end
