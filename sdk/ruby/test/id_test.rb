# frozen_string_literal: true

require "test_helper"

class IdTest < Minitest::Test
  UUID7 = /\A[0-9a-f]{8}-[0-9a-f]{4}-7[0-9a-f]{3}-[89ab][0-9a-f]{3}-[0-9a-f]{12}\z/

  def setup
    @coll = collection_name

    client.ensure_collection(@coll, { unique: ["slug"] })
    client.bulk(@coll, [{ _id: "a", slug: "x", n: 1 }])
  end

  def test_new_id_is_a_uuid_v7_in_creation_order
    prev = Ndb.new_id

    assert_match UUID7, prev

    10_000.times do
      nxt = Ndb.new_id

      assert_operator nxt, :>, prev

      prev = nxt
    end
  end

  def test_get_returns_the_document_or_nil
    doc = client.get(@coll, "a")

    assert_equal "a", doc["_id"]
    assert_equal 1, doc["_v"]
    assert_nil client.get(@coll, "nope")
  end

  def test_update_and_delete_take_an_id_string
    res = client.update(@coll, "a", { "$inc" => { n: 2 } })

    assert_equal 3, res["doc"]["n"]
    assert_equal 0, client.update(@coll, "nope", { "$set" => { n: 1 } })["matched"]
    assert_equal 1, client.update(@coll, "z", { "$set" => { n: 1 } }, upsert: true)["modified"]
    assert_equal({ "deleted" => 1 }, client.delete(@coll, "a"))
    assert_equal({ "deleted" => 0 }, client.delete(@coll, "a"))
    assert_nil client.get(@coll, "a")
  end

  def test_insert_without_id_generates_one
    res = client.bulk(@coll, [{ slug: "y" }])

    assert_match UUID7, res.first["_id"]
    assert_equal "y", client.get(@coll, res.first["_id"])["slug"]
  end
end
