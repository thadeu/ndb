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

      attr_reader :workers

      # only: class names (or classes). concurrency: replaces the option of
      # every class. consumer: the base of the consumer names (NDB_CONSUMER,
      # or the hostname). claim_every: seconds between two looks for idle
      # entries (the default of Ndb::Worker: claim_idle / 2, at least 1).
      def initialize(only: nil, concurrency: nil, consumer: nil, block: BLOCK, claim_every: nil)
        settings = Streams.settings

        @only = Array(only || settings["only"]).map { |entry| entry.respond_to?(:name) ? entry.name : entry.to_s }
        @concurrency = concurrency || settings["concurrency"]
        @consumer = consumer || self.class.consumer_base
        @block = block
        @claim_every = claim_every

        @workers = []
        @threads = []
        @stopped = false
      end

      # NDB_CONSUMER, or the hostname.
      def self.consumer_base(env = ENV)
        name = env["NDB_CONSUMER"].to_s.strip

        name.empty? ? Socket.gethostname : name
      end

      # The consumer classes this runner runs.
      def classes
        found = Streams.consumers

        return found if @only.empty?

        unknown = @only - found.map(&:name)

        raise ArgumentError, "no consumer class with the name #{unknown.join(', ')}" unless unknown.empty?

        found.select { |klass| @only.include?(klass.name) }
      end

      # Makes the groups and starts the threads. It does not block.
      def start
        selected = classes

        Streams.logger.warn("ndb streams: no consumer class to run") if selected.empty?

        selected.each do |klass|
          Streams.ensure_group(klass)

          threads = @concurrency || klass.stream_options[:concurrency]

          threads.times { |index| @workers << worker(klass, index) }

          Streams.logger.info("ndb streams: #{klass.stream_label} reads #{klass.stream_name}/#{klass.stream_options[:group]} with #{threads} thread(s) as #{@consumer}")
        end

        @threads = @workers.map { |worker| Thread.new { loop_worker(worker) } }

        self
      end

      # Asks the threads to end. Each one ends its current entry, acks it
      # and stops. It is safe in a signal handler.
      def stop
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
          deadline ? thread.join([deadline - now, 0].max) : thread.join
        end
      end

      # Starts, waits for SIGTERM or SIGINT, then stops. It blocks. A second
      # signal ends the process at once.
      def run
        old = SIGNALS.to_h { |name| [name, trap_signal(name)] }

        start
        join

        nil
      ensure
        old&.each { |name, handler| Signal.trap(name, handler || "DEFAULT") }
      end

      private

      def now = Process.clock_gettime(Process::CLOCK_MONOTONIC)

      def trap_signal(name)
        Signal.trap(name) do
          exit!(1) if stopped?

          stop
        end
      end

      def worker(klass, index)
        options = klass.stream_options
        group = klass.stream_group
        dead_letter = options[:dead_letter] && "#{Streams.prefix}#{options[:dead_letter]}"

        group.worker(
          "#{@consumer}:#{index}",
          block: @block,
          claim_idle: options[:claim_idle],
          claim_every: @claim_every,
          max_deliveries: options[:max_deliveries],
          dead_letter: dead_letter,
          on_error: ->(error) { report(klass, error) }
        ) do |entry|
          Perform.call(klass, Event.from_entry(entry, group: group))
        end
      end

      # Ndb::Worker#run ends only on a stop, or on an error outside its
      # reads (the daemon is down at the start). Then it starts again.
      def loop_worker(worker)
        backoff = 0

        until worker.stopped?
          begin
            worker.run
          rescue StandardError => e
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
