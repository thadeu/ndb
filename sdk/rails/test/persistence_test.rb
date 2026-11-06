# frozen_string_literal: true

require "test_helper"

class PersistenceTest < Minitest::Test
  def setup
    @post = model do

      attribute :status, :string, index: true
      attribute :duration, :integer
      attribute :created_at, :datetime

      validates :status, presence: true
    end
  end

  def test_create_and_find
    @post.create!(_id: "c1", status: "answered", duration: 42)

    found = @post.find("c1")

    assert_equal "answered", found.status
    assert_equal 42, found.duration
    assert_predicate found, :persisted?
    refute_predicate found, :changed?
  end

  def test_a_record_loaded_by_find_detects_a_concurrent_update
    @post.create!(_id: "c1", status: "answered")

    mine = @post.find("c1")
    theirs = @post.find("c1")

    assert_equal 1, mine.version

    theirs.update!(status: "failed")

    assert_raises(Ndb::StaleObjectError) { mine.update!(status: "busy") }
    assert_equal "failed", @post.find("c1").status
  end

  def test_create_returns_an_unsaved_record_when_invalid
    post = @post.create(_id: "c1")

    refute_predicate post, :persisted?
    assert_includes post.errors[:status], "can't be blank"
    assert_equal 0, @post.count
  end

  def test_create_bang_raises_record_invalid
    error = assert_raises(Ndb::RecordInvalid) { @post.create!(_id: "c1") }

    assert_match(/Status can't be blank/, error.message)
    assert_equal 0, @post.count
  end

  def test_save_returns_false_when_invalid_and_does_not_write
    post = @post.new(_id: "c1")

    refute post.save
    assert_equal 0, @post.count
  end

  def test_generated_id_is_a_uuid_v7
    klass = model { attribute :status, :string }
    record = klass.create!(status: "x")

    assert_match(/\A[0-9a-f]{8}-[0-9a-f]{4}-7[0-9a-f]{3}-[89ab][0-9a-f]{3}-[0-9a-f]{12}\z/, record.id)
    assert_equal record.id, klass.find(record.id).id
  end

  def test_update_sends_only_dirty_fields_as_set
    post = @post.create!(_id: "c1", status: "a", duration: 1)

    sent = capture_update { post.update!(status: "b") }

    assert_equal({ "$set" => { "status" => "b" } }, sent[:ops])
    assert_equal({ "_id" => "c1", "_v" => 1 }, sent[:filter])
    assert_equal "b", @post.find("c1").status
    assert_equal 1, @post.find("c1").duration
  end

  def test_setting_nil_sends_unset
    post = @post.create!(_id: "c1", status: "a", duration: 5)

    sent = capture_update { post.update!(duration: nil) }

    assert_equal({ "$unset" => { "duration" => true } }, sent[:ops])
    assert_nil @post.find("c1").duration
  end

  def test_save_without_changes_sends_nothing
    post = @post.create!(_id: "c1", status: "a")

    sent = capture_update { post.save! }

    assert_nil sent
  end

  def test_stale_update_raises
    first = @post.create!(_id: "c1", status: "a")
    second = @post.new(_id: "c1")
    second.send(:loaded!, 1)
    second.status = "b"

    first.update!(status: "c")

    assert_raises(Ndb::StaleObjectError) { second.save! }
  end

  def test_update_uses_the_version_of_the_last_write
    post = @post.create!(_id: "c1", status: "a")

    assert_equal 1, post.version

    post.update!(status: "b")
    post.update!(status: "c")

    assert_equal 3, post.version
  end

  def test_loaded_row_version_is_used_when_the_daemon_sends_it
    record = @post.instantiate("_id" => "c1", "status" => "a", "_v" => 7)

    assert_equal 7, record.version
  end

  def test_destroy
    post = @post.create!(_id: "c1", status: "a")

    assert_equal post, post.destroy
    assert_predicate post, :destroyed?
    assert_equal 0, @post.count
  end

  def test_destroy_with_stale_version_raises
    post = @post.create!(_id: "c1", status: "a")
    post.update!(status: "b")
    post.send(:loaded!, 1)

    assert_raises(Ndb::StaleObjectError) { post.destroy }
  end

  def test_duplicate_primary_key_raises_record_not_unique
    @post.create!(_id: "c1", status: "a")

    assert_raises(Ndb::RecordNotUnique) { @post.create!(_id: "c1", status: "a") }
  end

  def test_datetime_roundtrip
    t = Time.utc(2026, 11, 1, 10, 0, 0)

    @post.create!(_id: "c1", status: "a", created_at: t)

    assert_equal t, @post.find("c1").created_at
    assert_equal 1, @post.where(created_at: (t - 60)..).count
    assert_equal 0, @post.where(created_at: (t + 60)..).count
  end

  def test_callbacks_run_in_order
    log = []
    klass = model do
      attribute :status, :string

      before_save { log << :before_save }
      before_create { log << :before_create }
      after_create { log << :after_create }
      after_save { log << :after_save }
      before_update { log << :before_update }
      before_destroy { log << :before_destroy }
      after_destroy { log << :after_destroy }
    end

    record = klass.create!(status: "a")
    record.update!(status: "b")
    record.destroy

    assert_equal %i[before_save before_create after_create after_save before_save before_update after_save
                    before_destroy after_destroy], log
  end

  def test_before_save_abort_stops_the_write
    klass = model do
      attribute :status, :string

      before_save { throw :abort }
    end

    refute klass.new(status: "a").save
    assert_raises(Ndb::RecordNotSaved) { klass.new(status: "a").save! }
    assert_equal 0, klass.count
  end

  def test_increment_and_decrement_are_atomic_writes
    post = @post.create!(_id: "c1", status: "a", duration: 10)

    assert_same post, post.increment!(:duration, 5)
    assert_equal 15, post.duration
    assert_equal 2, post.version

    post.decrement!(:duration)

    assert_equal 14, post.duration
    assert_equal 14, @post.find("c1").duration
    refute_predicate post, :changed?
  end

  def test_concurrent_increments_do_not_lose_updates
    @post.create!(_id: "c1", status: "a", duration: 0)

    20.times.map { Thread.new { @post.find("c1").increment!(:duration) } }.each(&:join)

    assert_equal 20, @post.find("c1").duration
  end

  def test_increment_needs_a_saved_record_and_a_known_attribute
    assert_raises(Ndb::RecordNotSaved) { @post.new(_id: "x").increment!(:duration) }

    post = @post.create!(_id: "c1", status: "a")

    assert_raises(ArgumentError) { post.increment!(:nope) }
  end

  def test_relation_update_and_delete
    @post.create!(_id: "c1", status: "a", duration: 1)
    @post.create!(_id: "c2", status: "a", duration: 1)
    @post.create!(_id: "c3", status: "b", duration: 1)

    assert_equal 2, @post.where(status: "a").update({ "$inc" => { "duration" => 4 } })
    assert_equal 5, @post.find("c1").duration
    assert_equal 1, @post.find("c3").duration
    assert_equal 2, @post.where(status: "a").delete
    assert_equal 1, @post.count
  end

  private

  # Records the arguments of the next ndb_update call (nil when none).
  def capture_update
    sent = nil
    original = @post.method(:ndb_update)

    @post.define_singleton_method(:ndb_update) do |filter, ops, **options|
      sent = { filter: filter, ops: ops }

      original.call(filter, ops, **options)
    end

    yield

    sent
  end
end
