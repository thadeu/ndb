# frozen_string_literal: true

require "json"
require "logger"
require "socket"
require "ndb"

require "ndb/streams/errors"
require "ndb/streams/options"
require "ndb/streams/event"
require "ndb/streams/class_methods"
require "ndb/streams/perform"
require "ndb/streams/runner"
require "ndb/streams/boot"

module Ndb
  # Streams for an app: one class appends to a stream, consumes it, or does
  # both. It needs no Rails.
  #
  #   class OrderEvent            # a writer
  #     include Ndb::Streams
  #   end
  #
  #   OrderEvent.append(order_id: 42)
  #
  #   class BillingPaid           # a consumer
  #     include Ndb::Streams
  #
  #     stream :order_events, group: :billing
  #
  #     def perform(event) = Invoice.create!(order_id: event[:order_id])
  #   end
  #
  # `bundle exec ndb streams` runs the consumers. Ndb::Stream (singular) is
  # the low-level client of the ndb gem.
  module Streams
    @registry = []
    @lock = Mutex.new
    @inline = false

    class << self
      attr_writer :client, :logger

      def included(base)
        base.extend(ClassMethods)

        register(base)
      end

      # Adds a class. A class with the same name replaces the old one, as
      # after a code reload.
      def register(klass)
        @lock.synchronize do
          @registry.reject! { |known| known.equal?(klass) || (klass.name && known.name == klass.name) }

          @registry << klass
        end
      end

      # Every class with the include, and their subclasses.
      def classes
        @lock.synchronize { @registry.dup }
      end

      # The classes with a `perform`. It raises MissingGroup or
      # DuplicateGroup when the config of one of them is wrong.
      def consumers
        found = classes.select(&:stream_consumer?)

        validate!(found)

        found
      end

      # The consumers of one stream (its name without the prefix).
      def consumers_of(stream_name)
        consumers.select { |klass| klass.stream_name == stream_name.to_s }
      end

      # Forgets every class (for tests).
      def reset!
        @lock.synchronize { @registry.clear }
      end

      # The client of the app: the one of Ndb::Rails, or a client from
      # NDB_URL and NDB_TOKEN. `Ndb::Streams.client = ...` sets another.
      def client
        return @client if @client
        return Ndb::Rails.client if Ndb.const_defined?(:Rails) && Ndb::Rails.respond_to?(:client)

        @default_client ||= Ndb::Client.new
      end

      # The prefix of the stream names ("test2_" for a parallel test worker).
      def prefix
        return "" unless Ndb.const_defined?(:Rails) && Ndb::Rails.respond_to?(:configuration)

        Ndb::Rails.configuration.collection_prefix.to_s
      end

      # The `streams:` block of config/ndb.yml: "concurrency" and "only".
      def settings
        return {} unless Ndb.const_defined?(:Rails) && Ndb::Rails.respond_to?(:configuration)

        Ndb::Rails.configuration.streams || {}
      end

      def logger
        return @logger if @logger

        app_logger = ::Rails.logger if defined?(::Rails) && ::Rails.respond_to?(:logger)

        app_logger || (@default_logger ||= Logger.new($stdout))
      end

      # Inline mode, for tests: an append writes the entry and then calls
      # `perform` of each consumer of the stream, in the caller thread.
      def inline!
        @inline = true
      end

      def inline=(value)
        @inline = value ? true : false
      end

      def inline?
        @inline
      end

      # Runs the consumers in this thread until SIGTERM or SIGINT. only: the
      # names of the classes to run; concurrency: the threads of each class.
      # The defaults come from `streams:` in config/ndb.yml.
      def run(only: nil, concurrency: nil)
        runner(only: only, concurrency: concurrency).run
      end

      # A Runner that is not started: #start, #stop and #join.
      def runner(**options)
        Runner.new(**options)
      end

      # Makes the stream (when it has `retain`) and the group of a consumer.
      def ensure_group(klass)
        klass.ensure_stream!

        start = klass.stream_options[:start] == :all ? Ndb::Stream::START_ALL : Ndb::Stream::START_NEW

        klass.stream_group.ensure(start)
      end

      # Makes the missing groups of all consumers.
      def ensure_groups!
        consumers.each { |klass| ensure_group(klass) }
      end

      # The class that has this name now (a code reload makes a new class).
      def current(klass)
        return klass unless klass.name && Object.const_defined?(klass.name)

        Object.const_get(klass.name)
      end

      # Loads the classes of app/streams of the Rails app, when there is one.
      def load_app!(app = rails_app)
        return false unless app

        dir = app.root.join("app", "streams")

        return false unless dir.directory?

        ::Rails.autoloaders.main.eager_load_dir(dir.to_s)

        true
      end

      def rails_app
        return nil unless defined?(::Rails) && ::Rails.respond_to?(:application)

        ::Rails.application
      end

      # Runs the block after the commit of the open ActiveRecord
      # transactions (a rollback drops it), or now when there is none.
      def after_commit(&block)
        if defined?(::ActiveRecord) && ::ActiveRecord.respond_to?(:after_all_transactions_commit)
          ::ActiveRecord.after_all_transactions_commit(&block)
        else
          block.call
        end
      end

      def instrument(name, payload, &block)
        return block.call unless defined?(ActiveSupport::Notifications)

        ActiveSupport::Notifications.instrument(name, payload, &block)
      end

      private

      def validate!(found)
        groups = {}

        found.each do |klass|
          group = klass.stream_options[:group]

          if group.nil?
            raise MissingGroup, "#{klass.stream_label} has a perform method but no group: add `stream :#{klass.stream_name}, group: :a_name`"
          end

          key = [klass.stream_name, group]
          other = groups[key]

          if other
            raise DuplicateGroup, "#{other.stream_label} and #{klass.stream_label} both use the group #{group.inspect} on the stream #{klass.stream_name.inspect}: give each class its own group"
          end

          groups[key] = klass
        end
      end
    end
  end
end
