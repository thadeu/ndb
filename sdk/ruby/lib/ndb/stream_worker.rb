# frozen_string_literal: true

module Ndb
  # The error on_error gets for an entry that the worker sent to the dead
  # letter and acked. reason is "max_deliveries" (it was delivered
  # max_deliveries times) or "max_age" (it was older than max_age).
  class PoisonMessage < Error
    attr_reader :entry, :deliveries, :reason

    def initialize(message, entry:, deliveries:, reason: Stream::Tiers::MAX_DELIVERIES)
      super(message, code: "poison_message")

      @entry = entry
      @deliveries = deliveries
      @reason = reason
    end
  end

  class Stream
    # A retry schedule: the wait (seconds) before the next try of an entry
    # that failed on its delivery n. Make one with Stream.tiers. It is a
    # callable, as the retry: option of a worker takes.
    class Tiers
      MAX_DELIVERIES = "max_deliveries"
      MAX_AGE = "max_age"

      # The longest wait of a nack (30 days).
      MAX_WAIT = 30 * 86_400

      # [[wait, up_to], ...]: the wait of a try up to up_to (nil: no end).
      attr_reader :steps

      # waits: seconds, one for each try; the last one repeats. Or steps:
      # Hashes { wait:, up_to: }; a step is the wait of the tries up to
      # up_to (the tries after the step before it), and the last one
      # repeats.
      def initialize(*waits)
        waits = waits.flatten

        raise ArgumentError, "tiers needs at least one wait" if waits.empty?

        @steps = waits.all?(Hash) ? from_steps(waits) : from_waits(waits)
        @steps.freeze

        freeze
      end

      # The wait (seconds) after a failure on the delivery n (1 is the
      # first delivery).
      def call(deliveries)
        step = @steps.find { |_, up_to| up_to.nil? || deliveries <= up_to }

        (step || @steps.last).first
      end

      def to_proc = method(:call).to_proc

      # "15s, 1m, 10m, 1h, then 6h", or "15s x1, 1m x4, then 6h".
      def to_s
        plain = @steps.each_with_index.all? { |(_, up_to), index| up_to.nil? || up_to == index + 1 }
        last = @steps.last.first
        previous = 0

        head = @steps[0...-1].map do |wait, up_to|
          tries = up_to - previous
          previous = up_to

          plain ? Tiers.duration(wait) : "#{Tiers.duration(wait)} x#{tries}"
        end

        [*head, "then #{Tiers.duration(last)}"].join(", ").delete_prefix("then ")
      end

      alias inspect to_s

      # "15s", "1m", "6h", "15d"; "1.5s" for a part of a second.
      def self.duration(seconds)
        seconds = seconds.to_f

        return "#{format_number(seconds / 86_400)}d" if seconds >= 86_400 && (seconds % 86_400).zero?
        return "#{format_number(seconds / 3600)}h" if seconds >= 3600 && (seconds % 3600).zero?
        return "#{format_number(seconds / 60)}m" if seconds >= 60 && (seconds % 60).zero?

        "#{format_number(seconds)}s"
      end

      # The first tries of any retry callable, for a log: "15s, 1m, ...".
      def self.describe(retry_schedule, tries: 5)
        return retry_schedule.to_s if retry_schedule.is_a?(Tiers)

        waits = (1..tries).map { |n| duration(retry_schedule.call(n)) }

        "#{waits.join(', ')}, ..."
      end

      def self.format_number(value)
        rounded = value.round(3)

        rounded == rounded.to_i ? rounded.to_i.to_s : rounded.to_s
      end

      private

      def from_waits(waits)
        waits.each_with_index.map do |wait, index|
          [seconds(wait), index == waits.size - 1 ? nil : index + 1]
        end
      end

      def from_steps(steps)
        previous = 0

        steps.each_with_index.map do |step, index|
          unknown = step.keys - %i[wait up_to]

          raise ArgumentError, "a tier step has wait: and up_to:, not #{unknown.inspect}" unless unknown.empty?

          up_to = step[:up_to]
          last = index == steps.size - 1

          unless (up_to.nil? && last) || (up_to.is_a?(Integer) && up_to > previous)
            raise ArgumentError, "up_to of a tier step is an integer greater than the one before it (only the last step can have none), not #{up_to.inspect}"
          end

          previous = up_to if up_to

          [seconds(step.fetch(:wait)), up_to]
        end
      end

      def seconds(wait)
        unless wait.respond_to?(:to_f) && !wait.is_a?(String) && wait.to_f >= 0 && wait.to_f <= MAX_WAIT
          raise ArgumentError, "a tier wait is a number of seconds from 0 to 30 days, not #{wait.inspect}"
        end

        wait.to_f
      end
    end

    # A retry schedule for the retry: option of a worker:
    #
    #   Ndb::Stream.tiers(15, 60, 600, 3600, 21_600)   # try 1: 15 s, try 2: 1 min, ... then 6 h
    #   Ndb::Stream.tiers({ wait: 15, up_to: 1 }, { wait: 60, up_to: 5 }, { wait: 21_600 })
    def self.tiers(*waits)
      Tiers.new(*waits)
    end
  end

  # A worker of a group: it runs the block on each entry. A block that
  # returns acks the entry. A block that raises leaves it pending, and a
  # worker of the group claims it again after claim_idle; with retry:, the
  # entry comes back after the wait of the schedule instead. Any Exception
  # is a failed entry (also NotImplementedError or SystemStackError), but a
  # SignalException or SystemExit goes on to the caller.
  #
  #   worker = orders.group("billing").worker("w1", max_deliveries: 5) { |e| charge(e.doc) }
  #   thread = worker.start   # or worker.run, which blocks
  #   worker.stop             # ends the loop after the current read
  #
  # Delivery is at least once: the work on an entry must be safe to repeat.
  class Worker
    # The most pending rows of one request for the delivery counts.
    COUNTS_PAGE = 100

    attr_reader :group, :consumer, :retry_schedule, :max_age

    # count: the most entries per read (10). block: how long a read waits
    # for new entries, in seconds (5). claim_idle: entries pending this
    # long, of any consumer, are claimed and tried again (60; nil or a
    # negative value turns the claim off). claim_every: how often to look
    # for them (claim_idle / 2, at least 1). max_deliveries: an entry that
    # fails on its max_deliveries-th delivery goes to dead_letter (a stream
    # name) and is acked at once; nil is no limit. on_error: a callable that
    # gets the errors that do not stop the worker (a failed read, a raise in
    # the block, a PoisonMessage). A failed read, also of a group or stream
    # that does not exist yet, is reported and tried again with a backoff
    # (0.1 s, doubled up to 5 s): a worker that starts before its group
    # waits for it.
    #
    # Delayed retry (ndb only):
    #
    # retry: the wait before the next try of a failed entry. A callable
    # ->(deliveries) { seconds } (deliveries is 1 after the first failure),
    # or an Array of waits in seconds, [15, 60, 600, 3600, 21_600] (the
    # last one repeats; see Stream.tiers). The worker nacks the entry with
    # that wait: it has no owner while it waits, and any worker of the
    # group takes it when it is due. nil keeps the entry pending (claim_idle).
    # max_age: seconds. An entry older than this (by the time of its ID)
    # goes to dead_letter and is acked, without the block and with no retry.
    # An entry that fails when it is older than this goes there too, at
    # once (it is not nacked).
    # due_every: how often to claim the due entries, in seconds (1 when
    # retry: is set; else off, and claim_idle also takes them).
    #
    # The dead letter entry is a Hash with "stream", "group", "id",
    # "deliveries", "reason" ("max_deliveries" or "max_age") and "doc".
    #
    # The block gets the entry. A block with two parameters also gets the
    # delivery count of the entry (1 on the first delivery).
    def initialize(group, consumer, count: 10, block: 5, claim_idle: 60, claim_every: nil, max_deliveries: nil, dead_letter: nil, on_error: nil, retry: nil, max_age: nil, due_every: nil, &handler)
      raise ArgumentError, "a worker needs a block" unless handler

      @group = group
      @consumer = consumer
      @handler = handler

      @count = count
      @block = block
      @claim_idle = claim_idle && claim_idle.positive? ? claim_idle : nil
      @claim_every = claim_every || [(@claim_idle || 0) / 2.0, 1].max
      @max_deliveries = max_deliveries
      @dead_letter = dead_letter
      @on_error = on_error

      @retry_schedule = retry_callable(binding.local_variable_get(:retry))
      @max_age = positive_seconds(:max_age, max_age)
      @due_every = due_every.nil? && @retry_schedule ? 1 : positive_seconds(:due_every, due_every)

      @idle_cursor = "0-0"
      @stopped = false
    end

    # Runs the loop in this thread until #stop. It:
    #
    # 1. reads its own pending entries first (the ones it took before a
    #    restart and did not ack), and does the work on them;
    # 2. reads new entries, waiting up to block for them;
    # 3. acks the entries the block did without a raise, in one call, and
    #    nacks the failed ones with the wait of retry:;
    # 4. every due_every, claims the nacked entries whose wait passed;
    # 5. every claim_every, claims the entries idle for claim_idle, and
    #    sends the ones delivered max_deliveries times to dead_letter.
    def run
      work_pending

      next_claim = now + @claim_every
      next_due = @due_every ? now + @due_every : nil
      backoff = 0

      until stopped?
        if next_due && now >= next_due
          work_due

          next_due = now + @due_every
        end

        if @claim_idle && now >= next_claim
          work_idle

          next_claim = now + @claim_every
        end

        begin
          entries = @group.read(@consumer, count: @count, block: [read_wait(next_claim, next_due), 0.001].max)
        rescue Ndb::Error => e
          report(e)

          backoff = [[backoff * 2, 0.1].max, 5].min

          pause(backoff)

          next
        end

        backoff = 0

        handle(entries)
      end

      nil
    end

    # Runs the loop in a new Thread and returns it.
    def start
      Thread.new { run }
    end

    # Asks the loop to end. It ends after the current read (at most block
    # seconds) and the work on the entries of that read.
    def stop
      @stopped = true
    end

    def stopped? = @stopped

    private

    def now = Process.clock_gettime(Process::CLOCK_MONOTONIC)

    # The read waits for new entries until the next claim.
    def read_wait(next_claim, next_due)
      waits = [@block]
      waits << next_claim - now if @claim_idle
      waits << next_due - now if next_due

      waits.min
    end

    def pause(seconds)
      deadline = now + seconds

      sleep 0.01 while !stopped? && now < deadline
    end

    def report(error)
      @on_error&.call(error)
    end

    # An Array is a Stream.tiers; a callable is used as it is.
    def retry_callable(value)
      return nil if value.nil?
      return Stream.tiers(value) if value.is_a?(Array)
      return value if value.respond_to?(:call)

      raise ArgumentError, "retry is an Array of waits or a callable ->(deliveries) { seconds }, not #{value.inspect}"
    end

    def positive_seconds(name, value)
      return nil if value.nil?
      return value.to_f if value.respond_to?(:to_f) && !value.is_a?(String) && value.to_f.positive?

      raise ArgumentError, "#{name} is a number of seconds greater than 0, not #{value.inspect}"
    end

    # The consumer's own pending entries, in ID order. An entry that fails
    # stays pending (or is nacked, with retry:); the loop moves past it. A
    # failed read goes to on_error and is tried again after a backoff, also
    # a group (or stream) that does not exist yet: the worker waits for it.
    # A nacked entry has no owner, so this read does not give it.
    #
    # The pending read counts one more delivery. With max_deliveries, an
    # entry above the limit had max_deliveries deliveries without an ack:
    # the process stopped in the block (a crash). It goes to the dead letter
    # without the block.
    def work_pending
      after = "0"
      backoff = 0

      until stopped?
        begin
          entries = @group.read(@consumer, count: @count, pending: true, after: after)
        rescue Ndb::Error => e
          report(e)

          backoff = [[backoff * 2, 0.1].max, 5].min

          pause(backoff)

          next
        end

        backoff = 0

        return if entries.empty?

        counts = pending_counts(entries)

        handle(without_crashed(entries, counts), counts)

        after = entries.last.id
      end
    end

    # The entries to run: the ones above max_deliveries go to the dead
    # letter. Their count has the last delivery, so they ran count - 1
    # times.
    def without_crashed(entries, counts)
      return entries unless @max_deliveries

      entries.reject do |entry|
        n = counts.fetch(entry.id, 1)

        next false if entry.deleted? || n <= @max_deliveries

        bury(entry, n - 1, Stream::Tiers::MAX_DELIVERIES)

        true
      end
    end

    # Claims the nacked entries whose wait passed, batch by batch, and runs
    # them. The claim counts one more delivery.
    def work_due
      cursor = "0-0"

      until stopped?
        claimed = @group.claim_due(@consumer, cursor: cursor, count: @count)
        counts = pending_counts(claimed.entries)

        handle(without_crashed(claimed.entries, counts), counts)

        cursor = claimed.cursor

        break if cursor.nil? || cursor == "0-0"
      end
    rescue Ndb::Error => e
      report(e)
    end

    # Claims the idle entries of the group with a scan, one batch each
    # time, from where the last one ended. The scan skips the nacked
    # entries that still wait. An entry above the limit is one that a
    # stopped process did not bury: it goes to the dead letter. The others
    # run again.
    def work_idle
      claimed = @group.auto_claim(@consumer, min_idle: @claim_idle, cursor: @idle_cursor, count: @count)

      @idle_cursor = claimed.cursor || "0-0"

      return if claimed.entries.empty?

      counts = pending_counts(claimed.entries)

      handle(without_crashed(claimed.entries, counts), counts)
    rescue Ndb::Error => e
      report(e)
    end

    # The delivery counts of the consumer's pending entries, by ID. Only a
    # block that takes the count, a max_deliveries or a retry: needs them.
    # It reads the pending list of the consumer from the first ID to the
    # last, page by page, until it has every ID.
    def pending_counts(entries)
      return {} if entries.empty? || !(counts? || @max_deliveries || @retry_schedule)

      wanted = entries.to_h { |entry| [entry.id, true] }
      counts = {}
      start = entries.first.id
      page = [entries.size, COUNTS_PAGE].max

      loop do
        rows = @group.pending_list(start: start, stop: entries.last.id, count: page, consumer: @consumer)

        rows.each { |p| counts[p["_id"]] = p["deliveries"] if wanted[p["_id"]] }

        break if rows.size < page || counts.size == wanted.size

        start = next_id(rows.last["_id"])
      end

      counts
    rescue Ndb::Error => e
      report(e)

      {}
    end

    # The ID just after id.
    def next_id(id)
      ms, _, seq = id.partition("-")

      "#{ms}-#{seq.to_i + 1}"
    end

    def counts? = @handler.arity == 2

    # Sends an entry to the dead letter, acks it, then reports it.
    def bury(entry, deliveries, reason)
      stream = @group.stream

      if @dead_letter
        dead = { stream: stream.name, group: @group.name, id: entry.id, deliveries: deliveries, reason: reason, doc: entry.doc }

        stream.client.stream(@dead_letter).append(dead)
      end

      @group.ack(entry.id)

      report(PoisonMessage.new(poison_text(entry, deliveries, reason), entry: entry, deliveries: deliveries, reason: reason))
    rescue Ndb::Error => e
      report(e)
    end

    def poison_text(entry, deliveries, reason)
      where = "#{@group.stream.name}/#{@group.name} #{entry.id}"

      return "poison message: #{where} after #{deliveries} deliveries" if reason == Stream::Tiers::MAX_DELIVERIES

      "expired message: #{where} is older than max_age #{Stream::Tiers.duration(@max_age)} (#{deliveries} deliveries)"
    end

    # True when the entry is older than max_age, by the time of its ID.
    def expired?(entry)
      @max_age && entry.time && Time.now - entry.time > @max_age
    end

    # A failed entry: on its max_deliveries-th delivery, or when it is now
    # older than max_age, it goes to the dead letter; with retry: it is
    # nacked with the wait of the schedule; else it stays pending.
    def failed(entry, deliveries)
      return bury(entry, deliveries, Stream::Tiers::MAX_DELIVERIES) if @max_deliveries && deliveries >= @max_deliveries
      return bury(entry, deliveries, Stream::Tiers::MAX_AGE) if expired?(entry)

      nack(entry, deliveries) if @retry_schedule
    end

    # Gives the entry back to the group after the wait of the schedule. A
    # failed nack leaves it pending: claim_idle takes it.
    def nack(entry, deliveries)
      wait = @retry_schedule.call(deliveries).to_f.clamp(0, Stream::Tiers::MAX_WAIT)

      @group.nack(@consumer, entry.id, delay: wait)
    rescue Ndb::Error => e
      report(e)
    end

    # Runs the block on each entry and acks the ones that worked, in one
    # call. A deleted entry is acked without the block. An entry older
    # than max_age goes to the dead letter without the block. counts has
    # the delivery count of the entries that were delivered before. A stop
    # leaves the entries not yet run pending.
    def handle(entries, counts = {})
      done = []

      entries.each do |entry|
        break if stopped?

        if entry.deleted?
          done << entry.id

          next
        end

        n = counts.fetch(entry.id, 1)

        if expired?(entry)
          bury(entry, n, Stream::Tiers::MAX_AGE)

          next
        end

        begin
          counts? ? @handler.call(entry, n) : @handler.call(entry)

          done << entry.id
        rescue SignalException, SystemExit
          raise
        rescue Exception => e
          # A raise after a stop is not a failure: the entry stays pending
          # for the next run.
          break if stopped?

          report(e)

          failed(entry, n)
        end
      end
    ensure
      # Also after a signal or Thread#raise in the block: the entries done
      # before it are acked.
      ack_done(done)
    end

    def ack_done(ids)
      return if ids.nil? || ids.empty?

      @group.ack(ids)
    rescue Ndb::Error => e
      report(e)
    end
  end

  class Group
    # A Worker of this group as consumer, with the block as the handler. See
    # Worker#initialize for the options.
    def worker(consumer, **options, &handler)
      Worker.new(self, consumer, **options, &handler)
    end

    # Runs a worker in this thread until it is stopped. Pass stop: a callable
    # that returns true to end the loop (it is checked after each read), or
    # build one with #worker and call Worker#stop from another thread.
    def work(consumer, stop: nil, **options, &handler)
      worker = Worker.new(self, consumer, **options, &handler)

      return worker.run unless stop

      watcher = Thread.new do
        sleep 0.05 until stop.call || worker.stopped?

        worker.stop
      end

      begin
        worker.run
      ensure
        watcher.kill
      end
    end
  end
end
