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
    when "find" then client.find(coll, request["body"])
    when "aggregate" then aggregate(coll, request["body"])
    else flunk "request op #{request['op'].inspect} is not supported"
    end
  end

  def aggregate(coll, body)
    client.aggregate(coll, body)
  rescue Ndb::Error => e
    skip "daemon has no /aggregate endpoint yet (HTTP #{e.status})" if e.code == "http_status" && [404, 405].include?(e.status)

    raise
  end

  def write(coll, step, path)
    body = step["body"]

    case step["op"]
    when "insert" then client.insert(coll, body["insert"])
    when "update" then update(coll, body["update"])
    when "delete" then client.delete(coll, body["delete"]["id"], if_version: body["delete"]["if_version"])
    else flunk "#{path}: setup op #{step['op'].inspect} is not supported"
    end
  end

  def update(coll, change)
    client.update(coll, change["id"], set: change["set"] || {}, unset: change["unset"] || [],
                                      if_version: change["if_version"])
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
