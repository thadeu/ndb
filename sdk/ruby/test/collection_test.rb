# frozen_string_literal: true

require "test_helper"

class CollectionTest < Minitest::Test
  def setup
    @calls = client.collection(NdbTestHelpers.next_name, indexes: ["status"])
  end

  def test_insert_query_and_count
    assert_equal({ "_id" => "c1", "_v" => 1 }, @calls.insert(_id: "c1", status: "a", n: 1))
    assert_equal %w[c2 c3], @calls.insert([{ _id: "c2", status: "b", n: 2 }, { _id: "c3", status: "b", n: 3 }]).map { |r| r["_id"] }

    assert_equal %w[c3 c2], @calls.query(status: "b").sort(n: -1).all.map { |r| r["_id"] }
    assert_equal "c1", @calls.query(n: 1..1).first["_id"]
    assert_equal 2, @calls.count(status: "b")
    assert_equal 3, @calls.count
    assert_equal %w[c1 c2 c3], @calls.query.sort(_id: 1).limit(1).map { |r| r["_id"] }
    assert_equal %w[c2], @calls.raw_query("n" => { "$gt" => 1 }).query(n: 2).all.map { |r| r["_id"] }
  end

  def test_writes_by_id_and_by_query
    @calls.insert([{ _id: "a", status: "q", n: 1 }, { _id: "b", status: "q", n: 1 }])

    assert_equal 1, @calls.update("a", { "$inc" => { n: 1 } }, guard: { _v: 1 })["matched"]
    assert_equal 0, @calls.update("a", { "$inc" => { n: 1 } }, guard: { _v: 1 })["matched"]
    assert_equal 2, @calls.update_by({ status: "q" }, { "$set" => { done: true } }, many: true)["modified"]
    assert_equal({ "deleted" => 1 }, @calls.delete("a"))
    assert_equal({ "deleted" => 1 }, @calls.delete_by(status: "q"))
    assert_nil @calls.get("a")
  end

  def test_search_body_and_admin
    @calls.insert([{ _id: "a", status: "q" }, { _id: "b", status: "q" }])

    page = @calls.search("query" => { "status" => "q" }, "sort" => { "_id" => 1 }, "limit" => 1, "count" => true)

    assert_equal ["a"], page.rows.map { |r| r["_id"] }
    assert_equal 2, page.total
    refute_nil page.next

    assert client.health
    assert_includes client.collections.map { |c| c["name"] }, @calls.name
    assert client.stats.key?("collections")
    refute_empty client.size
    refute_empty @calls.stats
    assert_includes @calls.ensure_index("n"), "n"
    refute_includes @calls.drop_index("n"), "n"
  end

  def test_a_pattern_never_declares
    logs = client.collection("#{NdbTestHelpers.next_name}-nope-*")

    assert_equal [], logs.query.all
    assert_raises(Ndb::Error) { logs.insert(a: 1) }
  end
end
