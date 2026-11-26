# frozen_string_literal: true

require "test_helper"
require "json"

# The shared suite of plan §12.3: every spec/cases/**/*.json runs through
# the gem. The request goes out through the client (transport + errors).
class SharedCasesTest < Minitest::Test
  CASES_DIR = File.expand_path("../../../spec/cases", __dir__)

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
    expect = spec.fetch("expect")

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
