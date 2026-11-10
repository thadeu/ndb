# frozen_string_literal: true

module Ndb
  module Streams
    # The class methods that `include Ndb::Streams` adds.
    module ClassMethods
      ENSURE_LOCK = Mutex.new

      # A subclass starts with the config of its parent.
      def inherited(subclass)
        super

        subclass.instance_variable_set(:@ndb_stream_name, @ndb_stream_name)
        subclass.instance_variable_set(:@ndb_stream_config, @ndb_stream_config)
        subclass.instance_variable_set(:@ndb_around_performs, @ndb_around_performs)

        Streams.register(subclass)
      end

      # Sets the stream of the class and its options:
      #
      #   stream :orders, group: :billing, concurrency: 2, max_deliveries: 5
      #   stream :orders, group: :billing, retry: [15, 1.minute, 10.minutes], max_age: 15.days
      #   stream OrderEvent, group: :billing    # the stream of another class
      #
      # See Options::DEFAULTS for the options.
      def stream(name = nil, **options)
        config = Options.normalize(options)

        @ndb_stream_name = name.respond_to?(:stream_name) ? name.stream_name : name.to_s unless name.nil?
        @ndb_stream_config = (@ndb_stream_config || {}).merge(config).freeze

        nil
      end

      # Runs the block around each `perform`, on the instance. The block
      # calls `block.call` to go on.
      #
      #   around_perform { |event, block| Tenant.with(event[:tenant]) { block.call } }
      def around_perform(&hook)
        raise ArgumentError, "around_perform needs a block" unless hook

        @ndb_around_performs = [*@ndb_around_performs, hook].freeze

        nil
      end

      def around_performs
        @ndb_around_performs || []
      end

      # The name from `stream`, or from the class name (OrderEvent is
      # "order_events"). The name from the class is kept after the first
      # call.
      def stream_name
        @ndb_stream_name || (@ndb_default_stream_name ||= Options.default_name(self))
      end

      # Every option with its value. dead_letter is "<stream>_dead" when
      # max_deliveries or max_age is set.
      def stream_options
        options = Options::DEFAULTS.merge(@ndb_stream_config || {})
        buries = options[:max_deliveries] || options[:max_age]

        options[:dead_letter] = "#{stream_name}_dead" if options[:dead_letter].nil? && buries
        options[:dead_letter] = nil if options[:dead_letter] == false

        options
      end

      # True for a class with a `perform` instance method.
      def stream_consumer?
        method_defined?(:perform) || private_method_defined?(:perform)
      end

      # The class in a message.
      def stream_label
        name || "a class of the stream #{@ndb_stream_name.inspect}"
      end

      # The low-level stream (Ndb::Stream) of the class.
      def ndb_stream
        Streams.client.stream("#{Streams.prefix}#{stream_name}")
      end

      # The low-level group (Ndb::Group) of a consumer class.
      def stream_group
        group = stream_options[:group]

        raise MissingGroup, "#{stream_label} has no group: add `stream :#{stream_name}, group: :a_name`" if group.nil?

        ndb_stream.group(group)
      end

      # Adds one entry (a Hash) and returns its ID, or many entries (an
      # Array of Hash) and returns their IDs. A JSON string is parsed first.
      # In an ActiveRecord transaction the append waits for the commit and
      # returns nil; a rollback drops it.
      def append(payload)
        docs, many = Options.payload(payload)

        return [] if docs.empty?

        result = nil

        Streams.after_commit { result = write_entries(docs, many) }

        result
      end

      alias enqueue append
      alias perform_async append

      # Runs perform on one payload now, in this thread, with no request:
      # nothing is written to ndb and nothing is acked. It takes the path of
      # a delivery (the Rails reloader, around_perform, discard_on) and
      # returns the value of perform (nil when discard_on took the error).
      # For a test, or one entry by hand in a console.
      #
      # The payload is a Hash, a JSON string or the fields as keywords. The
      # names that start with _ are not payload fields (they are reserved in
      # a stream): _id is the entry ID of the event, _deliveries its count.
      #
      #   PostCallJob.perform_now(call_id: 42)
      #   PostCallJob.perform_now({ "call_id" => 42 }, _id: "1731163600000-0", _deliveries: 3)
      #   PostCallJob.perform_now('{"call_id":42}')
      def perform_now(payload = nil, **fields)
        raise ArgumentError, "#{stream_label} has no perform: perform_now runs a consumer" unless stream_consumer?

        meta = fields.select { |key, _| key.to_s.start_with?("_") }
        fields = fields.reject { |key, _| meta.key?(key) }
        docs, many = Options.payload(payload.nil? ? fields : payload)

        raise ArgumentError, "perform_now takes one entry, not an Array" if many

        unknown = meta.keys.map(&:to_s) - %w[_id _deliveries]

        raise ArgumentError, "perform_now: unknown option #{unknown.first}" if unknown.any?

        id = meta[:_id] || "#{Process.clock_gettime(Process::CLOCK_REALTIME, :millisecond)}-0"
        payload = JSON.parse(JSON.generate(docs.first)).reject { |key, _| key.start_with?("_") }
        event = Event.new(id: id.to_s, payload: payload, deliveries: meta.fetch(:_deliveries, 1))

        Perform.now(self, event)
      end

      # The newest entry, or nil. With n: the n newest entries, oldest first.
      def last(count = nil)
        events = read_events { |stream| stream.rev_range(count: count || 1) }

        count ? events.reverse : events.first
      end

      # The entry with this ID, or nil.
      def find(id)
        read_events { |stream| stream.range(id.to_s, id.to_s, count: 1) }.first
      end

      # The entries from `from` to `to`, both included, oldest first. Each
      # one is an ID or a Time. The defaults are the two ends of the stream.
      def range(from = "-", to = "+", count: nil)
        start = Options.range_id(from, :from)
        stop = Options.range_id(to, :to)

        read_events { |stream| stream.range(start, stop, count: count) }
      end

      # How many entries the stream has.
      def count
        ndb_stream.info.fetch("length")
      rescue Ndb::UnknownCollection
        0
      end

      # Makes the stream with its retention, one time for each client. A
      # stream without `retain` is made by the first append.
      def ensure_stream!
        retain = stream_options[:retain]

        return if retain.nil?

        stream = ndb_stream
        key = [stream.client, stream.name, retain]

        return if @ndb_stream_ensured == key

        ENSURE_LOCK.synchronize do
          stream.create(retention: retain)

          @ndb_stream_ensured = key
        end
      end

      private

      def read_events
        yield(ndb_stream).map { |entry| Event.from_entry(entry) }
      rescue Ndb::UnknownCollection
        []
      end

      def write_entries(docs, many)
        ensure_stream!

        stream = ndb_stream
        max_len = stream_options[:max_len]
        trim = max_len ? { max_len: max_len } : nil

        Ndb::Testing.touch(stream.name) if Ndb.const_defined?(:Testing)

        ids = Streams.instrument("append.ndb_streams", stream: stream_name, class: self, count: docs.size) do
          many ? stream.append_many(docs, trim: trim) : [stream.append(docs.first, trim: trim)]
        end

        Perform.inline(stream_name, ids, docs) if Streams.inline?

        many ? ids : ids.first
      end
    end
  end
end
