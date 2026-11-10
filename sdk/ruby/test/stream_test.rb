# frozen_string_literal: true

require "test_helper"

class StreamTest < Minitest::Test
  def setup
    @stream = client.stream(NdbTestHelpers.next_name)
  end

  def test_append_info_range
    id = @stream.append({ order: 1, total: 9.9 })

    refute_nil Ndb::Stream.id_time(id)

    ids = append_orders(2, 4)

    assert_equal 3, ids.size

    info = @stream.info

    assert_equal 4, info["length"]
    assert_equal id, info["first_id"]
    assert_equal ids.last, info["last_id"]
    assert_equal 4, info["entries_added"]

    all = @stream.range

    assert_equal 4, all.size
    assert_equal({ "order" => 1, "total" => 9.9 }, all.first.doc)
    assert_instance_of Time, all.first.time
    refute_predicate all.first, :deleted?

    last = @stream.rev_range("+", "-", count: 1)

    assert_equal [ids.last], last.map(&:id)

    assert_raises(Ndb::StreamIDTooSmall) { @stream.append({}, id: "1-1") }
    assert_raises(Ndb::InvalidStreamID) { @stream.append({}, id: "0-0") }

    assert_equal 1, @stream.delete(id)
    assert_equal 2, @stream.trim(max_len: 1)
  end

  def test_append_with_trim_and_no_create
    assert_raises(Ndb::UnknownCollection) { @stream.append({ order: 1 }, no_create: true) }

    append_orders(1, 5)

    @stream.append({ order: 6 }, trim: { max_len: 2 })

    assert_equal 2, @stream.info["length"]
  end

  def test_read_without_a_group
    assert_raises(Ndb::UnknownCollection) { @stream.read }

    ids = append_orders(1, 4)

    all = @stream.read

    assert_equal ids, all.map(&:id)
    assert_equal({ "order" => 1, "total" => 10.0 }, all.first.doc)
    assert_instance_of Time, all.first.time

    assert_equal ids[2..], @stream.read(after: ids[1]).map(&:id)
    assert_equal [ids[0]], @stream.read(after: Ndb::Stream::START_ALL, count: 1).map(&:id)
    assert_equal [{ "order" => 4 }], @stream.read(after: ids[2], fields: ["order"]).map(&:doc)

    assert_empty @stream.read(after: ids.last)
    assert_empty @stream.read(after: Ndb::Stream::START_NEW)
    assert_empty @stream.group("workers").tap(&:create).pending_list
  end

  def test_read_without_a_group_blocks_until_an_append
    append_orders(1, 1)

    writer = Thread.new do
      sleep 0.2

      @stream.append({ order: 7 })
    end

    started = now
    got = @stream.read(after: Ndb::Stream::START_NEW, block: 5)

    assert_equal [writer.value], got.map(&:id)
    assert_operator now - started, :<, 4

    assert_empty @stream.read(after: got.last.id, block: 0.05)
  end

  def test_blocking_read_without_a_group_outlives_the_client_timeout
    short = Ndb::Client.new(socket: TestDaemon.socket, timeout: 0.3)

    append_orders(1, 1)

    started = now

    assert_empty short.stream(@stream.name).read(after: Ndb::Stream::START_NEW, block: 0.8)
    assert_operator now - started, :>=, 0.7
  end

  def test_claim_options
    group = @stream.group("workers")

    group.create(Ndb::Stream::START_ALL)

    ids = append_orders(1, 2)

    group.read("w1", count: 1)

    just = group.claim("w2", ids[0], just_ids: true)

    assert_equal [ids[0]], just.entries
    assert_equal 1, group.pending_list.first["deliveries"]

    forced = group.claim("w2", ids[1], force: true, retry_count: 5, idle: 90)

    assert_equal [ids[1]], forced.entries.map(&:id)

    row = group.pending_list(start: ids[1], stop: ids[1]).first

    assert_equal 5, row["deliveries"]
    assert_operator row["idle_ms"], :>=, 90_000

    assert_equal ids, group.auto_claim("w3", just_ids: true).entries
  end

  def test_group_create_with_no_create
    group = client.stream(NdbTestHelpers.next_name).group("workers")

    assert_raises(Ndb::UnknownCollection) { group.create(Ndb::Stream::START_NEW, no_create: true) }
  end

  def test_not_a_stream
    name = NdbTestHelpers.next_name

    client.ensure_collection(name)

    assert_raises(Ndb::NotAStream) { client.stream(name).info }
  end

  def test_group_read_ack_pending_claim
    group = @stream.group("workers")

    group.create(Ndb::Stream::START_ALL)

    assert_raises(Ndb::GroupExists) { group.create(Ndb::Stream::START_ALL) }

    group.ensure(Ndb::Stream::START_ALL)

    ids = append_orders(1, 3)
    got = group.read("w1", count: 2)

    assert_equal ids[0], got[0].id
    assert_equal 2, got[1].doc["order"]

    assert_equal 1, group.ack(ids[0])

    summary = group.pending

    assert_equal 1, summary["count"]
    assert_equal ids[1], summary["lowest"]
    assert_equal ["w1"], summary["consumers"].map { |c| c["name"] }

    mine = group.read("w1", pending: true)

    assert_equal [ids[1]], mine.map(&:id)

    list = group.pending_list(consumer: "w1")

    assert_equal [ids[1]], list.map { |p| p["_id"] }
    assert_equal 2, list.first["deliveries"] # the pending read counts, as in Redis

    claimed = group.claim("w2", ids[1])

    assert_equal [2], claimed.entries.map { |e| e.doc["order"] }

    auto = group.auto_claim("w3", count: 10)

    assert_equal "0-0", auto.cursor
    assert_equal 1, auto.entries.size

    assert_equal 3, group.consumers.size

    groups = @stream.groups

    assert_equal 1, groups.size
    assert_equal 1, groups.first["pending"]
    assert_equal ids[1], groups.first["last_delivered_id"]

    assert_equal 1, group.delete_consumer("w3")

    group.destroy

    assert_raises(Ndb::UnknownGroup) { group.read("w1") }
  end

  def test_read_blocks_until_an_append
    group = @stream.group("workers")

    group.create

    writer = Thread.new do
      sleep 0.2

      @stream.append({ order: 7 })
    end

    started = now
    got = group.read("w1", block: 5)

    writer.join

    assert_equal [7], got.map { |e| e.doc["order"] }
    assert_operator now - started, :<, 4

    assert_empty group.read("w1", block: 0.05)
  end

  def test_blocking_read_outlives_the_client_timeout
    short = Ndb::Client.new(socket: TestDaemon.socket, timeout: 0.3)
    group = short.stream(@stream.name).group("workers")

    group.create

    started = now

    assert_empty group.read("w1", block: 0.8)
    assert_operator now - started, :>=, 0.7
  end

  def test_worker_does_every_entry_once
    group = @stream.group("workers")

    group.create(Ndb::Stream::START_ALL)
    append_orders(1, 50)

    lock = Mutex.new
    seen = Hash.new(0)

    workers = %w[w1 w2 w3].map do |name|
      group.worker(name, block: 0.1) { |e| lock.synchronize { seen[e.doc["order"]] += 1 } }
    end

    threads = workers.map(&:start)

    append_orders(51, 60)
    wait_for { lock.synchronize { seen.size == 60 } }

    workers.each(&:stop)
    threads.each(&:join)

    assert(seen.values.all? { |n| n == 1 }, "an entry was done twice: #{seen}")
    assert_equal 0, group.pending["count"]
  end

  def test_worker_retries_and_buries_poison
    group = @stream.group("workers")

    group.create(Ndb::Stream::START_ALL)

    ids = append_orders(1, 2)
    dead_name = "#{@stream.name}_dead"

    lock = Mutex.new
    tries = Hash.new(0)
    poisoned = 0

    on_error = lambda do |error|
      lock.synchronize { poisoned += 1 } if error.is_a?(Ndb::PoisonMessage)
    end

    worker = group.worker(
      "w1",
      block: 0.05,
      claim_idle: 0.1,
      claim_every: 0.1,
      max_deliveries: 3,
      dead_letter: dead_name,
      on_error: on_error
    ) do |e|
      order = e.doc["order"]
      tries_now = lock.synchronize { tries[order] += 1 }

      raise "boom" if order == 2 || (order == 1 && tries_now == 1)
    end

    thread = worker.start

    wait_for { lock.synchronize { poisoned == 1 } }

    worker.stop
    thread.join

    assert_equal({ 1 => 2, 2 => 3 }, tries)
    assert_equal 0, group.pending["count"]

    dead = client.stream(dead_name).range

    assert_equal 1, dead.size
    assert_equal ids[1], dead.first.doc["id"]
    assert_equal 3, dead.first.doc["deliveries"]
    assert_equal "workers", dead.first.doc["group"]
    assert_equal({ "order" => 2, "total" => 20.0 }, dead.first.doc["doc"])
  end

  # An entry that fails on its last delivery goes to the dead letter at
  # once, not after one more claim_idle.
  def test_worker_buries_at_the_last_failure
    group = @stream.group("workers")

    group.create(Ndb::Stream::START_ALL)

    ids = append_orders(7, 7)
    dead_name = "#{@stream.name}_dead"
    poisoned = Queue.new

    worker = group.worker(
      "w1",
      block: 0.05,
      claim_idle: 3600,
      max_deliveries: 1,
      dead_letter: dead_name,
      on_error: ->(error) { poisoned << error if error.is_a?(Ndb::PoisonMessage) }
    ) { raise "boom" }

    thread = worker.start

    error = poisoned.pop(timeout: 3)

    worker.stop
    thread.join

    refute_nil error, "the entry did not go to the dead letter at its last failure"

    dead = client.stream(dead_name).range

    assert_equal [ids[0]], dead.map { |e| e.doc["id"] }
    assert_equal 1, dead.first.doc["deliveries"]
    assert_equal 0, group.pending["count"]
  end

  def test_worker_starts_with_its_own_pending
    group = @stream.group("workers")

    group.create(Ndb::Stream::START_ALL)
    append_orders(1, 3)

    group.read("w1")

    lock = Mutex.new
    got = []
    done = false

    thread = Thread.new do
      group.work("w1", block: 0.05, claim_idle: nil, stop: -> { done }) do |e|
        lock.synchronize { got << e.doc["order"] }
      end
    end

    wait_for { lock.synchronize { got.size == 3 } }

    done = true
    thread.join

    assert_equal [1, 2, 3], got
  end

  # A poison entry that stops the process in the block (a crash) is never
  # acked. Each restart reads it again (one more delivery). Above
  # max_deliveries it goes to the dead letter, and the block does not run.
  def test_worker_buries_an_own_pending_entry_that_crashed_the_process
    group = @stream.group("workers")

    group.create(Ndb::Stream::START_ALL)

    ids = append_orders(1, 1)
    dead_name = "#{@stream.name}_dead"

    group.read("w1")
    group.read("w1", pending: true)

    runs = []
    poisoned = []

    worker = group.worker(
      "w1",
      block: 0.05,
      claim_idle: nil,
      max_deliveries: 2,
      dead_letter: dead_name,
      on_error: ->(error) { poisoned << error if error.is_a?(Ndb::PoisonMessage) }
    ) { |entry, n| runs << [entry.id, n] }

    thread = worker.start

    wait_for { poisoned.any? }

    worker.stop
    thread.join

    assert_empty runs, "the block ran on an entry above max_deliveries"
    assert_equal 0, group.pending["count"]

    dead = client.stream(dead_name).range

    assert_equal [ids[0]], dead.map { |e| e.doc["id"] }
    assert_equal 2, dead.first.doc["deliveries"]
  end

  # An own pending entry at the limit (not above it) runs one more time.
  def test_worker_runs_an_own_pending_entry_at_the_limit
    group = @stream.group("workers")

    group.create(Ndb::Stream::START_ALL)
    append_orders(1, 1)

    group.read("w1")

    runs = Queue.new
    worker = group.worker("w1", block: 0.05, claim_idle: nil, max_deliveries: 2) { |_entry, n| runs << n }
    thread = worker.start

    assert_equal 2, runs.pop(timeout: 5)

    worker.stop
    thread.join

    assert_equal 0, group.pending["count"]
  end

  # NotImplementedError and SystemStackError are not StandardError. They
  # are a failed entry: the entry stays pending and the worker goes on.
  def test_worker_takes_an_exception_that_is_not_a_standard_error_as_a_failure
    group = @stream.group("workers")

    group.create(Ndb::Stream::START_ALL)
    append_orders(1, 3)

    errors = Queue.new
    done = Queue.new

    worker = group.worker("w1", block: 0.05, claim_idle: nil, on_error: ->(error) { errors << error }) do |entry|
      raise NotImplementedError, "todo" if entry.doc["order"] == 1
      raise SystemStackError, "deep" if entry.doc["order"] == 2

      done << entry.doc["order"]
    end

    thread = worker.start

    assert_equal 3, done.pop(timeout: 5)
    assert thread.alive?

    worker.stop
    thread.join

    assert_equal [NotImplementedError, SystemStackError], [errors.pop, errors.pop].map(&:class)
    assert_equal 2, group.pending["count"]
  end

  # A signal (or Thread#raise) in the block goes on to the caller, but the
  # entries done before it in the same read are acked.
  def test_worker_acks_the_done_entries_when_the_block_is_interrupted
    group = @stream.group("workers")

    group.create(Ndb::Stream::START_ALL)
    append_orders(1, 2)

    started = Queue.new

    worker = group.worker("w1", block: 0.05, claim_idle: nil) do |entry|
      next if entry.doc["order"] == 1

      started << true

      sleep 30
    end

    thread = worker.start
    thread.report_on_exception = false

    started.pop(timeout: 5)
    thread.raise(Interrupt)

    assert_raises(Interrupt) { thread.join }
    assert_equal 1, group.pending["count"]
  end

  # A failed first read of the own pending entries (ndb is down) goes to
  # on_error, and the worker tries again: start does not end the thread.
  def test_worker_retries_a_failed_first_pending_read
    down = Ndb::Client.new(socket: "/tmp/ndb-not-here-#{Process.pid}.sock", ready_timeout: 0)
    errors = Queue.new
    worker = down.stream("x").group("g").worker("w1", block: 0.05, on_error: ->(error) { errors << error }) { |_e| nil }

    thread = worker.start

    2.times { assert_kind_of Ndb::ConnectionError, errors.pop(timeout: 5) }
    assert thread.alive?

    worker.stop

    assert thread.join(5)
    assert_nil thread.value
  end

  # A group that does not exist is not fixed by a retry: run raises.
  # A worker that starts before its stream and group waits for them: each
  # failed read goes to on_error, and the worker tries again.
  def test_worker_waits_for_a_missing_group
    group = @stream.group("late")
    errors = []
    seen = []

    worker = group.worker("w1", block: 0.05, claim_idle: nil, on_error: ->(error) { errors << error }) do |entry|
      seen << entry.doc["order"]
    end

    thread = worker.start

    wait_for { errors.size >= 2 }

    assert thread.alive?
    assert(errors.all? { |e| e.is_a?(Ndb::UnknownGroup) || e.is_a?(Ndb::UnknownCollection) })

    group.create(Ndb::Stream::START_ALL)
    @stream.append({ order: 1 })

    wait_for { seen == [1] && group.pending["count"].zero? }

    worker.stop
    thread.join(10)

    refute thread.alive?
  end

  private

  def append_orders(from, to)
    @stream.append_many((from..to).map { |i| { order: i, total: i * 10.0 } })
  end

  def now = Process.clock_gettime(Process::CLOCK_MONOTONIC)

  def wait_for
    deadline = now + 15

    until yield
      flunk "timed out" if now > deadline

      sleep 0.02
    end
  end
end
