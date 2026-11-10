# frozen_string_literal: true

require "test_helper"

class RetryTest < Minitest::Test
  NOT_READY = '{"error":{"code":"not_ready","message":"ndb is recovering"}}'

  # A tiny HTTP server: the block gets the client socket and the call number.
  def fake_server(&handler)
    server = TCPServer.new("127.0.0.1", 0)
    calls = Queue.new
    count = 0

    thread = Thread.new do
      loop do
        sock = server.accept
        count += 1
        calls << count

        handler.call(sock, count)
      end
    rescue IOError, Errno::EBADF
      nil
    end

    @servers = (@servers || []) << [server, thread]

    ["tcp://127.0.0.1:#{server.addr[1]}", calls]
  end

  def teardown
    (@servers || []).each do |server, thread|
      server.close
      thread.kill
    end
  end

  def answer(sock, status, body)
    while (line = sock.gets("\r\n")) && line != "\r\n"; end

    sock.write("HTTP/1.1 #{status}\r\nContent-Type: application/json\r\nContent-Length: #{body.bytesize}\r\nConnection: close\r\n\r\n#{body}")
    sock.close
  end

  def test_waits_for_the_daemon_to_start
    dir = Dir.mktmpdir("ndbr", "/tmp")
    sock = File.join(dir, "s.sock")
    pid = nil
    starter = Thread.new do
      sleep 0.3

      pid = Process.spawn(TestDaemon.binary, "-config", File.join(dir, "absent.yml"), "-data-dir", File.join(dir, "d"), "-unix", sock, "-tcp", "", out: File::NULL, err: File::NULL)
    end

    result = Ndb::Client.new(socket: sock, timeout: 5).collections

    assert_kind_of Array, result
  ensure
    starter&.join

    if pid
      Process.kill("TERM", pid)
      Process.wait(pid)
    end

    FileUtils.rm_rf(dir)
  end

  def test_not_ready_twice_then_ok
    url, = fake_server do |sock, n|
      n <= 2 ? answer(sock, "503 Service Unavailable", NOT_READY) : answer(sock, "200 OK", '{"collections":[]}')
    end

    assert_equal [], Ndb::Client.new(url: url).collections
  end

  def test_deadline_surfaces_the_original_error_class
    url, = fake_server { |sock, _| answer(sock, "503 Service Unavailable", NOT_READY) }

    error = assert_raises(Ndb::NotReady) { Ndb::Client.new(url: url, ready_timeout: 0.3).collections }

    assert_equal "not_ready", error.code
  end

  def test_connect_error_surfaces_as_connection_error_at_deadline
    client = Ndb::Client.new(socket: "/tmp/ndb-missing-#{Process.pid}.sock", ready_timeout: 0.2)

    assert_raises(Ndb::ConnectionError) { client.collections }
  end

  def test_zero_disables_the_retry
    url, calls = fake_server { |sock, _| answer(sock, "503 Service Unavailable", NOT_READY) }

    assert_raises(Ndb::NotReady) { Ndb::Client.new(url: url, ready_timeout: 0).collections }
    assert_equal 1, calls.size
  end

  def test_write_dropped_after_send_is_not_retried
    url, calls = fake_server do |sock, _|
      sock.gets("\r\n")
      sock.close
    end

    assert_raises(Ndb::ConnectionError) { Ndb::Client.new(url: url, ready_timeout: 1).bulk("c", [{ a: 1 }]) }
    assert_equal 1, calls.size
  end

  def test_read_dropped_after_send_is_retried
    url, calls = fake_server do |sock, _|
      sock.gets("\r\n")
      sock.close
    end

    assert_raises(Ndb::ConnectionError) { Ndb::Client.new(url: url, ready_timeout: 0.4).search("c", {}) }
    assert_operator calls.size, :>=, 2
  end

  def test_write_on_a_reused_connection_dropped_after_send_is_not_retried
    requests = Queue.new
    url, = fake_server do |sock, _|
      Thread.new do
        2.times do
          while (line = sock.gets("\r\n")) && line != "\r\n"; end

          requests << 1
          body = '{"collections":[]}'

          break sock.close if requests.size == 2

          sock.write("HTTP/1.1 200 OK\r\nContent-Length: #{body.bytesize}\r\n\r\n#{body}")
        end
      end
    end

    client = Ndb::Client.new(url: url, ready_timeout: 0)
    client.collections

    assert_raises(Ndb::ConnectionError) { client.bulk("c", [{ a: 1 }]) }
    sleep 0.1

    assert_equal 2, requests.size
  end
  # A read timeout is Ndb::TimeoutError, and the request is not sent again
  # (also a read on a reused connection).
  def test_a_read_timeout_is_a_timeout_error_and_is_not_sent_again
    requests = Queue.new
    url, = fake_server do |sock, _|
      Thread.new do
        loop do
          while (line = sock.gets("\r\n")) && line != "\r\n"; end

          break if line.nil?

          requests << 1

          next unless requests.size == 1

          body = '{"collections":[]}'

          sock.write("HTTP/1.1 200 OK\r\nContent-Length: #{body.bytesize}\r\n\r\n#{body}")
        end
      rescue IOError, SystemCallError
        nil
      end
    end

    client = Ndb::Client.new(url: url, timeout: 0.5, ready_timeout: 1)
    client.collections

    assert_raises(Ndb::TimeoutError) { client.collections }

    sleep 0.2

    assert_equal 2, requests.size
  end

  # A raise that is not a StandardError (Timeout, Thread#kill) between the
  # send and the read must not put the connection back with an answer that
  # was not read: the next call would get the answer of this one.
  def test_a_connection_left_by_a_non_standard_error_is_not_used_again
    fresh = Ndb::Client.new(socket: TestDaemon.socket)
    stop = Class.new(Exception)
    name = NdbTestHelpers.next_name

    fresh.ensure_collection(name)

    used = nil

    assert_raises(stop) do
      fresh.pool.with do |conn|
        used = conn

        conn.request("GET", "/_health", "")
        conn.send(:send_request, "GET", "/_health", "", {})

        raise stop
      end
    end

    assert_nil used.socket
    assert(fresh.collections.any? { |c| c["name"] == name })
  end
end
