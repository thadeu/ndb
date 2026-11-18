# frozen_string_literal: true

require "test_helper"
require "open3"

# turbo-rails is not a dependency of the gem. This suite runs without it
# (the tests of Ndb::Turbo run in their own process, test/turbo).
class TurboOptionalTest < Minitest::Test
  def test_this_suite_runs_without_turbo
    refute defined?(::Turbo), "turbo-rails must not be loaded in the main suite"
    refute defined?(Ndb::Turbo)
    refute_respond_to Ndb::Record, :broadcasts_live_refreshes
  end

  # An explicit require works without turbo-rails, and a refresh only logs
  # the error: it never fails the work that made the change.
  def test_ndb_turbo_loads_without_turbo_rails
    script = <<~RUBY
      require "logger"
      require "stringio"
      require "ndb/rails"
      require "ndb/turbo"

      log = StringIO.new
      Ndb::Streams.logger = Logger.new(log)

      Ndb::Turbo.refresh("credits", 1)

      puts [defined?(::Turbo::StreamsChannel).inspect, Ndb::Turbo.window, log.string.include?("ndb turbo:")].join(" ")
    RUBY

    out, err, status = Open3.capture3({ "RAILS_ENV" => nil }, RbConfig.ruby, "-I", File.expand_path("../lib", __dir__), "-e", script)

    assert status.success?, err
    assert_equal "nil 2 true", out.strip
  end
end
