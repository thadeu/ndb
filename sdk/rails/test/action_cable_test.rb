# frozen_string_literal: true

require "test_helper"
require "logger"
require "stringio"
require "action_cable"

# The ActionCable adapter (adapter: ndb in config/cable.yml), on a real
# daemon. The first group of tests is the common behavior of an adapter, as
# in actioncable's test/subscription_adapter/common.rb.
class ActionCableTest < Minitest::Test
  WAIT_FOR_EVENT = 5
  WAIT_FOR_NO_EVENT = 0.3

  def setup
    super

    @stream = "cable_#{NdbTestHelpers.next_name}"
    @servers = []
    @adapters = []

    @rx_adapter = adapter
    @tx_adapter = adapter

    @saved = [ActionCable::SubscriptionAdapter::Ndb.block, ActionCable::SubscriptionAdapter::Ndb.retry_after]
    ActionCable::SubscriptionAdapter::Ndb.block = 0.2
    ActionCable::SubscriptionAdapter::Ndb.retry_after = 0.05
  end

  def teardown
    @adapters.each(&:shutdown)
    @servers.each { |server| server.event_loop.stop }

    ActionCable::SubscriptionAdapter::Ndb.block, ActionCable::SubscriptionAdapter::Ndb.retry_after = @saved

    super
  end

  # The common behavior

  def test_the_adapter_is_found_by_its_name
    assert_instance_of ActionCable::SubscriptionAdapter::Ndb, @rx_adapter
  end

  def test_subscribe_and_unsubscribe
    subscribe_as_queue("channel") { |queue| }
  end

  def test_basic_broadcast
    subscribe_as_queue("channel") do |queue|
      @tx_adapter.broadcast("channel", "hello world")

      assert_equal "hello world", queue.pop(timeout: WAIT_FOR_EVENT)
    end
  end

  def test_broadcast_after_unsubscribe
    keep_queue = nil

    subscribe_as_queue("channel") do |queue|
      keep_queue = queue

      @tx_adapter.broadcast("channel", "hello world")

      assert_equal "hello world", queue.pop(timeout: WAIT_FOR_EVENT)
    end

    @tx_adapter.broadcast("channel", "hello void")
    sleep WAIT_FOR_NO_EVENT

    assert_empty keep_queue
  end

  def test_multiple_broadcast
    subscribe_as_queue("channel") do |queue|
      @tx_adapter.broadcast("channel", "bananas")
      @tx_adapter.broadcast("channel", "apples")

      received = [queue.pop(timeout: WAIT_FOR_EVENT), queue.pop(timeout: WAIT_FOR_EVENT)]

      assert_equal %w[apples bananas], received.sort
    end
  end

  def test_identical_subscriptions
    subscribe_as_queue("channel") do |queue|
      subscribe_as_queue("channel") do |queue_2|
        @tx_adapter.broadcast("channel", "hello")

        assert_equal "hello", queue_2.pop(timeout: WAIT_FOR_EVENT)
      end

      assert_equal "hello", queue.pop(timeout: WAIT_FOR_EVENT)
    end
  end

  def test_simultaneous_subscriptions
    subscribe_as_queue("channel") do |queue|
      subscribe_as_queue("other channel") do |queue_2|
        @tx_adapter.broadcast("channel", "apples")
        @tx_adapter.broadcast("other channel", "oranges")

        assert_equal "apples", queue.pop(timeout: WAIT_FOR_EVENT)
        assert_equal "oranges", queue_2.pop(timeout: WAIT_FOR_EVENT)
      end
    end
  end

  def test_channel_filtered_broadcast
    subscribe_as_queue("channel") do |queue|
      @tx_adapter.broadcast("other channel", "one")
      @tx_adapter.broadcast("channel", "two")

      assert_equal "two", queue.pop(timeout: WAIT_FOR_EVENT)
    end
  end

  def test_long_identifiers
    channel_1 = "#{'a' * 100}1"
    channel_2 = "#{'a' * 100}2"

    subscribe_as_queue(channel_1) do |queue|
      subscribe_as_queue(channel_2) do |queue_2|
        @tx_adapter.broadcast(channel_1, "apples")
        @tx_adapter.broadcast(channel_2, "oranges")

        assert_equal "apples", queue.pop(timeout: WAIT_FOR_EVENT)
        assert_equal "oranges", queue_2.pop(timeout: WAIT_FOR_EVENT)
      end
    end
  end

  def test_unicode_and_large_payloads_round_trip
    text = "olá, 世界 🚀 #{'x' * 200_000}"
    json = ActiveSupport::JSON.encode({ "html" => "<p>é</p>", "n" => 1 })

    subscribe_as_queue("channel") do |queue|
      @tx_adapter.broadcast("channel", text)
      @tx_adapter.broadcast("channel", json)

      assert_equal text, queue.pop(timeout: WAIT_FOR_EVENT)
      assert_equal json, queue.pop(timeout: WAIT_FOR_EVENT)
    end
  end

  def test_the_success_callback_is_optional
    queue = Queue.new
    callback = ->(message) { queue << message }

    @rx_adapter.subscribe("channel", callback)
    wait_until { listener(@rx_adapter)&.ready? }

    @tx_adapter.broadcast("channel", "hi")

    assert_equal "hi", queue.pop(timeout: WAIT_FOR_EVENT)
  ensure
    @rx_adapter.unsubscribe("channel", callback)
  end

  # ndb

  def test_a_broadcast_is_one_entry_of_the_stream
    @tx_adapter.broadcast("channel", "hello")

    entry = client.stream(@stream).rev_range(count: 1).first

    assert_equal({ "channel" => "channel", "payload" => "hello" }, entry.doc)
  end

  def test_every_process_gets_every_broadcast
    other = adapter

    subscribe_as_queue("channel") do |queue|
      subscribe_as_queue("channel", other) do |queue_2|
        @tx_adapter.broadcast("channel", "to all")

        assert_equal "to all", queue.pop(timeout: WAIT_FOR_EVENT)
        assert_equal "to all", queue_2.pop(timeout: WAIT_FOR_EVENT)
      end
    end
  end

  def test_old_entries_are_not_delivered
    @tx_adapter.broadcast("channel", "before")

    subscribe_as_queue("channel") do |queue|
      @tx_adapter.broadcast("channel", "after")

      assert_equal "after", queue.pop(timeout: WAIT_FOR_EVENT)
    end
  end

  # The next read starts after the last ID it saw, not at "$": an entry
  # appended between two reads is not lost.
  def test_an_entry_appended_between_two_reads_is_delivered
    stream = @rx_adapter.stream
    tx = @tx_adapter
    appended = false

    stream.define_singleton_method(:read) do |**options|
      entries = super(**options)

      if entries.empty? && !appended
        appended = true
        tx.broadcast("channel", "between")
      end

      entries
    end

    subscribe_as_queue("channel") do |queue|
      assert_equal "between", queue.pop(timeout: WAIT_FOR_EVENT)
    end

    assert appended
  end

  def test_a_stream_that_does_not_exist_yet
    subscribe_as_queue("channel") do |queue|
      sleep WAIT_FOR_NO_EVENT

      assert_raises(Ndb::UnknownCollection) { client.stream(@stream).info }

      @tx_adapter.broadcast("channel", "first")

      assert_equal "first", queue.pop(timeout: WAIT_FOR_EVENT)
    end
  end

  def test_max_len_trims_the_stream
    small = adapter(max_len: 10)

    1_100.times { |i| small.broadcast("channel", i.to_s) }

    length = client.stream(@stream).info["length"]

    assert_operator length, :<, 1_100
    assert_operator length, :>=, 10
  end

  def test_the_default_options
    plain = adapter(stream: nil)

    assert_equal "cable", plain.stream.name
    assert_equal 10_000, plain.send(:max_len)
  end

  def test_the_stream_option
    assert_equal @stream, @tx_adapter.stream.name
  end

  def test_the_channel_prefix
    prefixed_rx = adapter(channel_prefix: "app")
    prefixed_tx = adapter(channel_prefix: "app")
    other_tx = adapter(channel_prefix: "other")

    subscribe_as_queue("channel", prefixed_rx) do |queue|
      other_tx.broadcast("channel", "not for me")
      prefixed_tx.broadcast("channel", "for me")

      assert_equal "for me", queue.pop(timeout: WAIT_FOR_EVENT)
    end

    channels = client.stream(@stream).range.map { |entry| entry.doc["channel"] }

    assert_equal ["other:channel", "app:channel"], channels
  end

  def test_an_entry_without_a_known_channel_is_skipped
    subscribe_as_queue("channel") do |queue|
      client.stream(@stream).append({ "other" => "doc" })
      @tx_adapter.broadcast("channel", "ok")

      assert_equal "ok", queue.pop(timeout: WAIT_FOR_EVENT)
    end
  end

  def test_an_ndb_error_is_logged_and_the_loop_goes_on
    daemon = TcpDaemon.new
    rx = adapter(client: Ndb::Client.new(url: daemon.url, ready_timeout: 0, timeout: 5))
    tx = adapter(client: Ndb::Client.new(url: daemon.url, ready_timeout: 0, timeout: 5))

    subscribe_as_queue("channel", rx) do |queue|
      tx.broadcast("channel", "before")

      assert_equal "before", queue.pop(timeout: WAIT_FOR_EVENT)

      daemon.stop
      wait_until { log_of(rx).include?("ActionCable ndb adapter: Ndb::") }
      daemon.start

      tx.broadcast("channel", "after")

      assert_equal "after", queue.pop(timeout: WAIT_FOR_EVENT)
    end
  ensure
    daemon&.cleanup
  end

  def test_shutdown_stops_the_thread
    subscribe_as_queue("channel") { |queue| }

    thread = listener(@rx_adapter).instance_variable_get(:@thread)

    assert_predicate thread, :alive?

    @rx_adapter.shutdown

    refute_predicate thread, :alive?
  end

  def test_shutdown_without_a_subscription
    assert_nil @tx_adapter.shutdown
  end

  def test_shutdown_twice
    subscribe_as_queue("channel") { |queue| }

    @rx_adapter.shutdown
    @rx_adapter.shutdown

    refute_predicate listener(@rx_adapter).instance_variable_get(:@thread), :alive?
  end

  private

  # An adapter of a new server, as ActionCable makes it from cable.yml.
  # client: an Ndb::Client for this adapter in place of Ndb::Rails.client.
  def adapter(client: nil, **cable)
    server = ActionCable::Server::Base.new(config: ActionCable::Server::Configuration.new)

    server.config.cable = { adapter: "ndb", stream: @stream }.merge(cable).compact.with_indifferent_access
    server.config.logger = Logger.new(StringIO.new)

    adapter = server.config.pubsub_adapter.new(server)

    adapter.instance_variable_set(:@stream, client.stream(@stream)) if client

    @servers << server
    @adapters << adapter

    adapter
  end

  def listener(adapter) = adapter.instance_variable_get(:@listener)

  def log_of(adapter) = adapter.logger.instance_variable_get(:@logdev).dev.string

  # Subscribes, waits for the success callback, yields a queue of the
  # messages, and checks that no other message came.
  def subscribe_as_queue(channel, adapter = @rx_adapter)
    queue = Queue.new
    callback = ->(message) { queue << message }
    subscribed = Concurrent::Event.new

    adapter.subscribe(channel, callback, proc { subscribed.set })
    subscribed.wait(WAIT_FOR_EVENT)

    assert_predicate subscribed, :set?

    yield queue

    sleep WAIT_FOR_NO_EVENT

    assert_empty queue
  ensure
    adapter.unsubscribe(channel, callback) if subscribed&.set?
  end

  def wait_until(timeout = 10)
    deadline = Process.clock_gettime(Process::CLOCK_MONOTONIC) + timeout

    until yield
      flunk "the condition did not come true in #{timeout} s" if Process.clock_gettime(Process::CLOCK_MONOTONIC) > deadline

      sleep 0.02
    end
  end
end
