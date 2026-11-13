# frozen_string_literal: true

require "test_helper"

class RelationApiTest < Minitest::Test
  def setup
    @post = model do
      attribute :status, :string, index: true
      attribute :n, :integer
      attribute :tag, :string

      validates :n, presence: true
    end

    3.times { |i| @post.create!(_id: "p#{i}", status: "y", n: i, tag: "t#{i % 2}") }
  end

  def test_update_all
    assert_equal 3, @post.where(status: "y").update_all(status: "z")
    assert_equal 3, @post.where(status: "z").count
    assert_equal 1, @post.where(status: "z").limit(1).update_all("$inc" => { "n" => 10 })
  end

  def test_new_build_and_create_take_the_where
    scope = @post.where(status: "new", n: [1, 2])

    record = scope.new(tag: "a")

    assert_equal "new", record.status
    assert_nil record.n
    assert_equal "a", record.tag
    refute_predicate record, :persisted?
    assert_equal "new", scope.build.status

    created = @post.where(status: "new").create(n: 5)

    assert_predicate created, :persisted?
    assert_equal "new", @post.find(created.id).status
    assert_raises(Ndb::RecordInvalid) { @post.where(status: "new").create! }
    refute_predicate @post.where(status: "new").create, :persisted?
  end

  def test_find_or_create_by_on_a_relation
    scope = @post.where(status: "q")

    first = scope.find_or_create_by(n: 7)
    again = scope.find_or_create_by(n: 7)

    assert_equal "q", first.status
    assert_equal first.id, again.id
    assert_equal "q", scope.find_or_initialize_by(n: 8).status
    assert_equal "q", scope.find_or_create_by!(n: 9).status
  end

  def test_bang_finders_and_take
    assert_equal "p0", @post.first!.id
    assert_equal "p2", @post.last!.id
    assert_equal 1, @post.take(1).size
    assert_includes %w[p0 p1 p2], @post.take.id
    assert_equal "p1", @post.where(n: 1).take!.id
    assert_nil @post.where(n: 9).take

    assert_raises(Ndb::NotFound) { @post.where(n: 9).first! }
    assert_raises(Ndb::NotFound) { @post.where(n: 9).last! }
    assert_raises(Ndb::NotFound) { @post.where(n: 9).take! }
  end

  def test_ids_and_pick
    assert_equal %w[p0 p1 p2], @post.order(:n).ids
    assert_equal %w[p2], @post.where(n: 2).ids
    assert_equal 2, @post.order(n: :desc).pick(:n)
    assert_equal ["p0", 0], @post.order(:n).pick(:_id, :n)
    assert_nil @post.where(n: 9).pick(:n)
  end

  def test_none_sends_no_request
    none = @post.where(status: "y").none

    assert_equal [], none.to_a
    assert_equal 0, none.count
    assert_equal 0, none.update(status: "x")
    assert_equal 0, none.delete_all
    assert_equal [], none.pluck(:n)
    assert_equal [], none.ids
    refute none.exists?
    assert_equal 0, none.sum(:n)
    assert_equal 0, @post.none.count
    assert_equal 3, @post.count
  end

  def test_sole_and_find_sole_by
    assert_equal "p1", @post.find_sole_by(n: 1).id
    assert_equal "p1", @post.where(n: 1).sole.id
    assert_raises(Ndb::NotFound) { @post.find_sole_by(n: 9) }
    assert_raises(Ndb::SoleRecordExceeded) { @post.find_sole_by(status: "y") }
  end

  def test_destroy_by_and_delete_by
    assert_equal 1, @post.destroy_by(n: 0).size
    assert_equal 1, @post.delete_by(id: "p1")
    assert_equal %w[p2], @post.ids
  end

  def test_reorder_rewhere_unscope
    assert_equal [2, 1, 0], @post.order(:n).reorder(n: :desc).pluck(:n)
    assert_equal [1], @post.where(n: 0).rewhere(n: 1).pluck(:n)
    assert_equal 3, @post.where(n: 0).unscope(:where).count
    assert_equal 3, @post.limit(1).unscope(:limit).count
    assert_equal [0, 1, 2], @post.order(n: :desc).unscope(:order).order(:n).pluck(:n)
    assert_raises(ArgumentError) { @post.unscope(:joins) }
  end

  def test_distinct_pluck
    assert_equal %w[t0 t1], @post.order(:tag).distinct.pluck(:tag)
    assert_equal %w[t0 t1 t0], @post.order(:n).pluck(:tag)
  end

  def test_select_loads_only_those_fields
    record = @post.order(:n).select(:n).first

    assert_equal 0, record.n
    assert_equal "p0", record.id
    assert_nil record.status
    assert_equal [1], @post.select { |post| post.n == 1 }.map(&:n)
  end
end
