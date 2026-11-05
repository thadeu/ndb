# frozen_string_literal: true

require "tmpdir"
require "fileutils"
require "minitest"

# Builds the ndb daemon once and runs it on a short unix socket path
# (macOS limits the path to 104 bytes). Stopped at process exit.
module TestDaemon
  REPO_ROOT = File.expand_path("../../..", __dir__)

  class << self
    attr_reader :socket, :data_dir, :binary

    def start
      return socket if @pid

      @tmp = Dir.mktmpdir("ndb", "/tmp")
      @data_dir = File.join(@tmp, "d")
      @socket = File.join(@tmp, "s.sock")
      binary = File.join(@tmp, "ndb")

      @binary = binary

      build(binary)
      spawn_daemon(binary)
      wait_ready
      Minitest.after_run { stop }

      socket
    end

    def stop
      return unless @pid

      Process.kill("TERM", @pid)
      Process.wait(@pid)
      FileUtils.rm_rf(@tmp)
      @pid = nil
    end

    private

    def build(binary)
      ok = system("nice", "-n", "10", "go", "build", "-o", binary, "./cmd/ndb", chdir: REPO_ROOT)

      raise "go build failed" unless ok
    end

    def spawn_daemon(binary)
      log = File.join(@tmp, "ndb.log")

      @pid = Process.spawn(binary, "-data-dir", @data_dir, "-unix", socket, out: log, err: log)
    end

    def wait_ready
      200.times do
        return if healthy?

        sleep 0.05
      end

      raise "ndb daemon did not become ready"
    end

    def healthy?
      UNIXSocket.new(socket).close

      true
    rescue SystemCallError
      false
    end
  end
end

# A second daemon with a TCP listener, and optionally a token. It can be
# stopped and started again on the same port and data dir (restart tests).
class TcpDaemon
  TOKEN = "ruby-sdk-secret"

  attr_reader :port, :socket

  def initialize(token: nil)
    TestDaemon.start

    @tmp = Dir.mktmpdir("ndbt", "/tmp")
    @socket = File.join(@tmp, "s.sock")
    @port = free_port
    @token_file = nil

    if token
      @token_file = File.join(@tmp, "token")
      File.write(@token_file, "#{token}\n")
    end

    start
    Minitest.after_run { cleanup }
  end

  def url = "tcp://127.0.0.1:#{port}"

  def start
    args = ["-config", File.join(@tmp, "absent.yml"), "-data-dir", File.join(@tmp, "d"), "-unix", socket, "-tcp", "127.0.0.1:#{port}"]
    args += ["-token-file", @token_file] if @token_file

    log = File.join(@tmp, "ndb.log")
    @pid = Process.spawn(TestDaemon.binary, *args, out: log, err: log)

    wait_ready
  end

  def stop
    return unless @pid

    Process.kill("TERM", @pid)
    Process.wait(@pid)
    @pid = nil
  end

  def cleanup
    stop
    FileUtils.rm_rf(@tmp)
  end

  private

  def free_port
    server = TCPServer.new("127.0.0.1", 0)

    server.addr[1]
  ensure
    server&.close
  end

  def wait_ready
    200.times do
      begin
        UNIXSocket.new(socket).close

        return
      rescue SystemCallError
        sleep 0.05
      end
    end

    raise "tcp daemon did not become ready"
  end
end
