# frozen_string_literal: true

require "test_helper"

class RelationTest < Minitest::Test
  def setup
    @post = model do

      attribute :status, :string, index: true
      attribute :n, :integer
    end

    @post.create!((1..10).map { |i| { _id: "p#{i}", status: i.odd? ? "odd" : "even", n: i } })
  end

  def test_relation_is_lazy
    relation = @post.where(status: "odd")

    refute_predicate relation, :loaded?

    relation.to_a

    assert_predicate relation, :loaded?
  end

  def test_where_order_limit
    ids = @post.where(status: "odd").order(n: :desc).limit(2).map(&:_id)

    assert_equal %w[p9 p7], ids
  end

  def test_where_variants_against_the_daemon
    assert_equal 4, @post.where(n: 3..6).count
    assert_equal 3, @post.where(n: 3...6).count
    assert_equal 2, @post.where(n: ..2).count
    assert_equal 2, @post.where(_id: %w[p1 p2]).count
    assert_equal 0, @post.where(missing: nil).count - 10
    assert_equal 5, @post.where.not(status: "odd").count
    assert_equal 3, @post.where(n: 1).or(@post.where(n: 2)).or(@post.where(n: 3)).count
  end

  def test_first_find_and_find_by
    assert_equal "p1", @post.order(n: :asc).first._id
    assert_equal "p3", @post.find("p3")._id
    assert_equal %w[p1 p2], @post.find(%w[p1 p2]).map(&:_id).sort
    assert_equal "p4", @post.find_by(n: 4)._id
    assert_nil @post.find_by(n: 99)
    assert_raises(Ndb::NotFound) { @post.find("nope") }
    assert_raises(Ndb::NotFound) { @post.find_by!(n: 99) }
  end

  def test_last_asks_the_daemon_for_one_row
    bodies = spy_finds

    assert_equal "p10", @post.order(:n).last._id
    assert_equal %w[p9 p10], @post.order(:n).last(2).map(&:_id)
    assert_equal "p9", @post.where(status: "odd").order(:n).last._id
    assert_equal "p1", @post.order(n: :desc).last._id
    assert_nil @post.where(n: 99).last

    assert(bodies.all? { |body| body["limit"] == 1 || body["limit"] == 2 }, "last loaded the relation: #{bodies}")
    assert_equal({ "n" => -1 }, bodies.first["sort"])
  end

  def test_first_and_last_with_no_order_use_the_primary_key
    bodies = spy_finds

    # The ids sort as text: p1, p10, p2, ... p9.
    assert_equal "p1", @post.first._id
    assert_equal "p9", @post.last._id
    assert_equal %w[p8 p9], @post.last(2).map(&:_id)

    assert_equal [{ "_id" => 1 }, { "_id" => -1 }, { "_id" => -1 }], bodies.map { |body| body["sort"] }
  end

  def test_last_of_a_window_or_a_loaded_relation_uses_the_rows
    assert_equal "p3", @post.order(:n).limit(3).last._id
    assert_equal "p5", @post.order(:n).offset(2).limit(3).last._id

    loaded = @post.order(:n).load

    assert_equal "p10", loaded.last._id
  end

  def test_find_by_asks_the_daemon_for_one_row
    bodies = spy_finds

    assert_equal "p4", @post.find_by(n: 4)._id
    assert_equal "p4", @post.find_by!(n: 4)._id
    assert_equal "p3", @post.find("p3")._id

    assert_equal [1, 1], bodies.first(2).map { |body| body["limit"] }
  end

  def test_exists_pluck_count
    assert @post.where(n: 1).exists?
    refute @post.where(n: 99).exists?
    assert_equal [1, 2, 3], @post.order(:n).limit(3).pluck(:n)
    assert_equal [["p1", 1], ["p2", 2]], @post.order(:n).limit(2).pluck(:_id, :n)
    assert_equal 10, @post.count
  end

  def test_page_per
    assert_equal [4, 5, 6], @post.order(:n).page(2).per(3).map(&:n)
    assert_equal [10], @post.order(:n).page(4).per(3).map(&:n)
  end

  def test_offset
    assert_equal [9, 10], @post.order(:n).offset(8).map(&:n)
  end

  def test_after_cursor_and_next_cursor
    first = @post.order(:n).limit(4)

    assert_equal [1, 2, 3, 4], first.map(&:n)

    cursor = first.next_cursor
    second = @post.order(:n).after(cursor).limit(4)

    assert_equal [5, 6, 7, 8], second.map(&:n)

    last = @post.order(:n).after(second.next_cursor).limit(4)

    assert_equal [9, 10], last.map(&:n)
    assert_nil last.next_cursor
  end

  def test_find_each_walks_every_page
    seen = []

    @post.order(:n).find_each(batch_size: 3) { |p| seen << p.n }

    assert_equal (1..10).to_a, seen
  end

  def test_find_each_without_block_is_an_enumerator
    assert_equal 10, @post.find_each(batch_size: 4).count
  end

  def test_in_batches_yields_loaded_relations
    sizes = @post.order(:n).in_batches(of: 4).map { |batch| batch.to_a.size }

    assert_equal [4, 4, 2], sizes
  end

  def test_where_with_a_string_is_rejected
    assert_raises(ArgumentError) { @post.where("n > 1") }
  end

  def test_invalid_operator_is_an_invalid_query
    assert_raises(Ndb::InvalidQuery) { @post.where(n: { "$bogus" => 1 }).to_a }
  end

  def test_unknown_rows_fields_are_ignored
    klass = model(@post.collection_name) {}

    assert_equal 10, klass.count
    assert_equal 10, klass.to_a.size
  end

  private

  # Records the find bodies the model sends to the daemon.
  def spy_finds
    bodies = []
    klass = @post
    original = klass.method(:ndb_find)

    klass.define_singleton_method(:ndb_find) do |body|
      bodies << body

      original.call(body)
    end

    bodies
  end
end
