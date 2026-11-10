# frozen_string_literal: true

module Ndb
  module Streams
    # One entry of a stream, as `perform` and the read methods give it.
    #
    #   event[:order_id]   # one field, by symbol or string
    #   event.to_h         # the payload, without "_id" and "_ts"
    #   event.id           # "<ms>-<seq>"
    #   event.time         # the Time of the ID
    #   event.deliveries   # 1 on the first delivery
    class Event
      attr_reader :id

      # How many times the group delivered this entry: 1 on the first
      # delivery. nil for an entry that came from a read without a group
      # (last, find, range).
      attr_reader :deliveries

      def self.from_entry(entry, deliveries: nil)
        new(id: entry.id, payload: entry.doc || {}, deliveries: deliveries)
      end

      def initialize(id:, payload:, deliveries: nil)
        @id = id
        @payload = payload
        @deliveries = deliveries
      end

      def [](key)
        @payload[key.to_s]
      end

      def key?(key)
        @payload.key?(key.to_s)
      end

      # A copy of the payload. With ActiveSupport, symbols and strings read
      # the same key.
      def to_h
        return @payload.with_indifferent_access if @payload.respond_to?(:with_indifferent_access)

        @payload.dup
      end

      def time
        Ndb::Stream.id_time(@id)
      end

      def ==(other)
        other.is_a?(Event) && other.id == @id && other.to_h == to_h
      end

      def inspect
        "#<#{self.class.name} id=#{@id.inspect} #{@payload.inspect}>"
      end
    end
  end
end
