# frozen_string_literal: true

require "test_helper"

class RegexTest < Minitest::Test
  def setup
    @coll = collection_name

    client.ensure_collection(@coll, id: "cid", indexes: ["name"])
    client.insert(@coll, [
                    { cid: "1", name: "Alpha", note: "first\nbeta line", tags: %w[red Green] },
                    { cid: "2", name: "alphabet", note: "one line", tags: %w[blue] },
                    { cid: "3", name: "Beta", note: "a\nb", tags: [] }
                  ])
  end

  def test_ignore_case
    assert_equal %w[1 2], ids(name: /^alpha/i)
    assert_equal ["2"], ids(name: /^alpha/)
  end

  def test_caret_matches_at_each_line_like_ruby
    assert_equal ["1"], ids(note: /^beta/)
    assert_equal %w[1 2], ids(note: /line$/)
  end

  def test_multiline_flag_lets_dot_match_a_newline
    assert_empty ids(note: /first.beta/)
    assert_equal ["1"], ids(note: /first.beta/m)
  end

  def test_extended_flag
    assert_equal ["2"], ids(name: / ^ alpha b /x)
  end

  def test_regexp_as_the_value_of_dollar_regex_and_in_logic
    assert_equal ["2"], ids(name: { "$regex" => /bet$/i })
    assert_equal ["3"], ids("$and" => [{ name: /^b/i }, { note: /b$/ }])
    assert_equal %w[1 3], ids("$or" => [{ name: /^Alpha$/ }, { name: /^Beta$/ }])
    assert_equal ["1"], ids("$not" => { name: { "$not" => { "$regex" => /^Alpha$/ } } })
  end

  def test_array_field_matches_any_element
    assert_equal ["1"], ids(tags: /^g/i)
  end

  def test_string_pattern_is_sent_unchanged_and_uses_the_index
    page = client.find(@coll, where: { name: { "$regex" => "^alpha" } })

    assert_equal ["2"], page.rows.map { |r| r["cid"] }
    assert_equal "index:name", page.plan
  end

  def test_regexp_with_i_does_not_use_the_prefix_index
    page = client.find(@coll, where: { name: /^alpha/i })

    refute_equal "index:name", page.plan
  end

  def test_watch_where_accepts_a_regexp
    seen = []

    client.watch(@coll, "rx", where: { name: /^beta$/i }, limit: 1) { |ev| seen << ev["id"] }

    assert_equal ["3"], seen
  end

  def test_unsupported_features_fail_before_any_request
    dead = Ndb::Client.new(socket: "/tmp/ndb-no-such-regex.sock", ready_timeout: 0)

    [/a(?=b)/, /(?<!a)b/, /(a)\1/, /\h/, /a++/, /(?>a)/, /\R/].each do |regexp|
      error = assert_raises(Ndb::InvalidQuery, regexp.inspect) { dead.find(@coll, where: { name: regexp }) }

      assert_equal "invalid_query", error.code
    end

    assert_raises(Ndb::InvalidQuery) { Ndb::Conditions.equality(name: /a(?!b)/) }
    assert_raises(Ndb::InvalidQuery) { Ndb::Conditions.negation(name: Regexp.new('(a)\1')) }
  end

  def test_escaped_look_alikes_are_allowed
    assert_equal({ "$regex" => 'a\\\\1', "$options" => "m" }, Ndb::Conditions.regex(/a\\1/))
    assert_equal "m", Ndb::Conditions.regex(/\++[(?=]/)["$options"]
  end

  private

  def ids(where)
    client.find(@coll, where: where, sort: { cid: 1 }).rows.map { |r| r["cid"] }
  end
end
