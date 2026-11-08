# frozen_string_literal: true

# DatabaseCleaner (2.x): in the test setup,
#
#   require "ndb/rails/database_cleaner"
#
#   DatabaseCleaner[:ndb].strategy = :touched                  # the default: what the test used
#   DatabaseCleaner[:ndb].strategy = :truncation, { only: %w[calls] }
#   DatabaseCleaner[:ndb].clean_with(:truncation)              # every collection, before the suite
#
# Needs the database_cleaner-core gem. ndb has no transactions: both
# strategies truncate, which takes a few ms per collection whatever its
# size. :touched truncates the collections the tests used since the last
# clean; :truncation truncates every collection of the worker (or only:,
# less except:).

require "database_cleaner/core"
require "ndb/rails"
require "ndb/rails/testing"

module DatabaseCleaner
  module Ndb
    # Truncates the collections the tests used since the last clean.
    class Touched < DatabaseCleaner::Strategy
      # DatabaseCleaner passes the options as a Hash: strategy = :touched, { except: [...] }
      def initialize(options = {}, **keywords)
        super()

        @except = options.merge(keywords).fetch(:except, [])
      end

      def clean
        ::Ndb::Testing.clean!(except: @except)
      end
    end

    # Truncates every engine collection of the worker, or only these.
    class Truncation < DatabaseCleaner::Strategy
      def initialize(options = {}, **keywords)
        super()

        options = options.merge(keywords)

        @only = options[:only]
        @except = options.fetch(:except, [])
      end

      def clean
        @only ? ::Ndb::Testing.clean!(only: @only, except: @except) : ::Ndb::Testing.clean_all!(except: @except)
      end
    end
  end
end

DatabaseCleaner[:ndb].strategy = :touched
