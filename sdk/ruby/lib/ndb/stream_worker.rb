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
  # worker of the group claims it again after claim_idle.
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
    # for them (claim_idle / 2, at least 1). max_deliveries: an entry
    # delivered this many times goes to dead_letter (a stream name) and is
    # acked; nil is no limit. on_error: a callable that gets the errors that
    # do not stop the worker (a failed read, a raise in the block, a
    # PoisonMessage).
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
    # stays pending; the loop moves past it.
    def work_pending
      after = "0"

      until stopped?
        entries = @group.read(@consumer, count: @count, pending: true, after: after)

        return if entries.empty?

        handle(entries)

        after = entries.last.id
      end
    end

    # Claims the idle entries of the group. The poison ones go to the dead
    # letter; the others run again.
    def work_idle
      idle = @group.pending_list(idle: @claim_idle, count: @count)

      return if idle.empty?

      deliveries = idle.to_h { |p| [p["_id"], p["deliveries"]] }
      claimed = @group.claim(@consumer, deliveries.keys, min_idle: @claim_idle)

      retry_entries = claimed.entries.reject do |entry|
        n = deliveries[entry.id]

        next false unless @max_deliveries && n >= @max_deliveries

        bury(entry, n)

        true
      end

      handle(retry_entries)
    rescue Ndb::Error => e
      report(e)
    end

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
    # call. A deleted entry is acked without the block.
    def handle(entries)
      done = []

      entries.each do |entry|
        break if stopped?

        if entry.deleted?
          done << entry.id

          next
        end

        begin
          @handler.call(entry)

          done << entry.id
        rescue StandardError => e
          report(e)
        end
      end

      return if done.empty?

      begin
        @group.ack(done)
      rescue Ndb::Error => e
        report(e)
      end
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
