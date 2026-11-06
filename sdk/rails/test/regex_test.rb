# frozen_string_literal: true

require "test_helper"

class RegexTest < Minitest::Test
  def setup
    @post = model do

      attribute :name, :string, index: true
      attribute :note, :string
    end

    @post.create!([
                    { _id: "1", name: "Alpha", note: "first\nbeta" },
                    { _id: "2", name: "alphabet", note: "one" },
                    { _id: "3", name: "Beta", note: "two" }
                  ])
  end

  def test_query_json
    assert_equal({ "where" => { "name" => { "$regex" => "^a", "$options" => "im" } } },
                 @post.where(name: /^a/i).to_query)
    assert_equal({ "where" => { "name" => { "$not" => { "$regex" => "x", "$options" => "m" } } } },
                 @post.where.not(name: /x/).to_query)
  end

  def test_where_with_a_regexp
    assert_equal %w[1 2], ids(@post.where(name: /^alpha/i))
    assert_equal ["2"], ids(@post.where(name: /^alpha/))
    assert_equal ["1"], ids(@post.where(note: /^beta/))
  end

  def test_where_not_with_a_regexp
    assert_equal ["3"], ids(@post.where.not(name: /^alpha/i))
  end

  def test_or_with_regexps
    relation = @post.where(name: /^Alpha$/).or(@post.where(name: /^Beta$/))

    assert_equal %w[1 3], ids(relation)
  end

  def test_where_and_count_chain
    assert_equal 1, @post.where(name: /^alpha/i).where(note: /one/).count
  end

  def test_unsupported_regexp_raises_invalid_query
    assert_raises(Ndb::InvalidQuery) { @post.where(name: /a(?=b)/) }
    assert_raises(Ndb::InvalidQuery) { @post.where.not(name: /a++/) }
  end

  private

  def ids(relation)
    relation.order(:_id).pluck(:_id)
  end
end
