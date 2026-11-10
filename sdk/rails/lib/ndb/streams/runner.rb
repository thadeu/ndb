# frozen_string_literal: true

module Ndb
  module Streams
    # Runs the consumers: for each class, `concurrency` threads, each one an
    # Ndb::Worker of the group with a stable consumer name. So a restart
    # reads its own pending entries first.
    #
    #   runner = Ndb::Streams.runner(only: ["BillingPaid"])
    #   runner.start    # the threads; it does not block
    #   runner.stop     # each thread ends after its current entry
    #   runner.join
    #
    # #run does the three, and stops on SIGTERM and SIGINT.
    class Runner
      SIGNALS = %w[TERM INT].freeze

      # A read waits this many seconds for new entries, so a stop takes at
      # most this long.
      BLOCK = 2

      MAX_BACKOFF = 5

      # Seconds after the first signal in which a second one does nothing.
      GRACE = 2

      # How often #run looks for a thread that ended.
      SUPERVISE_EVERY = 0.2

      attr_reader :workers

      # only: class names (or classes). concurrency: replaces the option of
      # every class. consumer: the base of the consumer names (NDB_CONSUMER,
      # or the hostname). claim_every: seconds between two looks for idle
      # entries (the default of Ndb::Worker: claim_idle / 2, at least 1).
      # due_every: seconds between two claims of the due retries (the
      # default of Ndb::Worker: 1 with a retry schedule).
      def initialize(only: nil, concurrency: nil, consumer: nil, block: BLOCK, claim_every: nil, due_every: nil)
        settings = Streams.settings

        @only = Array(only || settings["only"]).map { |entry| entry.respond_to?(:name) ? entry.name : entry.to_s }
        @concurrency = concurrency || settings["concurrency"]
        @consumer = consumer || self.class.consumer_base
        @block = block
        @claim_every = claim_every
        @due_every = due_every

        @workers = []
        @threads = []
        @classes = {}.compare_by_identity
        @ready = Queue.new
        @stopped = false
        @stop_at = nil
        @interrupted = false
      end

      # NDB_CONSUMER, or the hostname.
      def self.consumer_base(env = ENV)
        name = env["NDB_CONSUMER"].to_s.strip

        name.empty? ? Socket.gethostname : name
      end

      # The retry schedule of a consumer in words, for the log:
      # "15s, 1m, 10m, 1h, then 6h; max age 15d; max deliveries 5".
      def self.retry_text(options)
        schedule = options[:retry]
        claim_idle = options[:claim_idle]

        idle = claim_idle ? Ndb::Stream::Tiers.duration(claim_idle) : "off"

        parts = [schedule ? Ndb::Stream::Tiers.describe(schedule) : "after claim_idle (#{idle})"]
        parts << "max age #{Ndb::Stream::Tiers.duration(options[:max_age])}" if options[:max_age]
        parts << "max deliveries #{options[:max_deliveries]}" if options[:max_deliveries]

        parts.join("; ")
      end

      # The consumer classes this runner runs.
      def classes
        found = Streams.consumers

        return found if @only.empty?

        unknown = @only - found.map(&:name)

        raise ConfigurationError, "no consumer class with the name #{unknown.join(', ')}" unless unknown.empty?

        found.select { |klass| @only.include?(klass.name) }
      end

      # Loads app/streams (in this thread) and starts the threads. Each
      # thread makes its group first; start returns after that first try,
      # so the groups are there when ndb is up. While ndb is down, a thread
      # tries again with a backoff: start does not raise. A stop during the
      # start starts no more workers, and stops the ones that started.
      def start
        Streams.load_classes! unless stopped?

        selected = stopped? ? [] : classes

        Streams.logger.warn("ndb streams: no consumer class to run") if selected.empty? && !stopped?

        selected.each do |klass|
          break if stopped?

          threads = @concurrency || klass.stream_options[:concurrency]

          threads.times { |index| @workers << worker(klass, index) }

          Streams.logger.info("ndb streams: #{klass.stream_label} reads #{klass.stream_name}/#{klass.stream_options[:group]} with #{threads} thread(s) as #{@consumer}")
          Streams.logger.info("ndb streams: #{klass.stream_label} retry: #{self.class.retry_text(klass.stream_options)}")
        end

        @threads = @workers.map { |worker| spawn_thread(worker) }

        wait_ready

        @workers.each(&:stop) if stopped?

        self
      end

      # Asks the threads to end. Each one ends its current entry, acks it
      # and stops. It is safe in a signal handler.
      def stop
        @stop_at ||= now
        @stopped = true

        @workers.each(&:stop)

        self
      end

      def stopped?
        @stopped
      end

      # Waits for the threads (at most timeout seconds). True when all ended.
      def join(timeout = nil)
        deadline = timeout && (now + timeout)

        @threads.all? do |thread|
          wait = deadline ? [deadline - now, 0].max : nil

          ended?(thread, wait)
        end
      end

      # Starts, waits for SIGTERM or SIGINT, then stops. It blocks. A
      # thread that ends before the stop starts again. A second signal in
      # the first GRACE seconds of the stop does nothing (Puma and the
      # terminal can send two). A later one interrupts the current entries
      # (the entries done before are acked); one more ends the process at
      # once.
      def run
        old = SIGNALS.to_h { |name| [name, trap_signal(name)] }

        start
        supervise
        join

        nil
      ensure
        old&.each { |name, handler| Signal.trap(name, handler || "DEFAULT") }
      end

      private

      def now = Process.clock_gettime(Process::CLOCK_MONOTONIC)

      def trap_signal(name)
        Signal.trap(name) { signal_received }
      end

      def signal_received
        return stop unless stopped?
        return if now - @stop_at < GRACE

        exit!(1) if @interrupted

        @interrupted = true

        @threads.each { |thread| thread.raise(Interrupt) if thread.alive? }
      end

      # Waits until each thread tried to make its group one time, or a stop.
      def wait_ready
        left = @threads.size

        while left.positive? && !stopped?
          left -= 1 if @ready.pop(timeout: 0.1)
        end
      end

      # Starts again a thread that ended while the runner runs (a
      # Thread#kill, or an exception that loop_worker gives on).
      def supervise
        until stopped?
          @threads.each_with_index do |thread, index|
            next if thread.alive? || stopped?

            worker = @workers[index]
            error = thread_error(thread)
            reason = error ? "#{error.class}: #{error.message}" : "killed"

            Streams.logger.error("ndb streams: a thread of #{worker.group.stream.name}/#{worker.group.name} ended (#{reason}); it starts again")

            @threads[index] = spawn_thread(worker)
          end

          sleep SUPERVISE_EVERY
        end
      end

      # The exception that ended the thread, or nil.
      def thread_error(thread)
        thread.join(0)

        nil
      rescue Exception => e
        e
      end

      # True when the thread ended in wait seconds (nil: no limit). An
      # exception of the thread does not go on to the caller.
      def ended?(thread, wait)
        !thread.join(wait).nil?
      rescue Exception
        true
      end

      def spawn_thread(worker)
        Thread.new do
          Thread.current.name = "ndb-streams-#{worker.consumer}"

          loop_worker(worker)
        end
      end

      def worker(klass, index)
        options = klass.stream_options
        group = klass.stream_group
        dead_letter = options[:dead_letter] && "#{Streams.prefix}#{options[:dead_letter]}"

        worker = group.worker(
          "#{@consumer}:#{index}",
          block: @block,
          claim_idle: options[:claim_idle],
          claim_every: @claim_every,
          retry: options[:retry],
          max_age: options[:max_age],
          due_every: @due_every,
          max_deliveries: options[:max_deliveries],
          dead_letter: dead_letter,
          on_error: ->(error) { report(klass, error) }
        ) do |entry, deliveries|
          Perform.call(klass, Event.from_entry(entry, deliveries: deliveries))
        end

        @classes[worker] = klass

        worker
      end

      # Makes the group, then runs the worker until a stop. Ndb::Worker#run
      # ends early only on an error (ndb is down, the group was deleted):
      # then it waits and starts again, with the group made again. Any
      # exception but a signal or an exit is such an error.
      def loop_worker(worker)
        backoff = 0
        first = true

        until worker.stopped?
          begin
            begin
              Streams.ensure_group(@classes.fetch(worker))
            ensure
              @ready << worker if first

              first = false
            end

            worker.run
          rescue SignalException, SystemExit
            raise
          rescue Exception => e
            Streams.logger.error("ndb streams: #{worker.group.stream.name}/#{worker.group.name} #{e.class}: #{e.message}")

            backoff = [[backoff * 2, 0.5].max, MAX_BACKOFF].min

            pause(worker, backoff)
          end
        end
      end

      def pause(worker, seconds)
        deadline = now + seconds

        sleep 0.05 while !worker.stopped? && now < deadline
      end

      def report(klass, error)
        if error.is_a?(Ndb::PoisonMessage)
          Streams.logger.warn("ndb streams: #{klass.stream_label} #{error.message}")

          return
        end

        Streams.logger.error("ndb streams: #{klass.stream_label} #{error.class}: #{error.message}")
      end
    end
  end
end
