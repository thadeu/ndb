# frozen_string_literal: true

require "tmpdir"
require "fileutils"
require "minitest"

# Builds the ndb daemon once and runs it on a short unix socket path
# (macOS limits the path to 104 bytes). Stopped at process exit.
module TestDaemon
  REPO_ROOT = File.expand_path("../../..", __dir__)

  class << self
    attr_reader :socket, :data_dir

    def start
      return socket if @pid

      @tmp = Dir.mktmpdir("ndb", "/tmp")
      @data_dir = File.join(@tmp, "d")
      @socket = File.join(@tmp, "s.sock")
      binary = File.join(@tmp, "ndb")

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
