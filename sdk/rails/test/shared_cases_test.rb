# frozen_string_literal: true

require "test_helper"
require "json"

# The shared suite of plan §12.3: every spec/cases/**/*.json runs through
# the gem. The request goes out through the client (transport + errors). When
# the body is something a Relation can say, the Relation must build the same
# JSON, so the model layer is checked against the case too.
class SharedCasesTest < Minitest::Test
  CASES_DIR = File.expand_path("../../../spec/cases", __dir__)
  RELATION_KEYS = %w[query sort limit skip after].freeze

  Dir[File.join(CASES_DIR, "**", "*.json")].sort.each do |path|
    spec = JSON.parse(File.read(path))
    name = path.delete_prefix("#{CASES_DIR}/").delete_suffix(".json").gsub(/\W+/, "_")

    define_method("test_#{name}") { run_case(spec, path) }
  end

  def test_there_are_cases
    refute_empty Dir[File.join(CASES_DIR, "**", "*.json")]
  end

  private

  def run_case(spec, path)
    coll = NdbTestHelpers.next_name

    client.ensure_collection(coll, spec.dig("setup", "catalog") || {})
    Array(spec.dig("setup", "writes")).each { |step| write(coll, step, path) }

    request = spec.fetch("request")
    body = request.fetch("body")
    expect = spec.fetch("expect")

    assert_relation_builds(coll, spec, body) if request["op"] == "search"

    if expect["error"]
      error = assert_raises(Ndb::Error, path) { send_request(coll, request) }

      assert_equal expect["error"], error.code, path
    else
      compare(expect, send_request(coll, request).rows, path)
    end
  end

  def send_request(coll, request)
    case request["op"]
    when "search" then client_in(request["database"]).search(coll, request["body"])
    when "aggregate" then aggregate(client_in(request["database"]), coll, request["body"])
    else flunk "request op #{request['op'].inspect} is not supported"
    end
  end

  def aggregate(db, coll, body)
    db.aggregate(coll, body)
  rescue Ndb::Error => e
    skip "daemon has no /aggregate endpoint yet (HTTP #{e.status})" if e.code == "http_status" && [404, 405].include?(e.status)

    raise
  end

  def write(coll, step, path)
    body = step["body"]

    case step["op"]
    when "bulk" then client_in(step["database"]).bulk(coll, body["docs"])
    else flunk "#{path}: setup op #{step['op'].inspect} is not supported"
    end
  end

  def assert_relation_builds(coll, spec, body)
    return unless (body.keys - RELATION_KEYS).empty?
    return if contains_null?(body["query"])

    klass = model(coll)
    relation = klass.where(body["query"] || {})
    relation = relation.order(*body["sort"].map { |f, d| { f => d == -1 ? :desc : :asc } }) if body["sort"]
    relation = relation.limit(body["limit"]) if body["limit"]
    relation = relation.offset(body["skip"]) if body["skip"]
    relation = relation.after(body["after"]) if body["after"]

    expected = body.reject { |key, value| key == "query" && value.empty? }

    assert_equal canonical(expected), canonical(relation.to_query), spec["name"]
  end

  # {"a": null} is an equality with null; Ruby nil means "$exists": false.
  def contains_null?(value)
    case value
    when nil then true
    when Hash then value.each_value.any? { |v| contains_null?(v) }
    when Array then value.any? { |v| contains_null?(v) }
    else false
    end
  end

  # The client of a database of the case: the same socket with ?db=, the
  # way an app names its database in NDB_URL. nil is the default database.
  def client_in(database)
    return client if database.nil?

    @clients ||= {}
    @clients[database] ||= Ndb::Client.new(url: "unix://#{TestDaemon.socket}?db=#{database}", timeout: 10)
  end

  def compare(expect, rows, path)
    want = expect["rows"] || []

    if expect["ordered"]
      assert_equal want.map { |r| canonical(r) }, rows.map { |r| canonical(r) }, path
    else
      assert_equal want.map { |r| canonical(r) }.sort, rows.map { |r| canonical(r) }.sort, path
    end
  end

  # Sorted keys, so the order of the keys does not matter.
  def canonical(value)
    JSON.generate(deep_sort(value))
  end

  def deep_sort(value)
    case value
    when Hash then value.sort.to_h { |k, v| [k, deep_sort(v)] }
    when Array then value.map { |v| deep_sort(v) }
    else value
    end
  end
end
