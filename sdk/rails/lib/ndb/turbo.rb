# frozen_string_literal: true

require "ndb/rails"

module Ndb
  # Turbo Streams helpers. They need turbo-rails, which is not a dependency
  # of the gem: the railtie loads this file when the app has turbo-rails, or
  # `require "ndb/turbo"` loads it.
  #
  # refresh tells the open pages of a stream to refresh (a Turbo refresh,
  # a morph with turbo_refreshes_with), at most once per window: a burst of
  # changes gives one refresh at once and one at the end of the window, so
  # the last change always shows. Each stream has its own window, in each
  # process.
  #
  #   Ndb::Turbo.refresh(domain)                # the stream of a record (its GlobalID)
  #   Ndb::Turbo.refresh("credits", account.id) # the stream "credits:<id>"
  module Turbo
    DEFAULT_WINDOW = 2

    # A call forgets the streams whose window is over when it knows more
    # than this many.
    PRUNE_AT = 1_000

    @lock = Mutex.new
    @last = {}
    @pending = {}
    @window = nil

    class << self
      attr_writer :window

      # Seconds between two refreshes of one stream: 2, and 0 in the test
      # environment (each call sends one, so a test can count them).
      def window
        @window || (test_env? ? 0 : DEFAULT_WINDOW)
      end

      # Sends a Turbo refresh to the stream of the streamables, now or at
      # the end of the window. It never raises: an error goes to the log.
      def refresh(*streamables)
        name = ::Turbo::StreamsChannel.send(:stream_name_from, streamables)
        wait = reserve(name)

        return if wait.nil?

        wait.positive? ? later(name, streamables, wait) : broadcast(name, streamables)
      rescue StandardError => e
        Streams.logger.warn("ndb turbo: the refresh failed: #{e.class}: #{e.message}")
      end

      # Forgets every window (for the tests).
      def reset!
        @lock.synchronize do
          @last.clear
          @pending.clear
        end
      end

      private

      # 0 to send now, the seconds to wait for a trailing refresh, or nil
      # when a trailing refresh of the stream is already pending.
      def reserve(name)
        @lock.synchronize do
          now = clock
          wait = @last.fetch(name, -Float::INFINITY) + window.to_f - now

          prune(now) if @last.size >= PRUNE_AT

          if wait <= 0
            @last[name] = now

            0
          elsif @pending[name]
            nil
          else
            @pending[name] = true

            wait
          end
        end
      end

      def later(name, streamables, wait)
        Thread.new do
          sleep wait

          @lock.synchronize do
            @pending.delete(name)
            @last[name] = clock
          end

          broadcast(name, streamables)
        end
      end

      # A failed broadcast never fails the work that made the change.
      def broadcast(name, streamables)
        ::Turbo::StreamsChannel.broadcast_refresh_to(*streamables)
      rescue StandardError => e
        Streams.logger.warn("ndb turbo: the refresh of #{name} failed: #{e.class}: #{e.message}")
      end

      def prune(now)
        limit = window.to_f

        @last.delete_if { |name, at| at + limit <= now && !@pending[name] }
      end

      def test_env?
        return ::Rails.env.test? if defined?(::Rails) && ::Rails.respond_to?(:env)

        ENV["RAILS_ENV"] == "test" || ENV["RACK_ENV"] == "test"
      end

      def clock = Process.clock_gettime(Process::CLOCK_MONOTONIC)
    end

    # The model macro. After each commit (create, update, destroy) the open
    # pages of the record refresh, or the pages of the streams that the
    # block gives (it runs on the record).
    #
    #   class Domain < Ndb::Record
    #     broadcasts_live_refreshes
    #   end
    #
    #   class Usage < Ndb::Record
    #     broadcasts_live_refreshes { ["credits", account_id] }
    #   end
    module Broadcasts
      extend ActiveSupport::Concern

      class_methods do
        def broadcasts_live_refreshes(&streams)
          after_commit do
            streamables = streams ? Array.wrap(instance_exec(&streams)) : [self]

            Ndb::Turbo.refresh(*streamables)
          end
        end
      end
    end
  end

  Record.include(Turbo::Broadcasts)
end
