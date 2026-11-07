# frozen_string_literal: true

require "test_helper"

class UpsertTest < Minitest::Test
  def setup
    @users = client.collection(NdbTestHelpers.next_name, unique: ["email"])
    @users.insert(_id: "u1", name: "Ana", plan: "free", email: "a@x")
  end

  def test_upsert_merges_an_existing_id_and_creates_a_new_one
    assert_equal({ "_id" => "u1", "_v" => 2 }, @users.upsert(_id: "u1", plan: "pro"))
    assert_equal({ "name" => "Ana", "plan" => "pro", "email" => "a@x" }, @users.get("u1").slice("name", "plan", "email"))

    results = @users.upsert_all([{ _id: "u1", plan: "team" }, { _id: "u2", name: "Bia" }])

    assert_equal [["u1", 3], ["u2", 1]], results.map { |r| [r["_id"], r["_v"]] }
  end

  def test_upsert_all_is_all_or_nothing
    assert_raises(Ndb::UniqueViolation) do
      @users.upsert_all([{ _id: "u3", name: "Cai" }, { _id: "u4", email: "a@x" }])
    end

    assert_nil @users.get("u3")
  end

  def test_insert_still_refuses_an_existing_id
    assert_raises(Ndb::UniqueViolation) { @users.insert(_id: "u1") }
  end

  def test_watch_events_carry_the_consumer_for_ack
    events = @users.watch("relay", limit: 1).to_a

    assert_equal "relay", events.first["consumer"]
    assert_equal({ "ok" => true }, @users.ack(events.first))
  end

  def test_page_is_immutable_data
    page = @users.query.page

    assert_kind_of Data, page
    assert_predicate page, :frozen?
    refute_respond_to page, :rows=
  end
end
