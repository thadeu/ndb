# frozen_string_literal: true

require "test_helper"
require "minitest/mock"

# Every model has created_at and updated_at, indexed, with no declaration.
class TimestampsTest < Minitest::Test
  def setup
    @post = model do
      attribute :status, :string, index: true
      attribute :n, :integer
    end
  end

  def test_create_sets_both
    post = @post.create!(id: "p1", status: "a")
    raw = client.get(@post.collection_name, "p1")

    assert_kind_of Time, post.created_at
    assert_equal post.created_at, post.updated_at
    assert_match(/\A\d{4}-\d\d-\d\dT\d\d:\d\d:\d\d\.\d{3}Z\z/, raw["created_at"])
    assert_equal raw["created_at"], raw["updated_at"]
    assert_equal post.created_at, @post.find("p1").created_at
  end

  def test_a_given_created_at_is_kept
    at = Time.utc(2020, 1, 1)

    assert_equal at, @post.create!(status: "a", created_at: at).created_at
  end

  def test_save_sets_updated_at_and_keeps_created_at
    post = @post.create!(id: "p1", status: "a")
    created = post.created_at

    travel(2) { post.update!(status: "b") }

    found = @post.find("p1")

    assert_equal created, found.created_at
    assert_operator found.updated_at, :>, created
    assert_equal post.updated_at, found.updated_at
  end

  def test_relation_update_sets_updated_at
    post = @post.create!(id: "p1", status: "a", n: 1)

    travel(2) { @post.where(status: "a").update("$inc" => { "n" => 1 }) }

    assert_operator @post.find("p1").updated_at, :>, post.updated_at
  end

  def test_a_time_in_relation_update_is_rfc3339
    @post.create!(id: "p1", status: "a")

    at = Time.new(2026, 11, 12, 17, 31, 33, "-03:00")

    @post.where(id: "p1").update("$set" => { "sent_at" => at })

    assert_equal "2026-11-12T20:31:33.000Z", client.get(@post.collection_name, "p1")["sent_at"]
    assert_equal 1, @post.where(id: "p1").where("sent_at" => { "$lt" => at + 1 }).count
  end

  def test_increment_sets_updated_at
    post = @post.create!(id: "p1", status: "a", n: 1)
    before = post.updated_at

    travel(2) { post.increment!(:n) }

    assert_operator post.updated_at, :>, before
    assert_equal post.updated_at, @post.find("p1").updated_at
  end

  def test_upsert_sets_created_at_only_on_a_new_document
    @post.create!(id: "p1", status: "a")

    created = @post.find("p1").created_at

    travel(2) { @post.upsert_all([{ id: "p1", status: "b" }, { id: "p2", status: "c" }]) }

    p1 = @post.find("p1")
    p2 = @post.find("p2")

    assert_equal created, p1.created_at
    assert_operator p1.updated_at, :>, created
    assert_equal p2.created_at, p2.updated_at
  end

  def test_find_or_create_by_sets_both
    post = @post.find_or_create_by!(status: "z")

    refute_nil @post.find(post.id).created_at
    refute_nil @post.find(post.id).updated_at
  end

  def test_both_are_indexed
    assert_includes @post.indexed_fields, "created_at"
    assert_includes @post.indexed_fields, "updated_at"
    assert_equal 1, @post.tap { |m| m.create!(status: "a") }.where(created_at: 1.hour.ago..).delete_all
  end

  private

  # Runs the block with the clock moved seconds ahead.
  def travel(seconds)
    now = Time.now
    later = now + seconds

    Time.stub(:now, later) { yield }
  end
end
