# frozen_string_literal: true

require "test_helper"

class UpsertTest < Minitest::Test
  def setup
    @user = model do
      attribute :name, :string
      attribute :plan, :string
      attribute :visits, :integer
    end

    @user.create!(_id: "u1", name: "Ana", plan: "free", visits: 1)
  end

  def test_upsert_all_writes_times_in_utc_with_milliseconds
    timed = model do
      attribute :sent_at, :datetime
    end

    at = Time.utc(2026, 11, 12, 15, 0, 5)

    Time.use_zone("America/Sao_Paulo") do
      timed.upsert_all([{ id: "m1", sent_at: at.in_time_zone, seen_at: at.in_time_zone }])
      timed.upsert(id: "m2", sent_at: at.getlocal("-03:00"))
    end

    raw = client.get(timed.collection_name, "m1")

    assert_equal "2026-11-12T15:00:05.000Z", raw["sent_at"]
    assert_equal "2026-11-12T15:00:05.000Z", raw["seen_at"]
    assert_equal "2026-11-12T15:00:05.000Z", client.get(timed.collection_name, "m2")["sent_at"]
  end

  def test_upsert_merges_the_given_attributes
    assert_equal({ "_id" => "u1", "_v" => 2 }, @user.upsert(id: "u1", plan: "pro"))

    user = @user.find("u1")

    assert_equal %w[Ana pro], [user.name, user.plan]
    assert_equal 1, user.visits
  end

  def test_upsert_all_creates_and_merges_and_casts
    results = @user.upsert_all([{ id: "u1", visits: "5" }, { _id: "u2", name: "Bia" }])

    assert_equal [2, 1], results.map { |r| r["_v"] }
    assert_equal 5, @user.find("u1").visits
    assert_equal "Bia", @user.find("u2").name
  end

  def test_search_result_is_data
    result = @user.search("query" => { "_id" => "u1" })

    assert_kind_of Data, result
    assert_equal ["u1"], result.records.map(&:id)
  end
end
