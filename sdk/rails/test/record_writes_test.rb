# frozen_string_literal: true

require "test_helper"

class RecordWritesTest < Minitest::Test
  def setup
    @post = model do
      attribute :status, :string, index: true
      attribute :views, :integer
      attribute :likes, :integer
      attribute :active, :boolean
      attribute :seen_at, :datetime

      validates :status, presence: true
    end
  end

  def test_touch_sets_updated_at_and_named_times_in_one_write
    post = @post.create!(_id: "p1", status: "new")
    before = post.updated_at
    version = post.version

    sleep 0.01

    assert post.touch(:seen_at)

    stored = @post.find("p1")

    assert_operator post.updated_at, :>, before
    assert_equal post.updated_at, stored.updated_at
    assert_equal post.updated_at, stored.seen_at
    assert_operator post.version, :>, version
    refute_predicate post, :changed?
  end

  def test_touch_skips_validations_and_save_callbacks_and_runs_after_touch
    calls = []
    klass = model do
      attribute :status, :string

      validates :status, presence: true
      before_save { calls << :save }
      after_touch { calls << :touch }
    end

    post = klass.create!(_id: "p1", status: "new")

    calls.clear
    post.status = nil
    post.touch(time: Time.utc(2030, 1, 1))

    assert_equal [:touch], calls
    assert_equal Time.utc(2030, 1, 1), klass.find("p1").updated_at
    assert_equal "new", klass.find("p1").status
  end

  def test_update_columns_writes_cast_values_without_callbacks_or_updated_at
    calls = []
    klass = model do
      attribute :status, :string
      attribute :views, :integer

      validates :status, presence: true
      before_save { calls << :save }
    end

    post = klass.create!(_id: "p1", status: "new", views: 1)
    stamp = klass.find("p1").updated_at

    calls.clear

    assert post.update_columns(status: "", views: "7")
    assert post.update_column(:status, "x")

    stored = klass.find("p1")

    assert_empty calls
    assert_equal 7, stored.views
    assert_equal "x", stored.status
    assert_equal stamp, stored.updated_at
    assert_equal 7, post.views
    refute_predicate post, :changed?
  end

  def test_update_columns_needs_a_saved_record
    assert_raises(Ndb::RecordNotSaved) { @post.new(status: "a").update_columns(status: "b") }
  end

  def test_toggle_and_toggle_bang
    post = @post.create!(_id: "p1", status: "new", active: false)

    post.toggle(:active)

    assert post.active
    refute @post.find("p1").active

    post.status = nil

    assert post.toggle!(:active)
    refute @post.find("p1").active
  end

  def test_update_attribute_skips_validations
    post = @post.create!(_id: "p1", status: "new")

    assert post.update_attribute(:status, nil)
    assert_nil @post.find("p1").status
  end

  def test_saved_changes_after_create_and_update
    post = @post.create!(_id: "p1", status: "new")

    assert post.saved_change_to_status?
    assert post.saved_change_to_attribute?(:status)

    post.status = "done"

    assert post.will_save_change_to_status?

    post.save!

    assert post.saved_change_to_status?
    assert_equal "new", post.status_before_last_save
    assert_equal %w[new done], post.saved_changes["status"]
    refute post.saved_change_to_views?
    refute post.will_save_change_to_status?
  end

  def test_a_found_record_has_no_saved_changes
    @post.create!(_id: "p1", status: "new")

    assert_empty @post.find("p1").saved_changes
  end

  def test_after_commit_callbacks_run_after_the_write
    log = []
    klass = model do
      attribute :status, :string

      after_commit { log << [:commit, self.class.find_by(id: id)&.status] }
      after_create_commit { log << :create }
      after_update_commit { log << :update }
      after_save_commit { log << :save }
      after_destroy_commit { log << :destroy }
    end

    post = klass.create!(_id: "p1", status: "new")
    post.update!(status: "done")

    assert_equal [:create, :save, [:commit, "new"], :update, :save, [:commit, "done"]].sort_by(&:to_s), log.sort_by(&:to_s)
    assert_equal [:commit, "new"], log[2]

    log.clear
    post.destroy

    assert_equal [:destroy], log.reject { |e| e.is_a?(Array) }
  end

  def test_after_commit_waits_for_the_transaction_and_after_rollback
    log = []
    klass = model do
      attribute :status, :string

      after_commit { log << :commit }
      after_rollback { log << :rollback }
    end

    klass.create!(_id: "p0", status: "new")
    log.clear

    klass.transaction do
      klass.create!(_id: "p1", status: "new")

      assert_empty log
    end

    assert_equal [:commit], log

    log.clear

    klass.transaction do
      klass.create!(_id: "p2", status: "new")

      raise Ndb::Rollback
    end

    assert_equal [:rollback], log
  end

  def test_insert_all_writes_many_with_timestamps_and_no_validations
    @post.insert_all([{ id: "a", status: nil, views: "3" }, { id: "b", status: "x" }])

    a = @post.find("a")

    assert_equal 3, a.views
    refute_nil a.created_at
    refute_nil a.updated_at
    assert_equal 2, @post.count
  end

  def test_insert_all_raises_on_a_duplicate_id
    @post.create!(_id: "a", status: "x")

    assert_raises(Ndb::RecordNotUnique) { @post.insert_all!([{ id: "a", status: "y" }]) }
    assert_equal "x", @post.find("a").status
  end

  def test_update_counters_and_increment_counter
    @post.create!(_id: "a", status: "x", views: 1, likes: 0)
    @post.create!(_id: "b", status: "x", views: 1, likes: 0)
    stamp = @post.find("a").updated_at

    sleep 0.01

    @post.update_counters(%w[a b], views: 2, likes: -1)
    @post.increment_counter(:views, "a")
    @post.decrement_counter(:likes, "b")

    a = @post.find("a")
    b = @post.find("b")

    assert_equal [4, -1], [a.views, a.likes]
    assert_equal [3, -2], [b.views, b.likes]
    assert_operator a.updated_at, :>, stamp
  end

  def test_cache_key_and_version
    post = @post.create!(_id: "p1", status: "new")

    assert_equal "test_posts/new", @post.new.cache_key
    assert_equal "test_posts/p1", post.cache_key

    version = post.cache_version

    refute_nil version
    assert_equal "test_posts/p1-#{version}", post.cache_key_with_version

    post.update!(status: "done")

    refute_equal version, post.cache_version
  end
end
