# frozen_string_literal: true

require "test_helper"
require "logger"
require "stringio"
require "ndb/rails/testing"
require "ndb/streams/assertions"

# Stream classes with a new stream for each test, and a log to read.
module StreamsTestHelpers
  def setup
    super

    Ndb::Streams.reset!
    Ndb::Streams.inline = false

    @log = StringIO.new
    Ndb::Streams.logger = Logger.new(@log)

    @runners = []
  end

  def teardown
    @runners.each(&:stop)
    @runners.each { |runner| runner.join(10) }

    Ndb::Streams.reset!
    Ndb::Streams.inline = false
    Ndb::Streams.logger = nil

    super
  end

  # A class with the include, on a new stream (or on name).
  def stream_class(name = nil, **options, &body)
    name ||= NdbTestHelpers.next_name

    Class.new do
      include Ndb::Streams

      stream name, **options

      class_eval(&body) if body
    end
  end

  # A started runner with short waits. The teardown stops it.
  def start_runner(**options)
    runner = Ndb::Streams.runner(block: 0.2, claim_every: 0.2, consumer: "test", **options)

    @runners << runner

    runner.start
  end

  def stop_runner(runner)
    runner.stop

    assert runner.join(10), "the runner did not stop"
  end

  def wait_until(timeout = 10)
    deadline = Process.clock_gettime(Process::CLOCK_MONOTONIC) + timeout

    until yield
      flunk "the condition did not come true in #{timeout} s" if Process.clock_gettime(Process::CLOCK_MONOTONIC) > deadline

      sleep 0.02
    end
  end

  # The next n items of a queue, or a failure after the timeout.
  def take(queue, count = 1, timeout: 10)
    Array.new(count) do
      item = queue.pop(timeout: timeout)

      flunk "no event in #{timeout} s" if item.nil?

      item
    end
  end
end
