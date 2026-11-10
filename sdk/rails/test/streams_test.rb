# frozen_string_literal: true

require "streams_helper"

class StreamsOrderEvent
  include Ndb::Streams
end

module StreamsTestApp
  class HTTPCall
    include Ndb::Streams
  end
end

class StreamsCategory
  include Ndb::Streams
end

class StreamsTest < Minitest::Test
  include StreamsTestHelpers
  include Ndb::Streams::Assertions

  # The name and the options

  def test_the_default_name_comes_from_the_class_name
    assert_equal "streams_order_events", StreamsOrderEvent.stream_name
    assert_equal "streams_test_app_http_calls", StreamsTestApp::HTTPCall.stream_name
  end

  # The words come from the ActiveSupport inflections, which ndb/streams
  # requires, and the name is kept after the first call.
  def test_the_default_name_uses_the_inflections_and_is_kept
    assert_equal "streams_categories", StreamsCategory.stream_name
    assert_same StreamsCategory.stream_name, StreamsCategory.stream_name
  end

  def test_a_class_without_a_name_needs_a_stream_name
    klass = Class.new { include Ndb::Streams }

    assert_raises(Ndb::Streams::ConfigurationError) { klass.stream_name }
  end

  def test_stream_sets_the_name_and_takes_the_name_of_another_class
    writer = stream_class("orders_#{NdbTestHelpers.next_name}")
    reader = Class.new { include Ndb::Streams }

    reader.stream writer, group: :billing

    assert_equal writer.stream_name, reader.stream_name
    assert_equal "billing", reader.stream_options[:group]
  end

  def test_every_option_has_a_default
    options = stream_class.stream_options

    assert_nil options[:group]
    assert_equal 1, options[:concurrency]
    assert_nil options[:max_deliveries]
    assert_nil options[:dead_letter]
    assert_equal 60.0, options[:claim_idle]
    assert_equal :new, options[:start]
    assert_empty options[:discard_on]
    assert_nil options[:max_len]
    assert_nil options[:retain]
  end

  def test_the_options_are_normalized
    klass = stream_class("orders", group: :billing, concurrency: 3, max_deliveries: 5, claim_idle: 2.minutes, start: "all", discard_on: ArgumentError, max_len: 100, retain: 30.days)
    options = klass.stream_options

    assert_equal "billing", options[:group]
    assert_equal 3, options[:concurrency]
    assert_equal 120.0, options[:claim_idle]
    assert_equal :all, options[:start]
    assert_equal [ArgumentError], options[:discard_on]
    assert_equal "orders_dead", options[:dead_letter]
    assert_equal "2592000s", options[:retain]
  end

  def test_the_dead_letter_can_be_named_or_turned_off
    named = stream_class("orders", max_deliveries: 2, dead_letter: :graveyard)
    off = stream_class("orders", max_deliveries: 2, dead_letter: false)

    assert_equal "graveyard", named.stream_options[:dead_letter]
    assert_nil off.stream_options[:dead_letter]
  end

  def test_an_unknown_option_and_a_bad_value_raise
    klass = Class.new { include Ndb::Streams }

    error = assert_raises(ArgumentError) { klass.stream :orders, groop: :billing }

    assert_match(/unknown stream option: :groop/, error.message)

    assert_raises(ArgumentError) { klass.stream :orders, concurrency: 0 }
    assert_raises(ArgumentError) { klass.stream :orders, start: :oldest }
    assert_raises(ArgumentError) { klass.stream :orders, discard_on: ["x"] }
    assert_raises(ArgumentError) { klass.stream :orders, claim_idle: "soon" }
  end

  # The registry

  def test_include_registers_the_class_and_a_subclass_inherits_the_config
    order = []
    base = stream_class("orders", group: :billing, max_deliveries: 4) do
      around_perform { |_event, block| order << :base && block.call }
    end

    child = Class.new(base) do
      stream "orders", group: :audit

      around_perform { |_event, block| order << :child && block.call }
    end

    assert_equal [base, child], Ndb::Streams.classes
    assert_equal "orders", child.stream_name
    assert_equal "audit", child.stream_options[:group]
    assert_equal 4, child.stream_options[:max_deliveries]
    assert_equal "billing", base.stream_options[:group]
    assert_equal [1, 2], [base.around_performs.size, child.around_performs.size]
  end

  def test_a_class_with_the_same_name_replaces_the_old_one
    first = Class.new { def self.name = "ReloadedStream" }
    second = Class.new { def self.name = "ReloadedStream" }

    first.include(Ndb::Streams)
    second.include(Ndb::Streams)

    assert_equal [second], Ndb::Streams.classes
  end

  def test_a_class_without_perform_is_a_writer
    writer = stream_class
    consumer = stream_class(writer.stream_name, group: :billing) { def perform(_event) = nil }

    refute writer.stream_consumer?
    assert consumer.stream_consumer?
    assert_equal [consumer], Ndb::Streams.consumers
    assert_equal [consumer], Ndb::Streams.consumers_of(writer.stream_name)
  end

  def test_a_consumer_without_a_group_is_an_error
    stream_class("orders") { def perform(_event) = nil }

    error = assert_raises(Ndb::Streams::MissingGroup) { Ndb::Streams.consumers }

    assert_match(/no group/, error.message)
    assert_raises(Ndb::Streams::MissingGroup) { Ndb::Streams.runner.start }
  end

  def test_two_consumers_with_one_group_on_one_stream_are_an_error
    stream_class("orders", group: :billing) { def perform(_event) = nil }
    stream_class("orders", group: :billing) { def perform(_event) = nil }

    error = assert_raises(Ndb::Streams::DuplicateGroup) { Ndb::Streams.consumers }

    assert_match(/"billing"/, error.message)
  end

  def test_two_groups_on_one_stream_are_fine
    stream_class("orders", group: :billing) { def perform(_event) = nil }
    stream_class("orders", group: :audit) { def perform(_event) = nil }

    assert_equal 2, Ndb::Streams.consumers.size
  end

  # Write

  def test_append_a_hash_returns_the_id
    klass = stream_class

    id = klass.append(order_id: 42, tags: %w[a b])

    assert_match(/\A\d+-\d+\z/, id)
    assert_equal 1, klass.count
    assert_equal({ "order_id" => 42, "tags" => %w[a b] }, klass.last.to_h)
  end

  def test_append_an_array_returns_the_ids
    klass = stream_class

    ids = klass.append([{ n: 1 }, { n: 2 }, { n: 3 }])

    assert_equal 3, ids.size
    assert_equal [1, 2, 3], klass.range.map { |event| event[:n] }
    assert_equal [], klass.append([])
  end

  def test_append_a_json_string
    klass = stream_class

    id = klass.append('{"n":1}')
    ids = klass.append('[{"n":2},{"n":3}]')

    assert_kind_of String, id
    assert_equal 2, ids.size
    assert_equal [1, 2, 3], klass.range.map { |event| event[:n] }
  end

  def test_enqueue_and_perform_async_are_append
    klass = stream_class

    klass.enqueue(n: 1)
    klass.perform_async(n: 2)

    assert_equal [1, 2], klass.range.map { |event| event[:n] }
  end

  def test_a_bad_payload_raises
    klass = stream_class

    assert_raises(ArgumentError) { klass.append(42) }
    assert_raises(ArgumentError) { klass.append([1, 2]) }
    assert_raises(ArgumentError) { klass.append("not json") }
    assert_raises(ArgumentError) { klass.append('"a string"') }
    assert_equal 0, klass.count
  end

  def test_max_len_trims_on_append
    klass = stream_class(max_len: 2)

    4.times { |n| klass.append(n: n) }

    assert_equal [2, 3], klass.range.map { |event| event[:n] }
  end

  def test_retain_makes_the_stream_with_a_retention
    klass = stream_class(retain: "30d")

    klass.append(n: 1)

    definition = client.collections.find { |d| d["name"] == klass.stream_name }

    assert_equal "30d", definition["retention"]
    assert_equal 1, klass.count
  end

  def test_append_waits_for_the_commit_of_an_active_record_transaction
    klass = stream_class

    with_fake_active_record do |pending|
      assert_nil klass.append(n: 1)
      assert_equal 0, klass.count

      pending.each(&:call)

      assert_equal 1, klass.count
    end

    assert_kind_of String, klass.append(n: 2)
  end

  def test_append_and_perform_send_notifications
    events = []
    klass = stream_class
    consumer = stream_class(klass.stream_name, group: :g) { def perform(_event) = nil }

    Ndb::Streams.inline!

    subscriber = ActiveSupport::Notifications.subscribe(/\.ndb_streams\z/) { |*args| events << ActiveSupport::Notifications::Event.new(*args) }

    klass.append([{ n: 1 }, { n: 2 }])

    append = events.find { |event| event.name == "append.ndb_streams" }
    performs = events.select { |event| event.name == "perform.ndb_streams" }

    assert_equal [klass.stream_name, klass, 2], append.payload.values_at(:stream, :class, :count)
    assert_equal 2, performs.size
    assert_equal [klass.stream_name, "g", consumer], performs.first.payload.values_at(:stream, :group, :class)
    assert_equal 1, performs.first.payload[:event][:n]
  ensure
    ActiveSupport::Notifications.unsubscribe(subscriber)
  end

  # Read

  def test_last_find_range_and_count
    klass = stream_class

    assert_equal 0, klass.count
    assert_nil klass.last
    assert_equal [], klass.last(2)
    assert_equal [], klass.range
    assert_nil klass.find("1-1")

    ids = klass.append([{ n: 1 }, { n: 2 }, { n: 3 }, { n: 4 }])

    assert_equal 4, klass.count
    assert_equal 4, klass.last[:n]
    assert_equal [3, 4], klass.last(2).map { |event| event[:n] }
    assert_equal 2, klass.find(ids[1])[:n]
    assert_nil klass.find("1-1")
    assert_equal [2, 3], klass.range(ids[1], ids[2]).map { |event| event[:n] }
    assert_equal [1, 2], klass.range(count: 2).map { |event| event[:n] }
  end

  def test_range_takes_times
    klass = stream_class

    klass.append(n: 1)

    now = Time.now

    assert_equal [1], klass.range(now - 60, now + 60).map { |event| event[:n] }
    assert_equal [], klass.range(now + 60, now + 120)
    assert_equal [], klass.range(now - 120, now - 60)
    assert_equal [1], klass.range(1.minute.ago).map { |event| event[:n] }
  end

  def test_the_event
    klass = stream_class

    id = klass.append(order_id: 42, "customer" => { "name" => "Ana" })
    event = klass.find(id)

    assert_equal id, event.id
    assert_equal 42, event[:order_id]
    assert_equal 42, event["order_id"]
    assert_nil event[:nothing]
    assert event.key?(:customer)
    assert_equal({ "order_id" => 42, "customer" => { "name" => "Ana" } }, event.to_h)
    assert_equal "Ana", event.to_h[:customer][:name]
    assert_in_delta Time.now.to_f, event.time.to_f, 60
    assert_nil event.deliveries
    assert_equal event, klass.last
    refute_respond_to event, :doc
  end

  # Tests of an app

  def test_inline_calls_each_consumer_in_the_caller_thread
    seen = []
    writer = stream_class
    stream_class(writer.stream_name, group: :billing) { define_method(:perform) { |event| seen << [:billing, event[:n], event.deliveries, Thread.current] } }
    stream_class(writer.stream_name, group: :audit) { define_method(:perform) { |event| seen << [:audit, event[:n], event.deliveries, Thread.current] } }
    stream_class(group: :other) { define_method(:perform) { |_event| seen << :other } }

    Ndb::Streams.inline!

    assert Ndb::Streams.inline?

    id = writer.append(n: 7)

    assert_equal [[:billing, 7, 1, Thread.current], [:audit, 7, 1, Thread.current]], seen
    assert_equal id, writer.last.id

    Ndb::Streams.inline = false
    writer.append(n: 8)

    assert_equal 2, seen.size
    assert_equal 2, writer.count
  end

  def test_inline_gives_the_error_of_perform_to_the_caller_but_not_a_discarded_one
    writer = stream_class
    stream_class(writer.stream_name, group: :g, discard_on: [KeyError]) { def perform(event) = raise(event[:error] == "key" ? KeyError : IOError, "bad") }

    Ndb::Streams.inline!

    writer.append(error: "key")

    assert_raises(IOError) { writer.append(error: "io") }
    assert_match(/discarded/, @log.string)
  end

  # Perform takes the options of the class of now: after a reload (a new
  # class with the same name) its discard_on applies.
  def test_perform_takes_discard_on_from_the_class_of_now
    old = Class.new do
      include Ndb::Streams

      stream :reloaded, group: :g

      def perform(_event) = raise(KeyError, "gone")
    end

    Object.const_set(:StreamsReloaded, old)
    Object.send(:remove_const, :StreamsReloaded)

    fresh = Class.new do
      include Ndb::Streams

      stream :reloaded, group: :g, discard_on: [KeyError]

      def perform(_event) = raise(KeyError, "gone")
    end

    Object.const_set(:StreamsReloaded, fresh)

    event = Ndb::Streams::Event.new(id: "1-0", payload: {}, deliveries: 1)

    assert_equal :discarded, Ndb::Streams::Perform.call(old, event)
  ensure
    Object.send(:remove_const, :StreamsReloaded) if Object.const_defined?(:StreamsReloaded)
  end

  def test_assert_streamed
    klass = stream_class

    assert_no_streamed klass

    klass.append(order_id: 42, state: "paid")
    klass.append(order_id: 43, state: "paid")

    assert_streamed klass
    assert_equal 2, assert_streamed(klass, state: :paid).size
    assert_equal [42], assert_streamed(klass, order_id: 42, state: "paid").map { |event| event[:order_id] }
    assert_no_streamed klass, order_id: 44
    assert_no_streamed klass, missing: nil

    assert_raises(Minitest::Assertion) { assert_streamed klass, order_id: 44 }
    assert_raises(Minitest::Assertion) { assert_no_streamed klass, order_id: 42 }
  end

  def test_the_rspec_matcher
    klass = stream_class

    klass.append(order_id: 42)

    matcher = have_streamed(order_id: 42)
    other = have_streamed(order_id: 1)

    assert matcher.matches?(klass)
    refute other.matches?(klass)
    assert_match(/found none/, other.failure_message)
    assert_match(/found one/, matcher.failure_message_when_negated)
    assert_match(/have streamed/, matcher.description)
  end

  def test_the_cleaner_truncates_the_streams_a_test_used
    Ndb::Testing.clean!(only: [])

    klass = stream_class

    klass.append(n: 1)

    assert_includes Ndb::Testing.touched, klass.stream_name

    Ndb::Testing.clean!

    assert_equal 0, klass.count

    klass.append(n: 2)

    assert_equal [2], klass.range.map { |event| event[:n] }
  end

  def test_the_prefix_of_a_parallel_worker_goes_before_the_stream_name
    klass = stream_class

    klass.append(n: 1)

    Ndb::Testing.worker!(5)

    assert_equal "test5_#{klass.stream_name}", klass.ndb_stream.name
    assert_equal 0, klass.count
  ensure
    Ndb::Rails.configuration.collection_prefix = nil
    Ndb::Rails.reset_client!
  end

  private

  # ActiveRecord with only the hook of Rails 7.2. It yields the blocks that
  # wait for the commit.
  def with_fake_active_record
    pending = []
    fake = Module.new

    fake.define_singleton_method(:after_all_transactions_commit) { |&block| pending << block }

    Object.const_set(:ActiveRecord, fake)

    yield pending
  ensure
    Object.send(:remove_const, :ActiveRecord)
  end
end
