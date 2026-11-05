# frozen_string_literal: true

$LOAD_PATH.unshift File.expand_path("../lib", __dir__)

ENV["RAILS_ENV"] = "test"

require "socket"
require "minitest/autorun"
require "rails"
require "ndb_record"
require_relative "daemon"

TestDaemon.start

NdbRecord.configure do |c|
  c.socket = TestDaemon.socket
  c.timeout = 10
end

module NdbTestHelpers
  @counter = 0

  def self.next_name
    @counter += 1

    "t#{Process.pid}_#{@counter}"
  end

  # A model class with a fresh collection per test.
  def model(name = nil, &block)
    coll = name || NdbTestHelpers.next_name
    klass = Class.new(NdbRecord::Base)

    klass.define_singleton_method(:name) { "TestPost" }
    klass.collection_name = coll
    klass.class_eval(&block) if block

    klass
  end

  def client
    NdbRecord.client
  end
end

class Minitest::Test
  include NdbTestHelpers
end
