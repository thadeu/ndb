# frozen_string_literal: true

require "test_helper"

class TruncateTest < Minitest::Test
  def setup
    @coll = NdbTestHelpers.next_name

    client.ensure_collection(@coll, indexes: ["n"])
    client.bulk(@coll, (1..5).map { |i| { _id: "c#{i}", n: i } })
  end

  def test_truncate_keeps_the_definition_and_sends_one_watch_event
    assert_equal({ "collection" => @coll, "truncated" => true }, client.truncate(@coll))
    assert_nil client.get(@coll, "c1")

    client.bulk(@coll, [{ _id: "c9", n: 9 }])

    events = client.watch(@coll, "relay", limit: 2).to_a

    assert_equal %w[truncate put], events.map { |ev| ev["op"] }
    assert_nil events.first["_id"]
    assert_includes client.collections.map { |c| c["name"] }, @coll
  end

  def test_drop_removes_the_collection
    assert_equal({ "collection" => @coll, "dropped" => true }, client.drop(@coll))
    refute_includes client.collections.map { |c| c["name"] }, @coll
    assert_raises(Ndb::UnknownCollection) { client.drop(@coll) }
  end
end
