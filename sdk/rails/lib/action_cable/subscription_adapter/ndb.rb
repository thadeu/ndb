# frozen_string_literal: true

require "ndb/rails"

module ActionCable
  module SubscriptionAdapter
    # The ActionCable adapter on an ndb stream (adapter: ndb in
    # config/cable.yml). A broadcast appends one entry to the stream. Each
    # process has one listener thread that reads the stream without a group,
    # so every process gets every entry, and gives each entry to its local
    # subscribers. As Redis PUBLISH/SUBSCRIBE: at most once, and a process
    # gets only the entries appended after it starts to listen.
    #
    # cable.yml keys: stream (default "cable"), max_len (default 10_000, an
    # approximate trim on each broadcast) and channel_prefix.
    class Ndb < Base
      prepend ChannelPrefix

      DEFAULT_STREAM = "cable"
      DEFAULT_MAX_LEN = 10_000

      # Seconds that one read waits for a new entry. shutdown waits at most
      # this long for the thread.
      cattr_accessor :block, default: 5

      # Seconds that the listener waits after an error before it reads again.
      cattr_accessor :retry_after, default: 1

      def initialize(*)
        super

        @listener = nil
        @stream = nil
      end

      def broadcast(channel, payload)
        doc = { "channel" => channel, "payload" => payload }

        stream.append(doc, trim: { max_len: max_len, approx: true })
      end

      def subscribe(channel, callback, success_callback = nil)
        listener.add_subscriber(channel, callback, success_callback)
      end

      def unsubscribe(channel, callback)
        listener.remove_subscriber(channel, callback)
      end

      def shutdown
        @listener&.shutdown
      end

      def stream
        @stream || @server.mutex.synchronize do
          @stream ||= ::Ndb::Rails.client.stream(config_options.fetch(:stream, DEFAULT_STREAM))
        end
      end

      private

      def listener
        @listener || @server.mutex.synchronize { @listener ||= Listener.new(self, @server.event_loop) }
      end

      def max_len
        config_options.fetch(:max_len, DEFAULT_MAX_LEN)
      end

      def config_options
        @config_options ||= @server.config.cable.deep_symbolize_keys
      end

      # The subscribers of one process and the thread that reads the stream.
      class Listener < SubscriberMap
        READ_COUNT = 500

        def initialize(adapter, event_loop)
          super()

          @adapter = adapter
          @event_loop = event_loop

          @lock = Mutex.new
          @thread = nil
          @running = false
          @ready = false
          @waiting = []
        end

        # True when the thread knows where to start: a subscription made
        # after this gets every later broadcast.
        def ready?
          @ready
        end

        # The success callback runs once the thread is ready. Nothing goes to
        # ndb: the thread reads every entry.
        def add_channel(_channel, on_success)
          @lock.synchronize do
            start

            next unless on_success

            if @ready
              @event_loop.post(&on_success)
            else
              @waiting << on_success
            end
          end
        end

        def invoke_callback(*)
          @event_loop.post { super }
        end

        def shutdown
          thread = @lock.synchronize do
            @running = false

            @thread
          end

          thread&.join
        end

        private

        def start
          return if @thread

          @running = true
          @thread = Thread.new { listen }
        end

        def running?
          @lock.synchronize { @running }
        end

        def listen
          after = nil

          while running?
            begin
              after ||= start_id
              after = read(after)
            rescue StandardError => e
              @adapter.logger.error("ActionCable ndb adapter: #{e.class}: #{e.message}")

              sleep @adapter.retry_after
            end
          end
        end

        # The last ID of the stream: the next read gives only the entries
        # appended after it. A stream that does not exist yet starts at "0".
        # "$" on each read would lose an entry appended between two reads.
        def start_id
          last = @adapter.stream.rev_range(count: 1).first
          id = last ? last.id : ::Ndb::Stream::START_ALL

          ready!

          id
        rescue ::Ndb::UnknownCollection
          ready!

          ::Ndb::Stream::START_ALL
        end

        def ready!
          @lock.synchronize do
            @ready = true

            @waiting.each { |callback| @event_loop.post(&callback) }
            @waiting.clear
          end
        end

        # One blocking read. Returns the ID to read after next time.
        def read(after)
          entries = @adapter.stream.read(after: after, count: READ_COUNT, block: @adapter.block)

          entries.each do |entry|
            after = entry.id

            next if entry.deleted?

            broadcast(entry.doc["channel"], entry.doc["payload"])
          end

          after
        rescue ::Ndb::UnknownCollection
          sleep @adapter.retry_after

          after
        end
      end
    end
  end
end
