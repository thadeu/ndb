# frozen_string_literal: true

require "test_helper"

class SearchTest < Minitest::Test
  def setup
    @call = model do
      attribute :status, :string, index: true
      attribute :dur, :integer
      attribute :tags
    end

    @call.create!(_id: "c1", status: "answered", dur: 10, tags: %w[a b])
    @call.create!(_id: "c2", status: "failed", dur: 40, tags: %w[b])
    @call.create!(_id: "c3", status: "answered", dur: 50, tags: %w[a])
  end

  def test_search_takes_a_whole_body_and_pages
    body = JSON.parse('{"query":{"status":"answered"},"sort":{"dur":-1},"limit":1,"count":true}')
    page = @call.search(body)

    assert_equal ["c3"], page.records.map(&:id)
    assert_equal 2, page.total
    assert page.records.first.version

    rest = @call.search(body.merge("after" => page.next_cursor, "count" => false))

    assert_equal ["c1"], rest.records.map(&:id)
    assert_nil rest.next_cursor
  end

  def test_search_takes_symbol_keys
    assert_equal %w[c2 c3], @call.search(query: { dur: { "$gte" => 30 } }, sort: { _id: 1 }).records.map(&:id)
  end

  def test_where_raw_keeps_json_meaning_and_chains
    form = JSON.parse('{"status":{"$in":["answered","failed"]},"dur":{"$gte":30}}')

    assert_equal %w[c2 c3], @call.where_raw(form).order(:_id).map(&:id)

    # An Array is the whole array (JSON), not $in (the where convention).
    assert_equal ["c2"], @call.where_raw("tags" => ["b"]).map(&:id)
    assert_equal %w[c1 c2], @call.where(tags: ["b"]).order(:_id).map(&:id)

    assert_equal ["c3"], @call.where_raw("status" => "answered").where(tags: "a").where(dur: 20..).map(&:id)
  end

  def test_bad_input
    assert_raises(ArgumentError) { @call.search("x") }
    assert_raises(ArgumentError) { @call.where_raw([1]) }
    assert_raises(Ndb::InvalidQuery) { @call.search("query" => { "dur" => { "$bad" => 1 } }) }
  end
end
