# frozen_string_literal: true

# Minitest, the Rails way: in test/test_helper.rb, after rails/test_help,
#
#   require "ndb/rails/test_help"
#
# Every ActiveSupport::TestCase truncates the ndb collections it used after
# each test, and each worker of `parallelize` gets collections of its own.
# A test case opts out with `self.ndb_clean = false`.

require "ndb/rails"
require "ndb/rails/testing"

Ndb::Testing.worker!(ENV.fetch("TEST_ENV_NUMBER", nil)) if ENV.key?("TEST_ENV_NUMBER")

ActiveSupport.on_load(:active_support_test_case) do
  include Ndb::Testing::Minitest

  parallelize_setup { |worker| Ndb::Testing.worker!(worker) } if respond_to?(:parallelize_setup)
end
