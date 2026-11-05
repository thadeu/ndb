# frozen_string_literal: true

require "test_helper"
require "tmpdir"

class TcpTest < Minitest::Test
  def self.plain = (@plain ||= TcpDaemon.new)
  def self.secured = (@secured ||= TcpDaemon.new(token: TcpDaemon::TOKEN))

  def setup
    @coll = NdbTestHelpers.next_name
    @saved_env = ENV.to_h.slice("NDB_URL", "NDB_TOKEN")

    ENV.delete("NDB_URL")
    ENV.delete("NDB_TOKEN")
  end

  def teardown
    ENV.delete("NDB_URL")
    ENV.delete("NDB_TOKEN")
    ENV.update(@saved_env)
  end

  def plain = self.class.plain
  def secured = self.class.secured

  def test_roundtrip_over_tcp
    tcp = NdbRecord::Client.new(url: plain.url)

    tcp.ensure_collection(@coll, id: "cid", indexes: ["a"])
    tcp.insert(@coll, [{ cid: "1", a: "x" }])

    assert_equal [{ "cid" => "1", "a" => "x" }], tcp.find(@coll, where: { a: "x" }).rows
  end

  def test_keep_alive_reuses_one_connection_over_tcp
    tcp = NdbRecord::Client.new(url: plain.url)

    tcp.ensure_collection(@coll, id: "cid")
    20.times { tcp.find(@coll, {}) }

    assert_equal 1, tcp.pool.created
  end

  def test_threads_share_the_pool_over_tcp
    tcp = NdbRecord::Client.new(url: plain.url)

    tcp.ensure_collection(@coll, id: "cid")
    tcp.insert(@coll, [{ cid: "1" }])

    counts = 8.times.map { Thread.new { 25.times.map { tcp.find(@coll, {}).rows.size }.sum } }.map(&:value)

    assert_equal [25] * 8, counts
    assert_operator tcp.pool.created, :<=, 8
  end

  def test_host_name_is_resolved_on_connect
    tcp = NdbRecord::Client.new(url: "tcp://localhost:#{plain.port}")

    assert_equal [], tcp.collections.select { |c| c["name"] == "none" }
  end

  def test_reconnects_after_the_daemon_restarts
    tcp = NdbRecord::Client.new(url: plain.url)

    tcp.ensure_collection(@coll, id: "cid")
    tcp.insert(@coll, [{ cid: "1" }])

    plain.stop

    assert_raises(NdbRecord::ConnectionError) { tcp.find(@coll, {}) }

    plain.start

    assert_equal [{ "cid" => "1" }], tcp.find(@coll, {}).rows
  end

  def test_connection_refused_is_connection_error
    dead = NdbRecord::Client.new(url: "tcp://127.0.0.1:1")

    assert_raises(NdbRecord::ConnectionError) { dead.find("x", {}) }
  end

  def test_token_success
    tcp = NdbRecord::Client.new(url: secured.url, token: TcpDaemon::TOKEN)

    assert_kind_of Array, tcp.collections
  end

  def test_wrong_or_missing_token_is_unauthorized
    [NdbRecord::Client.new(url: secured.url, token: "nope"), NdbRecord::Client.new(url: secured.url)].each do |tcp|
      error = assert_raises(NdbRecord::Unauthorized) { tcp.collections }

      assert_equal "unauthorized", error.code
      assert_equal 401, error.status
    end
  end

  def test_token_falls_back_to_env_and_option_wins
    ENV["NDB_TOKEN"] = TcpDaemon::TOKEN

    assert_kind_of Array, NdbRecord::Client.new(url: secured.url).collections
    assert_raises(NdbRecord::Unauthorized) { NdbRecord::Client.new(url: secured.url, token: "nope").collections }
  end

  def test_blob_upload_sends_the_token
    tcp = NdbRecord::Client.new(url: secured.url, token: TcpDaemon::TOKEN)

    assert tcp.put_blob(StringIO.new("hello"), "text/plain")
    assert_raises(NdbRecord::Unauthorized) { NdbRecord::Client.new(url: secured.url).put_blob(StringIO.new("hello")) }
  end

  def test_unix_socket_needs_no_token
    unix = NdbRecord::Client.new(socket: secured.socket)

    assert_kind_of Array, unix.collections
  end

  def test_ndb_url_fallback_tcp_and_unix
    ENV["NDB_URL"] = plain.url

    assert_kind_of Array, NdbRecord::Client.new.collections

    ENV["NDB_URL"] = "unix://#{plain.socket}"

    assert_kind_of Array, NdbRecord::Client.new.collections
  end

  def test_explicit_address_wins_over_ndb_url
    ENV["NDB_URL"] = "tcp://127.0.0.1:1"

    assert_kind_of Array, NdbRecord::Client.new(url: plain.url).collections
    assert_kind_of Array, NdbRecord::Client.new(socket: plain.socket).collections
  end

  def test_bad_address_is_rejected
    ["http://127.0.0.1:7070", "tcp://", "unix://", "tcp://host"].each do |bad|
      assert_raises(ArgumentError, bad) { NdbRecord::Client.new(url: bad) }
    end
  end

  def test_configuration_reads_url_and_token_from_yml
    Dir.mktmpdir do |dir|
      path = File.join(dir, "ndb.yml")

      File.write(path, "production:\n  url: tcp://ndb.internal:7070\n  token: abc\n")

      config = NdbRecord::Configuration.new.load_file(path, "production")

      assert_equal "tcp://ndb.internal:7070", config.url
      assert_equal "abc", config.token
    end
  end

  def test_configuration_builds_the_client_with_url_and_token
    config = NdbRecord::Configuration.new

    config.url = secured.url
    config.token = TcpDaemon::TOKEN

    client = NdbRecord::Client.new(url: config.url, token: config.token)

    assert_kind_of Array, client.collections
  end
end
