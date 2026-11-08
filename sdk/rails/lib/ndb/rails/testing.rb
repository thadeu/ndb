# frozen_string_literal: true

module Ndb
  # Test support: clean the collections a test used, and give each parallel
  # worker collections of its own. ndb has no transactions, so a test cannot
  # roll back: the collections it touched are truncated after it, which
  # takes a few ms whatever their size and keeps their definition and
  # indexes.
  #
  #   require "ndb/rails/test_help"             # Minitest (ActiveSupport::TestCase)
  #   require "ndb/rails/rspec"                 # RSpec
  #   require "ndb/rails/database_cleaner"      # DatabaseCleaner[:ndb]
  #
  # Every clean refuses to run outside the test environment (a safeguard,
  # like DatabaseCleaner's): set NDB_ALLOW_CLEAN=1 to force it.
  module Testing
    # Raised when a clean would run outside the test environment.
    class UnsafeClean < Ndb::Error; end

    # The Minitest hooks: truncate after each test. ndb/rails/test_help puts
    # them in ActiveSupport::TestCase; include them in another Minitest::Test.
    # `self.ndb_clean = false` turns them off for a test case.
    module Minitest
      def self.included(base)
        base.class_attribute :ndb_clean, default: true if base.respond_to?(:class_attribute)
      end

      def after_teardown
        super
      ensure
        Ndb::Testing.clean! if !respond_to?(:ndb_clean) || ndb_clean
      end
    end

    @touched = Set.new
    @lock = Mutex.new

    class << self
      # Records that a model used a collection (Ndb::Record calls it).
      def touch(name)
        @lock.synchronize { @touched << name }
      end

      def touched
        @lock.synchronize { @touched.to_a }
      end

      # Truncates the collections the tests used since the last clean (or
      # only these), less except.
      def clean!(only: nil, except: [])
        guard!

        names = (only || touched).map(&:to_s) - except.map(&:to_s)

        names.each { |name| truncate(name) }

        @lock.synchronize { @touched.clear }

        names
      end

      # Truncates every engine collection of the daemon (with the prefix of
      # this worker, when there is one), less except: for the start of a run.
      def clean_all!(except: [])
        guard!

        prefix = Rails.configuration.collection_prefix.to_s

        names = Rails.client.collections.filter_map do |definition|
          name = definition["name"]

          next if definition["write"] == "direct" || definition["partition_of"]
          next unless name.start_with?(prefix)

          name
        end

        clean!(only: names, except: except)
      end

      # Gives a parallel worker collections of its own: "test<n>_calls".
      # Rails parallelize and parallel_tests (TEST_ENV_NUMBER) call it.
      def worker!(number)
        Rails.configuration.collection_prefix = "test#{number.to_s.empty? ? 1 : number}_"
        Rails.reset_client!
      end

      def guard!
        return if ENV["NDB_ALLOW_CLEAN"] == "1"
        return if defined?(::Rails) && ::Rails.respond_to?(:env) && ::Rails.env.test?
        return if ENV["RAILS_ENV"] == "test" || ENV["RACK_ENV"] == "test"

        raise UnsafeClean, "ndb test clean refused: this is not the test environment (set NDB_ALLOW_CLEAN=1 to force it)"
      end

      private

      def truncate(name)
        Rails.client.truncate(name)
      rescue Ndb::UnknownCollection, Ndb::ReadOnlyCollection
        nil
      end
    end
  end
end
