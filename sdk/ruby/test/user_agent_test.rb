# frozen_string_literal: true

require "test_helper"

class UserAgentTest < Minitest::Test
  SDK_AGENT = %r{\Andb-ruby/#{Regexp.escape(Ndb::VERSION)} \(ruby #{Regexp.escape(RUBY_VERSION)}; #{Regexp.escape(RUBY_PLATFORM)}\)}

  # A fake daemon: it keeps the request headers of each call and answers {}.
  def fake_server
    server = TCPServer.new("127.0.0.1", 0)
    seen = Queue.new

    thread = Thread.new do
      loop do
        sock = server.accept
        seen << read_headers(sock)

        sock.write("HTTP/1.1 200 OK\r\nContent-Type: application/json\r\nContent-Length: 2\r\nConnection: close\r\n\r\n{}")
        sock.close
      end
    rescue IOError, Errno::EBADF
      nil
    end

    @servers = (@servers || []) << [server, thread]

    ["tcp://127.0.0.1:#{server.addr[1]}", seen]
  end

  def teardown
    (@servers || []).each do |server, thread|
      server.close
      thread.kill
    end
  end

  def read_headers(sock)
    headers = {}

    sock.gets("\r\n")

    while (line = sock.gets("\r\n")) && line != "\r\n"
      name, value = line.split(":", 2)
      headers[name.downcase] = value.strip
    end

    length = headers["content-length"].to_i
    sock.read(length) if length.positive?

    headers
  end

  def test_every_request_sends_the_gem_user_agent
    url, seen = fake_server
    client = Ndb::Client.new(url: url, ready_timeout: 0)

    client.ensure_collection("ua")
    client.get("ua", "1")

    agents = [seen.pop["user-agent"], seen.pop["user-agent"]]

    agents.each { |agent| assert_match(/#{SDK_AGENT}\z/, agent) }
  end

  def test_user_agent_option_appends_the_app_token
    url, seen = fake_server
    client = Ndb.connect(url, user_agent: "myapp/1.2", ready_timeout: 0)

    client.ensure_collection("ua")

    assert_match(%r{#{SDK_AGENT} myapp/1\.2\z}, seen.pop["user-agent"])
  end

  def test_the_real_daemon_accepts_the_header
    client.ensure_collection(collection_name)

    assert_match SDK_AGENT, client.user_agent
  end
end
