# frozen_string_literal: true

require "test_helper"

# One test per row of the Ruby -> JSON table (plan §12.2). No daemon call.
class RelationQueryTest < Minitest::Test
  def setup
    @post = model do
      attribute :status, :string
    end
  end

  def query(relation)
    relation.to_query
  end

  def test_equality
    assert_equal({ "query" => { "status" => "x" } }, query(@post.where(status: "x")))
  end

  def test_endless_range
    t = Time.utc(2026, 11, 1)

    assert_equal({ "query" => { "created_at" => { "$gte" => "2026-11-01T00:00:00.000Z" } } },
                 query(@post.where(created_at: t..)))
  end

  def test_beginless_ranges
    assert_equal({ "query" => { "n" => { "$lte" => 5 } } }, query(@post.where(n: ..5)))
    assert_equal({ "query" => { "n" => { "$lt" => 5 } } }, query(@post.where(n: ...5)))
  end

  def test_exclusive_and_inclusive_ranges
    assert_equal({ "query" => { "n" => { "$gte" => 1, "$lt" => 9 } } }, query(@post.where(n: 1...9)))
    assert_equal({ "query" => { "n" => { "$gte" => 1, "$lte" => 9 } } }, query(@post.where(n: 1..9)))
  end

  def test_array_is_in
    assert_equal({ "query" => { "dnis" => { "$in" => %w[1133 1144] } } }, query(@post.where(dnis: %w[1133 1144])))
  end

  def test_nil_is_exists_false
    assert_equal({ "query" => { "dnis" => { "$exists" => false } } }, query(@post.where(dnis: nil)))
  end

  def test_where_not
    assert_equal({ "query" => { "status" => { "$ne" => "x" } } }, query(@post.where.not(status: "x")))
    assert_equal({ "query" => { "s" => { "$nin" => %w[a b] } } }, query(@post.where.not(s: %w[a b])))
    assert_equal({ "query" => { "s" => { "$exists" => true } } }, query(@post.where.not(s: nil)))
    assert_equal({ "query" => { "$not" => { "n" => { "$gt" => 1 } } } }, query(@post.where.not(n: { "$gt" => 1 })))
  end

  def test_or
    rel = @post.where(status: "a").or(@post.where(status: "b"))

    assert_equal({ "query" => { "$or" => [{ "status" => "a" }, { "status" => "b" }] } }, query(rel))
  end

  def test_chained_where_is_and
    rel = @post.where(a: 1).where(b: 2)

    assert_equal({ "query" => { "a" => 1, "b" => 2 } }, query(rel))
  end

  def test_chained_where_on_the_same_field_uses_and
    rel = @post.where(n: 1..5).where(n: { "$ne" => 3 })

    assert_equal({ "query" => { "$and" => [{ "n" => { "$gte" => 1, "$lte" => 5 } }, { "n" => { "$ne" => 3 } }] } },
                 query(rel))
  end

  def test_order
    assert_equal({ "sort" => { "created_at" => -1 } }, query(@post.order(created_at: :desc)))
    assert_equal({ "sort" => { "a" => 1, "b" => -1 } }, query(@post.order(:a, b: :desc)))
  end

  def test_limit_and_offset
    assert_equal({ "limit" => 50, "skip" => 10 }, query(@post.limit(50).offset(10)))
  end

  def test_page_per
    assert_equal({ "skip" => 100, "limit" => 50 }, query(@post.page(3).per(50)))
  end

  def test_after_limit
    assert_equal({ "after" => "tok", "limit" => 50 }, query(@post.after("tok").limit(50)))
  end

  def test_group_average_pipeline
    body = @post.where(status: "x").group(:status).aggregate_body(:avg, :duration)

    assert_equal({ "pipeline" => [
                   { "$match" => { "status" => "x" } },
                   { "$group" => { "_id" => "$status", "avg" => { "$avg" => "$duration" } } }
                 ] }, body)
  end

  def test_group_count_pipeline
    body = @post.group(:status).aggregate_body(:count)

    assert_equal [{ "$group" => { "_id" => "$status", "count" => { "$count" => {} } } }], body["pipeline"]
  end

  def test_group_by_two_fields_pipeline
    body = @post.group(:a, :b).aggregate_body(:sum, :n)

    assert_equal({ "_id" => { "a" => "$a", "b" => "$b" }, "sum" => { "$sum" => "$n" } },
                 body["pipeline"].first["$group"])
  end

  def test_relations_are_immutable
    base = @post.where(a: 1)
    base.where(b: 2)

    assert_equal({ "query" => { "a" => 1 } }, query(base))
  end
end
