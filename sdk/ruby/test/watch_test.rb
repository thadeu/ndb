# frozen_string_literal: true

require "test_helper"

class WatchTest < Minitest::Test
  def setup
    @coll = NdbTestHelpers.next_name

    client.ensure_collection(@coll, id: "cid")
    client.insert(@coll, (1..6).map { |i| { cid: "c#{i}", n: i } })
  end

  def test_watch_yields_events_until_the_limit_and_ack_resumes
    seen = []

    client.watch(@coll, "relay", limit: 2) { |ev| seen << ev }

    assert_equal %w[c1 c2], seen.map { |ev| ev["id"] }
    assert_equal %w[put put], seen.map { |ev| ev["op"] }
    assert_equal 1, seen.first["doc"]["n"]

    client.ack(@coll, "relay", seen.last["pos"])

    resumed = []

    client.watch(@coll, "relay", limit: 1) { |ev| resumed << ev }

    assert_equal %w[c3], resumed.map { |ev| ev["id"] }
  end

  def test_watch_where_delivers_only_matching_events
    seen = []

    client.watch(@coll, "filtered", where: { n: 5.. }, limit: 2) { |ev| seen << ev["id"] }

    assert_equal %w[c5 c6], seen
  end

  def test_watch_without_a_block_returns_an_enumerator
    ids = client.watch(@coll, "enum", limit: 3).map { |ev| ev["id"] }

    assert_equal %w[c1 c2 c3], ids
  end

  def test_watch_with_a_bad_where_raises_invalid_query
    error = assert_raises(Ndb::InvalidQuery) do
      client.watch(@coll, "bad", where: { n: { "$bogus" => 1 } }) { |_| nil }
    end

    assert_equal "invalid_query", error.code
  end

  def test_error_codes_of_the_history_are_mapped
    error = Ndb.error_for("history_truncated", "gone", 410)

    assert_instance_of Ndb::HistoryTruncated, error
    assert_instance_of Ndb::WatchLagged, Ndb.error_for("watch_lagged", "slow", 200)
  end
end
