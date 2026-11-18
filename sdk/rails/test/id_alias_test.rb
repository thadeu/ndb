# frozen_string_literal: true

require "test_helper"

# id is an alias of _id in a query too, as in ActiveRecord.
class IdAliasTest < Minitest::Test
  def setup
    @post = model do
      attribute :status, :string, index: true
    end

    @post.create!(id: "p1", status: "open")
    @post.create!(id: "p2", status: "done")
  end

  def test_where_id_reads_id
    assert_equal ["p1"], @post.where(id: "p1").pluck(:_id)
    assert_equal %w[p1 p2], @post.where(id: %w[p1 p2]).order(:_id).pluck(:_id)
  end

  def test_find_by_id
    assert_equal "p1", @post.find_by(id: "p1", status: "open").id
    assert_nil @post.find_by(id: "p1", status: "done")
  end

  def test_where_not_id
    assert_equal ["p2"], @post.where.not(id: "p1").pluck(:_id)
  end

  def test_exists_takes_an_id_or_conditions
    assert @post.exists?("p1")
    refute @post.exists?("nope")
    assert @post.exists?(status: "done")
    assert @post.exists?(id: "p2")
    refute @post.where(status: "open").exists?("p2")
    refute @post.exists?(false)
    assert @post.exists?
  end

  # pluck, pick, select, order and group read id as _id too: pluck(:id) gave nil for each row,
  # and a scope built from it (where(app_id: App.pluck(:id))) matched nothing.
  def test_pluck_and_pick_read_id
    assert_equal %w[p1 p2], @post.order(:_id).pluck(:id)
    assert_equal [%w[p1 open], %w[p2 done]], @post.order(:_id).pluck(:id, :status)
    assert_equal "p2", @post.where(status: "done").pick(:id)
  end

  def test_order_by_id
    assert_equal %w[p2 p1], @post.order(id: :desc).pluck(:id)
    assert_equal %w[p2 p1], @post.order("id DESC").pluck(:id)
    assert_equal %w[p1 p2], @post.order(:id).pluck(:id)
  end

  def test_select_id
    assert_equal %w[p1 p2], @post.select(:id, :status).order(:id).map(&:id)
  end

  def test_group_by_id
    assert_equal({ "p1" => 1, "p2" => 1 }, @post.group(:id).count)
  end

  def test_a_model_with_an_id_attribute_keeps_it
    legacy = model do
      attribute :id, :string, index: true
    end

    legacy.create!(_id: "a", id: "x")

    assert_equal ["a"], legacy.where(id: "x").pluck(:_id)
    assert_equal ["x"], legacy.pluck(:id)
    assert_equal ["x"], legacy.order(:id).pluck(:id)
  end
end
