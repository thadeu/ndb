# frozen_string_literal: true

require "test_helper"

class ConditionsTest < Minitest::Test
  def test_values_become_operators
    where = Ndb::Conditions.equality(status: "x", n: 1..3, k: %w[a b], gone: nil, "$or" => [{ a: 1 }])

    assert_equal({ "$gte" => 1, "$lte" => 3 }, where["n"])
    assert_equal({ "$in" => %w[a b] }, where["k"])
    assert_equal({ "$exists" => false }, where["gone"])
    assert_equal "x", where["status"]
    assert_equal [{ "a" => 1 }], where["$or"]
  end

  def test_times_are_utc_rfc3339_with_milliseconds
    assert_equal "1970-01-01T00:00:00.000Z", Ndb::Conditions.json_value(Time.at(0))
  end

  def test_loads_without_activesupport
    refute defined?(ActiveSupport), "the plain client must not load ActiveSupport"
  end
end
