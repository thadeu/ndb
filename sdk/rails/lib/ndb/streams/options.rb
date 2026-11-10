# frozen_string_literal: true

module Ndb
  module Streams
    # The options of `stream`: their defaults and their checks.
    module Options
      DEFAULTS = {
        group: nil,
        concurrency: 1,
        max_deliveries: nil,
        dead_letter: nil,
        claim_idle: 60.0,
        retry: nil,
        max_age: nil,
        start: :new,
        discard_on: [].freeze,
        max_len: nil,
        retain: nil
      }.freeze

      STARTS = %i[new all].freeze

      # The highest sequence of an ID: the end of one millisecond.
      MAX_SEQ = (2**64) - 1

      class << self
        # The options as the gem keeps them. An unknown key or a bad value
        # raises ArgumentError.
        def normalize(options)
          unknown = options.keys - DEFAULTS.keys

          unless unknown.empty?
            raise ArgumentError, "unknown stream option: #{unknown.map(&:inspect).join(', ')} (known: #{DEFAULTS.keys.join(', ')})"
          end

          options.to_h { |key, value| [key, send(key, value)] }
        end

        # "order_events" for OrderEvent, "billing_order_events" for
        # Billing::OrderEvent, "categories" for Category. The words come from
        # the ActiveSupport inflections (the gem requires them), so an
        # inflection of the app (config/initializers/inflections.rb) applies.
        def default_name(klass)
          name = klass.name

          raise ConfigurationError, "a class without a name needs `stream :name`" if name.nil?

          name.gsub("::", "_").underscore.pluralize
        end

        # Hash: one entry. Array of Hash: many entries. String: JSON of one
        # of the two. Returns [docs, many].
        def payload(value)
          case value
          when Hash
            [[value], false]
          when Array
            bad = value.find { |doc| !doc.is_a?(Hash) }

            raise ArgumentError, "each entry of a stream is a Hash, not #{bad.class}" if bad

            [value, true]
          when String
            payload(parse(value))
          else
            raise ArgumentError, "a stream payload is a Hash, an Array of Hash or a JSON string, not #{value.class}"
          end
        end

        # A Time or an ID as an ID of a range. edge is :from or :to.
        def range_id(value, edge)
          return value.to_s unless value.respond_to?(:to_time) && !value.is_a?(String)

          time = value.to_time
          ms = (time.to_i * 1000) + (time.nsec / 1_000_000)

          edge == :from ? "#{ms}-0" : "#{ms}-#{MAX_SEQ}"
        end

        private

        def parse(json)
          parsed = JSON.parse(json)

          return parsed if parsed.is_a?(Hash) || parsed.is_a?(Array)

          raise ArgumentError, "the JSON of a stream payload is an object or an array"
        rescue JSON::ParserError => e
          raise ArgumentError, "the stream payload is not JSON: #{e.message}"
        end

        def group(value)
          value&.to_s
        end

        def concurrency(value)
          positive_integer(:concurrency, value)
        end

        def max_deliveries(value)
          value.nil? ? nil : positive_integer(:max_deliveries, value)
        end

        def max_len(value)
          value.nil? ? nil : positive_integer(:max_len, value)
        end

        # false: no dead letter stream.
        def dead_letter(value)
          return value if value.nil? || value == false

          value.to_s
        end

        # Seconds: a number or an ActiveSupport duration.
        def claim_idle(value)
          return nil if value.nil?

          raise ArgumentError, "claim_idle is a number of seconds or a duration, not #{value.inspect}" unless value.respond_to?(:to_f) && !value.is_a?(String)

          value.to_f
        end

        # The retry schedule: an Array of waits (seconds or durations; the
        # last one repeats) is an Ndb::Stream::Tiers; a callable
        # ->(deliveries) { seconds } stays as it is. nil: a failed entry
        # waits claim_idle.
        def retry(value)
          return nil if value.nil?
          return value if value.respond_to?(:call)

          unless value.is_a?(Array)
            raise ArgumentError, "retry is an Array of waits or a callable ->(deliveries) { seconds }, not #{value.inspect}"
          end

          Ndb::Stream.tiers(*value.map { |wait| wait.is_a?(Hash) ? wait : seconds(:retry, wait) })
        end

        # Seconds: a number or an ActiveSupport duration, greater than 0.
        def max_age(value)
          return nil if value.nil?

          age = seconds(:max_age, value)

          raise ArgumentError, "max_age is greater than 0, not #{value.inspect}" unless age.positive?

          age
        end

        def seconds(name, value)
          return value.to_f if value.respond_to?(:to_f) && !value.is_a?(String)

          raise ArgumentError, "#{name} takes numbers of seconds or durations, not #{value.inspect}"
        end

        def start(value)
          value = value.to_sym if value.respond_to?(:to_sym)

          raise ArgumentError, "start is :new or :all, not #{value.inspect}" unless STARTS.include?(value)

          value
        end

        def discard_on(value)
          classes = Array(value)
          bad = classes.find { |klass| !(klass.is_a?(Class) && klass <= Exception) }

          raise ArgumentError, "discard_on takes exception classes, not #{bad.inspect}" if bad

          classes.freeze
        end

        # The retention of the daemon: "30d" as it is, a number or a
        # duration as seconds.
        def retain(value)
          return nil if value.nil?
          return value if value.is_a?(String)

          "#{positive_integer(:retain, value.to_i)}s"
        end

        def positive_integer(name, value)
          return value if value.is_a?(Integer) && value.positive?

          raise ArgumentError, "#{name} is an integer greater than 0, not #{value.inspect}"
        end
      end
    end
  end
end
