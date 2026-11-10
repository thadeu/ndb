# frozen_string_literal: true

require "test_helper"
require "tempfile"

class UserAgentTest < Minitest::Test
  RAILS_TOKENS = "ndb-rails/#{Ndb::Rails::VERSION} rails/#{Rails.version}".freeze

  def teardown
    Ndb::Rails.configuration.user_agent = nil
    Ndb::Rails.reset_client!
  end

  def test_client_adds_the_gem_and_rails_after_the_ndb_gem
    Ndb::Rails.reset_client!

    assert_equal "#{Ndb::Client::USER_AGENT} #{RAILS_TOKENS}", Ndb::Rails.client.user_agent
  end

  def test_config_user_agent_appends_the_app_token
    Ndb::Rails.configure { |c| c.user_agent = "myapp/1.2" }

    assert_equal "#{Ndb::Client::USER_AGENT} #{RAILS_TOKENS} myapp/1.2", Ndb::Rails.client.user_agent
  end

  def test_config_file_reads_user_agent
    file = Tempfile.new(["ndb", ".yml"])
    file.write("test:\n  user_agent: shop/3\n")
    file.close

    config = Ndb::Rails::Configuration.new.load_file(file.path, "test")

    assert_equal "shop/3", config.user_agent
  ensure
    file&.unlink
  end

  def test_the_daemon_answers_with_the_header
    Ndb::Rails.configure do |c|
      c.socket = TestDaemon.socket
      c.user_agent = "myapp/1.2"
    end

    assert Ndb::Rails.client.ensure_collection(NdbTestHelpers.next_name)
  end
end
