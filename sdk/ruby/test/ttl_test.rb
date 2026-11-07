# frozen_string_literal: true

require "test_helper"

class TtlTest < Minitest::Test
  def setup
    @coll = collection_name

    client.ensure_collection(@coll, { expires_field: "exp" })
  end

  def test_insert_and_update_with_expire_in
    client.bulk(@coll, [{ _id: "a" }, { _id: "b" }], expire_in: 3600)

    assert_in_delta 3600, client.ttl(@coll, "a"), 2

    client.update_by(@coll, { _id: "a" }, { "$set" => { n: 1 } }, expire_in: "30m")

    assert_in_delta 1800, client.ttl(@coll, "a"), 2
    assert_in_delta 3600, client.ttl(@coll, "b"), 2
  end

  def test_expire_persist_and_ttl
    client.bulk(@coll, [{ _id: "s1" }])

    assert_equal(-1, client.ttl(@coll, "s1"))

    res = client.expire(@coll, "s1", in: 1800)

    assert_equal 2, res["_v"]
    assert res["expires_at"]
    assert_in_delta 1800, client.ttl(@coll, "s1"), 2

    assert_equal 3, client.expire(@coll, "s1", at: Time.now + 7200)["_v"]
    assert_in_delta 7200, client.ttl(@coll, "s1"), 2

    assert_equal 4, client.persist(@coll, "s1")["_v"]
    assert_equal(-1, client.ttl(@coll, "s1"))
  end

  def test_missing_and_expired_documents
    client.bulk(@coll, [{ _id: "s1" }])
    client.expire(@coll, "s1", at: Time.now - 60)

    assert_nil client.ttl(@coll, "s1")
    assert_nil client.ttl(@coll, "nope")
    assert_raises(Ndb::NotFound) { client.expire(@coll, "s1", in: 60) }
    assert_raises(Ndb::NotFound) { client.persist(@coll, "nope") }
  end

  def test_collection_without_expires_field
    plain = collection_name

    client.ensure_collection(plain)
    client.bulk(plain, [{ _id: "a" }])

    assert_raises(Ndb::TtlNotEnabled) { client.bulk(plain, [{ _id: "b" }], expire_in: 60) }
    assert_raises(Ndb::TtlNotEnabled) { client.expire(plain, "a", in: 60) }

    derived = collection_name

    client.ensure_collection(derived, { time: "at", ttl: "1h" })

    assert_raises(Ndb::TtlDerived) { client.expire(derived, "a", in: 60) }
  end
end
