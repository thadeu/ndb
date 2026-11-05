# frozen_string_literal: true

require "test_helper"

class TtlTest < Minitest::Test
  def setup
    @session = model do
      primary_key :sid

      attribute :user, :string
      attribute :expires_at, :datetime, expires: true
    end
  end

  def test_create_with_expire_in
    @session.create!({ sid: "a", user: "u" }, expire_in: 1.hour)

    assert_in_delta 3600, @session.find("a").ttl, 2
    assert_in_delta 3600, @session.find("a").expires_at - Time.now, 2
  end

  def test_create_many_and_update_with_expire_in
    @session.create!([{ sid: "a" }, { sid: "b" }], expire_in: "10m")

    assert_in_delta 600, @session.find("b").ttl, 2

    session = @session.find("a")

    session.update!({ user: "x" }, expire_in: 30.minutes)

    assert_in_delta 1800, session.ttl, 2
    assert_equal "x", @session.find("a").user
  end

  def test_expire_and_persist_ttl
    session = @session.create!(sid: "s1", user: "u")

    assert_equal(-1, session.ttl)

    session.user = "unsaved"
    session.expire(30.minutes)

    assert_in_delta 1800, session.ttl, 2
    assert_in_delta 1800, session.expires_at - Time.now, 2
    assert_equal 2, session.version
    assert_equal ["user"], session.changed

    session.persist_ttl!

    assert_equal(-1, session.ttl)
    assert_nil session.expires_at
    assert_equal 3, session.version
  end

  def test_missing_or_expired_document
    session = @session.create!(sid: "s1")

    session.expire(60)
    Ndb::Rails.client.expire(@session.collection_name, "s1", at: Time.now - 60)

    assert_nil session.ttl
    assert_raises(Ndb::NotFound) { session.expire(60) }
    assert_raises(Ndb::NotFound) { session.persist_ttl! }
  end

  def test_model_without_expires_attribute
    plain = model { attribute :n, :integer }

    assert_raises(Ndb::TtlNotEnabled) { plain.create!({ n: 1 }, expire_in: 60) }

    record = plain.create!(n: 1)

    assert_raises(Ndb::TtlNotEnabled) { record.expire(60) }
  end
end
