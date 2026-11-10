# frozen_string_literal: true

module Ndb
  # The error on_error gets for a poison message: an entry delivered
  # max_deliveries times. It was sent to the dead letter and acked.
  class PoisonMessage < Error
    attr_reader :entry, :deliveries

    def initialize(message, entry:, deliveries:)
      super(message, code: "poison_message")

      @entry = entry
      @deliveries = deliveries
    end
  end

  # A worker of a group: it runs the block on each entry. A block that
  # returns acks the entry; a block that raises leaves it pending, and a
  # worker of the group claims it again after claim_idle. Any Exception is
  # a failed entry (also NotImplementedError or SystemStackError), but a
  # SignalException or SystemExit goes on to the caller.
  #
  #   worker = orders.group("billing").worker("w1", max_deliveries: 5) { |e| charge(e.doc) }
  #   thread = worker.start   # or worker.run, which blocks
  #   worker.stop             # ends the loop after the current read
  #
  # Delivery is at least once: the work on an entry must be safe to repeat.
  class Worker
    attr_reader :group, :consumer

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
    # The block gets the entry. A block with two parameters also gets the
    # delivery count of the entry (1 on the first delivery).
    def initialize(group, consumer, count: 10, block: 5, claim_idle: 60, claim_every: nil, max_deliveries: nil, dead_letter: nil, on_error: nil, &handler)
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

      @stopped = false
    end

    # Runs the loop in this thread until #stop. It:
    #
    # 1. reads its own pending entries first (the ones it took before a
    #    restart and did not ack), and does the work on them;
    # 2. reads new entries, waiting up to block for them;
    # 3. acks the entries the block did without a raise, in one call;
    # 4. every claim_every, claims the entries idle for claim_idle, and
    #    sends the ones delivered max_deliveries times to dead_letter.
    def run
      work_pending

      next_claim = now + @claim_every
      backoff = 0

      until stopped?
        if @claim_idle && now >= next_claim
          work_idle

          next_claim = now + @claim_every
        end

        wait = @claim_idle ? [@block, next_claim - now].min : @block

        begin
          entries = @group.read(@consumer, count: @count, block: [wait, 0.001].max)
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

    def pause(seconds)
      deadline = now + seconds

      sleep 0.01 while !stopped? && now < deadline
    end

    def report(error)
      @on_error&.call(error)
    end

    # The consumer's own pending entries, in ID order. An entry that fails
    # stays pending; the loop moves past it. A failed read goes to on_error
    # and is tried again after a backoff, also a group (or stream) that
    # does not exist yet: the worker waits for it.
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
    # letter. Their count has the pending read, so they ran count - 1 times.
    def without_crashed(entries, counts)
      return entries unless @max_deliveries

      entries.reject do |entry|
        n = counts.fetch(entry.id, 1)

        next false if entry.deleted? || n <= @max_deliveries

        bury(entry, n - 1)

        true
      end
    end

    # Claims the idle entries of the group. The poison ones go to the dead
    # letter; the others run again.
    def work_idle
      idle = @group.pending_list(idle: @claim_idle, count: @count)

      return if idle.empty?

      deliveries = idle.to_h { |p| [p["_id"], p["deliveries"]] }
      claimed = @group.claim(@consumer, deliveries.keys, min_idle: @claim_idle)

      # An entry at the limit is one that a stopped process did not bury.
      retry_entries = claimed.entries.reject do |entry|
        n = deliveries[entry.id]

        next false unless @max_deliveries && n >= @max_deliveries

        bury(entry, n)

        true
      end

      # The claim counted one more delivery.
      handle(retry_entries, deliveries.transform_values { |n| n + 1 })
    rescue Ndb::Error => e
      report(e)
    end

    # The delivery counts of the consumer's pending entries, by ID. Only a
    # block that takes the count, or a max_deliveries, needs the request.
    def pending_counts(entries)
      return {} unless counts? || @max_deliveries

      rows = @group.pending_list(start: entries.first.id, stop: entries.last.id, count: entries.size, consumer: @consumer)

      rows.to_h { |p| [p["_id"], p["deliveries"]] }
    rescue Ndb::Error => e
      report(e)

      {}
    end

    def counts? = @handler.arity == 2

    # Sends a poison entry to the dead letter, acks it, then reports it.
    def bury(entry, deliveries)
      stream = @group.stream

      if @dead_letter
        dead = { stream: stream.name, group: @group.name, id: entry.id, deliveries: deliveries, doc: entry.doc }

        stream.client.stream(@dead_letter).append(dead)
      end

      @group.ack(entry.id)

      report(PoisonMessage.new("poison message: #{stream.name}/#{@group.name} #{entry.id} after #{deliveries} deliveries", entry: entry, deliveries: deliveries))
    rescue Ndb::Error => e
      report(e)
    end

    # Runs the block on each entry and acks the ones that worked, in one
    # call. A deleted entry is acked without the block. counts has the
    # delivery count of the entries that were delivered before.
    def handle(entries, counts = {})
      done = []

      entries.each do |entry|
        break if stopped?

        if entry.deleted?
          done << entry.id

          next
        end

        n = counts.fetch(entry.id, 1)

        begin
          counts? ? @handler.call(entry, n) : @handler.call(entry)

          done << entry.id
        rescue SignalException, SystemExit
          raise
        rescue Exception => e
          report(e)

          bury(entry, n) if @max_deliveries && n >= @max_deliveries
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
