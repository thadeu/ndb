# frozen_string_literal: true

module Ndb
  module Streams
    # Test helpers. ndb/rails/minitest adds the assertions to
    # ActiveSupport::TestCase; ndb/rails/rspec adds the have_streamed matcher.
    #
    #   assert_streamed OrderEvent, order_id: 42
    #   assert_no_streamed OrderEvent
    #   expect(OrderEvent).to have_streamed(order_id: 42)
    module Assertions
      # The most entries a check reads, from the newest.
      LIMIT = 1000

      # The entries of the stream of klass whose payload has these fields.
      # Values are compared as JSON, so a symbol is equal to its string.
      def self.matching(klass, subset)
        wanted = JSON.parse(JSON.generate(subset))

        klass.last(LIMIT).select do |event|
          payload = event.to_h

          wanted.all? { |key, value| payload.key?(key) && payload[key] == value }
        end
      end

      def self.describe(klass, subset)
        fields = subset.empty? ? "an entry" : "an entry with #{subset.inspect}"

        "#{fields} in the stream #{klass.stream_name.inspect}"
      end

      # Passes when the stream of klass has an entry with these fields (any
      # entry, when there are no fields). Returns the matching events.
      def assert_streamed(klass, **subset)
        found = Assertions.matching(klass, subset)

        assert !found.empty?, "Expected #{Assertions.describe(klass, subset)}, found none (#{klass.count} entries)"

        found
      end

      # Passes when the stream of klass has no entry with these fields.
      def assert_no_streamed(klass, **subset)
        found = Assertions.matching(klass, subset)

        assert found.empty?, "Expected no #{Assertions.describe(klass, subset)}, found #{found.size}"
      end

      # The RSpec matcher: expect(OrderEvent).to have_streamed(order_id: 42)
      def have_streamed(**subset)
        Matcher.new(subset)
      end

      # A matcher with the protocol of RSpec. It needs no RSpec to load.
      class Matcher
        def initialize(subset)
          @subset = subset
        end

        def matches?(klass)
          @klass = klass

          !Assertions.matching(klass, @subset).empty?
        end

        def description
          "have streamed #{@subset.inspect}"
        end

        def failure_message
          "expected #{Assertions.describe(@klass, @subset)}, found none (#{@klass.count} entries)"
        end

        def failure_message_when_negated
          "expected no #{Assertions.describe(@klass, @subset)}, found one"
        end
      end
    end
  end
end
