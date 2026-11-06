# frozen_string_literal: true

require "test_helper"

class UpdateTest < Minitest::Test
  def setup
    @coll = collection_name

    client.ensure_collection(@coll, { indexes: ["grp"] })
    client.insert(@coll, [{ _id: "a", grp: "g1", qty: 1 }, { _id: "b", grp: "g1", qty: 1 }, { _id: "c", grp: "g2", qty: 1 }])
  end

  def test_update_merges_filter_and_operators
    res = client.update(@coll, { _id: "a" }, { "$inc" => { qty: 4 }, "$set" => { "meta.k" => "v" } })

    assert_equal 1, res["matched"]
    assert_equal 1, res["modified"]
    assert_equal 5, res["doc"]["qty"]
    assert_equal 2, res["doc"]["_v"]
    assert_equal({ "k" => "v" }, res["doc"]["meta"])
  end

  def test_version_guard_is_a_filter
    assert_equal 0, client.update(@coll, { _id: "a", _v: 9 }, { "$set" => { qty: 2 } })["matched"]
    assert_equal 1, client.update(@coll, { _id: "a", _v: 1 }, { "$set" => { qty: 2 } })["matched"]
  end

  def test_guarded_decrement_wins_once
    guard = { _id: "c", qty: { "$gte" => 1 } }

    assert_equal 1, client.update(@coll, guard, { "$inc" => { qty: -1 } })["modified"]
    assert_equal 0, client.update(@coll, guard, { "$inc" => { qty: -1 } })["matched"]
  end

  def test_many_and_delete
    res = client.update(@coll, { grp: "g1" }, { "$set" => { done: true } }, many: true)

    assert_equal({ "matched" => 2, "modified" => 2 }, res)
    assert_equal({ "deleted" => 2 }, client.delete(@coll, { grp: "g1" }, many: true))
    assert_equal({ "deleted" => 0 }, client.delete(@coll, { _id: "zzz" }))
  end

  def test_upsert_and_operator_check
    res = client.update(@coll, { _id: "n" }, { "$inc" => { qty: 2 }, "$push" => { tags: "x" } }, upsert: true)

    assert_equal 0, res["matched"]
    assert_equal({ "_id" => "n", "qty" => 2, "tags" => ["x"], "_v" => 1 }, res["doc"])

    error = assert_raises(ArgumentError) { client.update(@coll, { _id: "a" }, { set: { qty: 1 } }) }

    assert_match(/must start with \$/, error.message)
  end
end
