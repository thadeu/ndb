# frozen_string_literal: true

require "test_helper"

class DestroyTest < Minitest::Test
  def setup
    @post = model do
      attribute :status, :string, index: true

      before_destroy { throw :abort if status == "locked" }
    end
  end

  def test_destroy_bang_returns_the_record
    @post.create!(_id: "p1", status: "open")

    post = @post.find("p1").destroy!

    assert_predicate post, :destroyed?
    assert_equal 0, @post.count
  end

  def test_destroy_bang_raises_when_a_callback_aborts
    @post.create!(_id: "p1", status: "locked")

    error = assert_raises(Ndb::RecordNotDestroyed) { @post.find("p1").destroy! }

    assert_equal "p1", error.record.id
    assert_equal 1, @post.count
  end

  def test_record_delete_skips_callbacks_and_the_version_guard
    @post.create!(_id: "p1", status: "locked")

    mine = @post.find("p1")

    @post.find("p1").update!(status: "locked2")

    assert_same mine, mine.delete
    assert_predicate mine, :destroyed?
    assert_equal 0, @post.count
  end

  def test_class_destroy_with_one_id_and_many
    @post.create!(_id: "p1", status: "open")
    @post.create!(_id: "p2", status: "open")
    @post.create!(_id: "p3", status: "open")

    assert_equal "p1", @post.destroy("p1").id
    assert_equal %w[p2 p3], @post.destroy(%w[p2 p3]).map(&:id)
    assert_equal 0, @post.count
    assert_raises(Ndb::NotFound) { @post.destroy("p1") }
  end

  def test_class_delete_counts
    @post.create!(_id: "p1", status: "locked")
    @post.create!(_id: "p2", status: "open")
    @post.create!(_id: "p3", status: "open")

    assert_equal 1, @post.delete("p1")
    assert_equal 2, @post.delete(%w[p2 p3 missing])
    assert_equal 0, @post.delete([])
    assert_equal 0, @post.count
  end

  def test_relation_destroy_all_runs_callbacks
    @post.create!(_id: "p1", status: "open")
    @post.create!(_id: "p2", status: "locked")
    @post.create!(_id: "p3", status: "open")

    destroyed = @post.where(status: %w[open locked]).destroy_all

    assert_equal 3, destroyed.size
    assert_equal ["p2"], @post.all.map(&:id)
  end

  def test_relation_delete_all_and_its_alias
    @post.create!(_id: "p1", status: "locked")
    @post.create!(_id: "p2", status: "open")
    @post.create!(_id: "p3", status: "open")

    assert_equal 1, @post.where(status: "locked").delete_all
    assert_equal 2, @post.where(status: "open").delete
    assert_equal 0, @post.count
  end

  def test_unfiltered_delete_all_is_refused
    @post.create!(_id: "p1", status: "open")

    error = assert_raises(Ndb::EmptyFilter) { @post.delete_all }

    assert_equal "empty_filter", error.code
    assert_equal 1, @post.count
  end

  def test_class_destroy_all
    @post.create!(_id: "p1", status: "open")
    @post.create!(_id: "p2", status: "locked")

    assert_equal 2, @post.destroy_all.size
    assert_equal 1, @post.count
  end
end
