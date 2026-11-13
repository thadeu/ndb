# frozen_string_literal: true

require "test_helper"

class RelationWindowTest < Minitest::Test
  def setup
    @post = model do
      attribute :status, :string, index: true
      attribute :n, :integer
    end

    5.times { |i| @post.create!(_id: "p#{i}", status: "y", n: i) }
  end

  def test_delete_all_respects_limit
    assert_equal 2, @post.where(status: "y").limit(2).delete_all
    assert_equal 3, @post.count
  end

  def test_update_respects_limit_and_order
    assert_equal 1, @post.where(status: "y").order(n: :desc).limit(1).update(n: 99)
    assert_equal [0, 1, 2, 3, 99], @post.order(:n).pluck(:n)
  end

  def test_offset_narrows_a_write
    assert_equal 2, @post.where(status: "y").order(:n).offset(3).delete_all
    assert_equal [0, 1, 2], @post.order(:n).pluck(:n)
  end

  def test_batches_write_only_their_rows
    deleted = []

    @post.where(status: "y").in_batches(of: 2) { |batch| deleted << batch.delete_all }

    assert_equal [2, 2, 1], deleted
  end

  def test_batch_update_touches_its_rows
    counts = []

    @post.where(status: "y").in_batches(of: 2) { |batch| counts << batch.update("$inc" => { "n" => 10 }) }

    assert_equal [2, 2, 1], counts
    assert_equal [10, 11, 12, 13, 14], @post.order(:n).pluck(:n)
  end

  def test_destroy_all_respects_limit
    assert_equal 2, @post.where(status: "y").limit(2).destroy_all.size
    assert_equal 3, @post.count
  end

  def test_count_respects_limit
    assert_equal 2, @post.where(status: "y").limit(2).count
    assert_equal 2, @post.where(status: "y").offset(3).count
    assert_equal 5, @post.where(status: "y").limit(10).count
    assert_equal 2, @post.limit(2).size
    refute @post.offset(5).exists?
    assert @post.offset(4).exists?
  end

  def test_batch_count_is_the_batch
    counts = []

    @post.in_batches(of: 2) { |batch| counts << batch.count }

    assert_equal [2, 2, 1], counts
  end

  def test_order_takes_a_string
    assert_equal [4, 3, 2, 1, 0], @post.order("n DESC").pluck(:n)
    assert_equal [0, 1, 2, 3, 4], @post.order("n").pluck(:n)
    assert_equal [4, 3, 2, 1, 0], @post.order("status asc, n desc").pluck(:n)
    assert_equal({ "status" => 1, "n" => -1 }, @post.order("status ASC, n desc").values[:order])
  end

  def test_order_refuses_an_expression
    assert_raises(ArgumentError) { @post.order("LOWER(n)") }
    assert_raises(ArgumentError) { @post.order("n desc nulls last") }
  end
end
