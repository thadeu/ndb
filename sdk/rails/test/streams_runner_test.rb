# frozen_string_literal: true

require "streams_helper"

class StreamsRunnerTest < Minitest::Test
  include StreamsTestHelpers

  def test_the_runner_calls_perform_and_acks
    seen = Queue.new
    writer = stream_class
    consumer = stream_class(writer.stream_name, group: :billing) do
      define_method(:perform) { |event| seen << [self, event] }
    end

    runner = start_runner

    ids = writer.append([{ n: 1 }, { n: 2 }])
    first, second = take(seen, 2)

    assert_equal ids, [first[1].id, second[1].id]
    assert_equal [1, 2], [first[1][:n], second[1][:n]]
    assert_equal 1, first[1].deliveries
    assert_kind_of consumer, first[0]
    refute_same first[0], second[0]

    wait_until { consumer.stream_group.pending["count"].zero? }

    assert_equal ["test:0"], consumer.stream_group.consumers.map { |row| row["name"] }

    stop_runner(runner)

    assert runner.stopped?
  end

  def test_a_new_group_reads_only_new_entries_and_start_all_reads_every_entry
    seen = Queue.new
    writer = stream_class

    writer.append(n: 1)

    stream_class(writer.stream_name, group: :fresh) { define_method(:perform) { |event| seen << [:fresh, event[:n]] } }
    stream_class(writer.stream_name, group: :history, start: :all) { define_method(:perform) { |event| seen << [:history, event[:n]] } }

    start_runner

    assert_equal [[:history, 1]], take(seen)

    writer.append(n: 2)

    assert_equal [[:fresh, 2], [:history, 2]], take(seen, 2).sort_by(&:first)
  end

  def test_a_restart_reads_what_came_while_it_was_stopped
    seen = Queue.new
    writer = stream_class
    stream_class(writer.stream_name, group: :billing) { define_method(:perform) { |event| seen << event[:n] } }

    runner = start_runner

    writer.append(n: 1)

    assert_equal [1], take(seen)

    stop_runner(runner)

    writer.append([{ n: 2 }, { n: 3 }])

    start_runner

    assert_equal [2, 3], take(seen, 2)
  end

  def test_a_failed_entry_stays_pending_and_a_restart_reads_it_first
    seen = Queue.new
    fail_it = true
    writer = stream_class
    consumer = stream_class(writer.stream_name, group: :billing) do
      define_method(:perform) do |event|
        seen << [event[:n], event.deliveries]

        raise IOError, "the bank is down" if fail_it
      end
    end

    runner = start_runner

    id = writer.append(n: 1)

    assert_equal [[1, 1]], take(seen)

    wait_until { @log.string.include?("the bank is down") }

    assert_equal [id], consumer.stream_group.pending_list.map { |row| row["_id"] }

    stop_runner(runner)

    fail_it = false

    start_runner

    assert_equal [[1, 2]], take(seen)

    wait_until { consumer.stream_group.pending["count"].zero? }
  end

  def test_an_idle_entry_is_claimed_and_tried_again
    seen = Queue.new
    writer = stream_class
    stream_class(writer.stream_name, group: :billing, claim_idle: 0.3) do
      define_method(:perform) do |event|
        seen << event.deliveries

        raise IOError, "not yet" if event.deliveries < 3
      end
    end

    start_runner

    writer.append(n: 1)

    assert_equal [1, 2, 3], take(seen, 3)
  end

  def test_discard_on_acks_without_a_retry
    seen = Queue.new
    writer = stream_class
    consumer = stream_class(writer.stream_name, group: :billing, claim_idle: 0.2, discard_on: [KeyError]) do
      define_method(:perform) do |event|
        seen << event[:n]

        raise KeyError, "no such order" if event[:n] == 1
      end
    end

    start_runner

    writer.append([{ n: 1 }, { n: 2 }])

    assert_equal [1, 2], take(seen, 2)

    wait_until { consumer.stream_group.pending["count"].zero? }

    sleep 0.6

    assert seen.empty?
    assert_match(/discarded .*KeyError: no such order/, @log.string)
  end

  def test_max_deliveries_sends_the_entry_to_the_dead_letter
    seen = Queue.new
    writer = stream_class
    consumer = stream_class(writer.stream_name, group: :billing, claim_idle: 0.2, max_deliveries: 2) do
      define_method(:perform) do |event|
        seen << event.deliveries

        raise IOError, "always"
      end
    end

    dead = stream_class("#{writer.stream_name}_dead")

    start_runner

    id = writer.append(order_id: 42)

    wait_until { dead.count == 1 }

    entry = dead.last

    assert_equal [1, 2], take(seen, 2)
    assert_equal [writer.stream_name, "billing", id, 2], [entry[:stream], entry[:group], entry[:id], entry[:deliveries]]
    assert_equal({ "order_id" => 42 }, entry[:doc])

    wait_until { consumer.stream_group.pending["count"].zero? }

    assert_match(/poison message/, @log.string)
    assert seen.empty?
  end

  def test_around_perform_wraps_perform_in_order_on_the_instance
    seen = Queue.new
    writer = stream_class
    base = stream_class(writer.stream_name, group: :base) do
      around_perform do |event, block|
        note(:outer_in, event[:n])

        block.call

        note(:outer_out, event[:n])
      end

      define_method(:note) { |*parts| seen << parts }
    end

    Class.new(base) do
      stream writer.stream_name, group: :billing

      around_perform do |_event, block|
        note(:inner_in)

        block.call
      end

      def perform(event) = note(:perform, event[:n])
    end

    start_runner

    writer.append(n: 5)

    assert_equal [[:outer_in, 5], [:inner_in], [:perform, 5], [:outer_out, 5]], take(seen, 4)
  end

  def test_an_around_perform_that_does_not_call_the_block_skips_perform
    seen = Queue.new
    writer = stream_class
    consumer = stream_class(writer.stream_name, group: :billing) do
      around_perform { |event, block| block.call unless event[:skip] }

      define_method(:perform) { |event| seen << event[:n] }
    end

    start_runner

    writer.append([{ n: 1, skip: true }, { n: 2 }])

    assert_equal [2], take(seen)

    wait_until { consumer.stream_group.pending["count"].zero? }
  end

  def test_concurrency_starts_one_worker_with_a_stable_name_for_each_thread
    writer = stream_class
    consumer = stream_class(writer.stream_name, group: :billing, concurrency: 3) { def perform(_event) = nil }

    runner = Ndb::Streams.runner(consumer: "web-1", block: 0.2).start

    @runners << runner

    assert_equal %w[web-1:0 web-1:1 web-1:2], runner.workers.map(&:consumer)

    stop_runner(runner)

    other = Ndb::Streams.runner(concurrency: 2, block: 0.2).start

    @runners << other

    assert_equal 2, other.workers.size
    assert_equal ["billing"], consumer.ndb_stream.groups.map { |row| row["name"] }
  end

  def test_the_consumer_name_is_ndb_consumer_or_the_hostname
    assert_equal "worker-7", Ndb::Streams::Runner.consumer_base("NDB_CONSUMER" => "worker-7")
    assert_equal Socket.gethostname, Ndb::Streams::Runner.consumer_base({})
  end

  def test_only_selects_the_classes_by_name
    seen = Queue.new
    writer = stream_class

    billing = stream_class(writer.stream_name, group: :billing) { define_method(:perform) { |_event| seen << :billing } }
    audit = stream_class(writer.stream_name, group: :audit) { define_method(:perform) { |_event| seen << :audit } }

    billing.define_singleton_method(:name) { "BillingPaid" }
    audit.define_singleton_method(:name) { "AuditTrail" }

    assert_equal [audit], Ndb::Streams.runner(only: ["AuditTrail"]).classes
    assert_equal [billing], Ndb::Streams.runner(only: billing).classes
    assert_raises(ArgumentError) { Ndb::Streams.runner(only: %w[Nothing]).classes }

    start_runner(only: "AuditTrail")

    writer.append(n: 1)

    assert_equal [:audit], take(seen)
    assert_equal ["audit"], writer.ndb_stream.groups.map { |row| row["name"] }
  end

  def test_the_defaults_come_from_the_streams_block_of_ndb_yml
    writer = stream_class

    billing = stream_class(writer.stream_name, group: :billing) { def perform(_event) = nil }
    audit = stream_class(writer.stream_name, group: :audit) { def perform(_event) = nil }

    billing.define_singleton_method(:name) { "BillingPaid" }
    audit.define_singleton_method(:name) { "AuditTrail" }

    Ndb::Rails.configuration.streams = { "concurrency" => 2, "only" => ["BillingPaid"] }

    runner = start_runner

    assert_equal [billing], runner.classes
    assert_equal 2, runner.workers.size
    assert_equal 1, Ndb::Streams.runner(concurrency: 1, only: ["AuditTrail"]).classes.size
  ensure
    Ndb::Rails.configuration.streams = {}
  end

  def test_run_blocks_until_sigterm_and_ends_the_current_entry
    seen = Queue.new
    writer = stream_class
    consumer = stream_class(writer.stream_name, group: :billing) do
      define_method(:perform) do |event|
        seen << :started

        sleep 0.3

        seen << event[:n]
      end
    end

    done = false

    thread = Thread.new do
      Ndb::Streams.run

      done = true
    end

    wait_until { group_names(writer) == ["billing"] }

    writer.append(n: 1)

    assert_equal [:started], take(seen)

    Process.kill("TERM", Process.pid)

    assert thread.join(10), "run did not return after SIGTERM"
    assert done
    assert_equal [1], take(seen)
    assert_equal 0, consumer.stream_group.pending["count"]
  end

  private

  def group_names(klass)
    klass.ndb_stream.groups.map { |row| row["name"] }
  rescue Ndb::UnknownCollection
    []
  end
end
