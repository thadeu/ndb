# frozen_string_literal: true

$LOAD_PATH.unshift File.expand_path("../lib", __dir__)

require "socket"
require "minitest/autorun"
require "ndb"
require_relative "daemon"

TestDaemon.start

module NdbTestHelpers
  @counter = 0

  def self.next_name
    @counter += 1

    "t#{Process.pid}_#{@counter}"
  end

  # A client on the shared test daemon.
  def client
    @client ||= Ndb::Client.new(socket: TestDaemon.socket, timeout: 10)
  end

  def collection_name
    NdbTestHelpers.next_name
  end
end

class Minitest::Test
  include NdbTestHelpers
end
