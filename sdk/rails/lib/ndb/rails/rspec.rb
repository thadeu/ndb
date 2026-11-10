# frozen_string_literal: true

# RSpec: in spec/rails_helper.rb (or spec_helper.rb),
#
#   require "ndb/rails/rspec"
#
# Each example truncates the ndb collections it used. `ndb_clean: false`
# on an example or a group skips it. Under parallel_tests each process gets
# collections of its own (TEST_ENV_NUMBER). The examples also get the
# have_streamed matcher: expect(OrderEvent).to have_streamed(order_id: 42).

require "ndb/rails"
require "ndb/rails/testing"
require "ndb/streams/assertions"

Ndb::Testing.worker!(ENV.fetch("TEST_ENV_NUMBER", nil)) if ENV.key?("TEST_ENV_NUMBER")

RSpec.configure do |config|
  config.include Ndb::Streams::Assertions

  config.after do |example|
    Ndb::Testing.clean! unless example.metadata[:ndb_clean] == false
  end
end
