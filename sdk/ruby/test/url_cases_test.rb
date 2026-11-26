# frozen_string_literal: true

require "test_helper"
require "json"

# spec/url/cases.json: the token a client takes from NDB_URL, the token
# option and NDB_TOKEN. Every client runs the same cases.
class UrlCasesTest < Minitest::Test
  CASES = File.expand_path("../../../spec/url/cases.json", __dir__)

  JSON.parse(File.read(CASES)).fetch("cases").each do |c|
    define_method("test_#{c["name"].tr(" -", "__")}") { run_case(c) }
  end

  def setup
    @saved_env = ENV.to_h.slice("NDB_URL", "NDB_TOKEN")
  end

  def teardown
    ENV.delete("NDB_URL")
    ENV.delete("NDB_TOKEN")
    ENV.update(@saved_env)
  end

  private

  def run_case(c)
    c["env"] ? ENV["NDB_TOKEN"] = c["env"] : ENV.delete("NDB_TOKEN")

    secret = c["secret"]

    if c.dig("want", "error")
      error = assert_raises(ArgumentError) { Ndb::Client.new(url: c["url"], token: c["option"]) }

      refute_includes error.message, secret if secret

      return
    end

    client = Ndb::Client.new(url: c["url"], token: c["option"])

    want = c.dig("want", "token")
    token = client.instance_variable_get(:@token)

    want.nil? ? assert_nil(token) : assert_equal(want, token)

    database = c.dig("want", "database")

    database.nil? ? assert_nil(client.endpoint.database) : assert_equal(database, client.endpoint.database)

    return unless secret

    refute_includes client.inspect, secret
    refute_includes client.endpoint.inspect, secret
    refute_includes client.endpoint.to_s, secret
  end
end
