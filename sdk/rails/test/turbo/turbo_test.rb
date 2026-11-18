# frozen_string_literal: true

# turbo-rails loads a Rails engine, so these tests run in their own process
# (rake test:turbo) with one booted app: the main suite proves that the gem
# works without turbo-rails.

require "test_helper"
require "logger"
require "stringio"
require "tmpdir"
require "minitest/mock"
require "action_controller/railtie"
require "action_cable/engine"
require "turbo-rails"
require "turbo/broadcastable/test_helper"
require "action_cable/subscription_adapter/ndb"

class NdbTurboTestApp < Rails::Application
  config.root = Dir.mktmpdir("ndb-turbo")
  config.eager_load = false
  config.logger = Logger.new(nil)
  config.secret_key_base = "x"
  config.action_cable.cable = { "adapter" => "test" }
end

NdbTurboTestApp.initialize!

class TurboTest < Minitest::Test
  include Turbo::Broadcastable::TestHelper

  WINDOW = 0.2

  class Domain < Ndb::Record
    self.collection_name = "turbo_#{NdbTestHelpers.next_name}"

    attribute :name, :string
  end

  class LiveDomain < Domain
    broadcasts_live_refreshes
  end

  class LiveUsage < Domain
    broadcasts_live_refreshes { ["domains", name] }
  end

  def setup
    super

    @log = StringIO.new
    @logger = Ndb::Streams.logger

    Ndb::Streams.logger = Logger.new(@log)
    Ndb::Turbo.reset!
  end

  def teardown
    Ndb::Turbo.window = nil
    Ndb::Streams.logger = @logger

    super
  end

  def test_the_railtie_loads_ndb_turbo_when_the_app_has_turbo_rails
    assert defined?(Ndb::Turbo)
    assert_respond_to Ndb::Record, :broadcasts_live_refreshes
  end

  def test_the_window_is_0_in_the_test_environment
    assert_equal 0, Ndb::Turbo.window
  end

  def test_the_window_is_2_seconds_outside_the_test_environment
    env = Rails.env
    Rails.env = "production"

    assert_equal 2, Ndb::Turbo.window
  ensure
    Rails.env = env
  end

  def test_the_window_can_be_set
    Ndb::Turbo.window = 5

    assert_equal 5, Ndb::Turbo.window
  end

  def test_a_refresh_of_a_record_goes_to_its_global_id
    domain = Domain.create!(name: "a.test")

    Ndb::Turbo.refresh(domain)

    tags = capture_turbo_stream_broadcasts(domain)

    assert_equal 1, tags.size
    assert_equal "refresh", tags.first["action"]
    assert_equal 1, broadcasts(domain.to_gid_param).size
  end

  def test_a_refresh_of_names_goes_to_their_stream
    Ndb::Turbo.refresh("credits", 42)

    assert_turbo_stream_broadcasts "credits:42", count: 1
  end

  def test_window_0_sends_every_call
    5.times { Ndb::Turbo.refresh("credits", 1) }

    assert_turbo_stream_broadcasts "credits:1", count: 5
  end

  def test_a_burst_sends_one_now_and_one_at_the_end_of_the_window
    Ndb::Turbo.window = WINDOW

    5.times { Ndb::Turbo.refresh("credits", 2) }

    assert_turbo_stream_broadcasts "credits:2", count: 1

    sleep WINDOW + 0.2

    assert_turbo_stream_broadcasts "credits:2", count: 2

    sleep WINDOW + 0.1

    assert_turbo_stream_broadcasts "credits:2", count: 2
  end

  def test_each_stream_has_its_own_window
    Ndb::Turbo.window = WINDOW
    domain = Domain.create!(name: "b.test")

    3.times do
      Ndb::Turbo.refresh(domain)
      Ndb::Turbo.refresh("credits", 3)
    end

    assert_turbo_stream_broadcasts domain, count: 1
    assert_turbo_stream_broadcasts "credits:3", count: 1

    sleep WINDOW + 0.2

    assert_turbo_stream_broadcasts domain, count: 2
    assert_turbo_stream_broadcasts "credits:3", count: 2
  end

  def test_a_call_after_the_window_is_sent_at_once
    Ndb::Turbo.window = WINDOW

    Ndb::Turbo.refresh("credits", 4)

    sleep WINDOW + 0.05

    Ndb::Turbo.refresh("credits", 4)

    assert_turbo_stream_broadcasts "credits:4", count: 2
  end

  def test_a_failed_broadcast_is_logged_not_raised
    Turbo::StreamsChannel.stub(:broadcast_refresh_to, ->(*) { raise IOError, "cable down" }) do
      Ndb::Turbo.refresh("credits", 5)
    end

    assert_includes @log.string, "ndb turbo: the refresh of credits:5 failed: IOError: cable down"
  end

  def test_a_failed_trailing_broadcast_is_logged_not_raised
    Ndb::Turbo.window = WINDOW

    Turbo::StreamsChannel.stub(:broadcast_refresh_to, ->(*) { raise IOError, "cable down" }) do
      2.times { Ndb::Turbo.refresh("credits", 6) }

      sleep WINDOW + 0.2
    end

    assert_equal 2, @log.string.scan("ndb turbo: the refresh of credits:6 failed").size
  end

  def test_a_record_with_no_id_is_logged_not_raised
    Ndb::Turbo.refresh(Domain.new)

    assert_includes @log.string, "ndb turbo: the refresh failed: URI::GID::MissingModelIdError"
  end

  def test_old_streams_are_forgotten
    (Ndb::Turbo::PRUNE_AT + 1).times { |i| Ndb::Turbo.refresh("prune", i) }

    assert_operator Ndb::Turbo.instance_variable_get(:@last).size, :<=, 1
  end

  def test_concurrent_calls_send_one_now_and_one_later
    Ndb::Turbo.window = WINDOW

    Array.new(8) { Thread.new { 5.times { Ndb::Turbo.refresh("credits", 7) } } }.each(&:join)

    assert_turbo_stream_broadcasts "credits:7", count: 1

    sleep WINDOW + 0.2

    assert_turbo_stream_broadcasts "credits:7", count: 2
  end

  def test_broadcasts_live_refreshes_refreshes_the_record_after_each_commit
    domain = LiveDomain.create!(name: "c.test")
    domain.update!(name: "d.test")
    domain.destroy

    assert_equal 3, broadcasts(domain.to_gid_param).size
  end

  def test_broadcasts_live_refreshes_takes_the_streams_from_a_block
    LiveUsage.create!(name: "e.test")

    assert_turbo_stream_broadcasts "domains:e.test", count: 1
  end

  # The whole path on ndb: the refresh goes through the ActionCable adapter
  # on an ndb stream to a subscriber of the record stream.
  def test_a_refresh_reaches_a_subscriber_over_the_ndb_adapter
    block = ActionCable::SubscriptionAdapter::Ndb.block
    ActionCable::SubscriptionAdapter::Ndb.block = 0.2

    domain = Domain.create!(name: "f.test")
    server = ndb_cable_server
    adapter = server.pubsub
    queue = Queue.new
    subscribed = Concurrent::Event.new

    adapter.subscribe(domain.to_gid_param, ->(message) { queue << message }, proc { subscribed.set })
    subscribed.wait(5)

    ActionCable.server.stub(:pubsub, adapter) { Ndb::Turbo.refresh(domain) }

    assert_includes JSON.parse(queue.pop(timeout: 5)), '<turbo-stream action="refresh"'
  ensure
    adapter&.shutdown
    server&.event_loop&.stop

    ActionCable::SubscriptionAdapter::Ndb.block = block
  end

  private

  def ndb_cable_server
    server = ActionCable::Server::Base.new(config: ActionCable::Server::Configuration.new)

    server.config.cable = { adapter: "ndb", stream: "cable_#{NdbTestHelpers.next_name}" }.with_indifferent_access
    server.config.logger = Logger.new(nil)

    server
  end
end
