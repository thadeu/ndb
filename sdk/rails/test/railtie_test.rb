# frozen_string_literal: true

require "test_helper"
require "tmpdir"

class RailtieTest < Minitest::Test
  def test_configuration_reads_the_block_of_the_environment
    Dir.mktmpdir do |dir|
      path = File.join(dir, "ndb.yml")

      File.write(path, <<~YAML)
        default: &default
          timeout: 7
        development:
          <<: *default
          socket: /tmp/dev.sock
        production:
          socket: <%= "/run/ndb/" + "ndb.sock" %>
          pool_size: 4
      YAML

      dev = Ndb::Rails::Configuration.new.load_file(path, "development")
      prod = Ndb::Rails::Configuration.new.load_file(path, "production")

      assert_equal ["/tmp/dev.sock", 7, 16], [dev.socket, dev.timeout, dev.pool_size]
      assert_equal ["/run/ndb/ndb.sock", 30, 4], [prod.socket, prod.timeout, prod.pool_size]
    end
  end

  def test_configuration_reads_url_and_token_from_yml
    Dir.mktmpdir do |dir|
      path = File.join(dir, "ndb.yml")

      File.write(path, "production:\n  url: tcp://ndb.internal:7070\n  token: abc\n")

      config = Ndb::Rails::Configuration.new.load_file(path, "production")

      assert_equal "tcp://ndb.internal:7070", config.url
      assert_equal "abc", config.token
    end
  end

  def test_collection_prefix_reads_ndb_prefix
    ENV["NDB_PREFIX"] = "env_"

    assert_equal "env_", Ndb::Rails::Configuration.new.collection_prefix
  ensure
    ENV.delete("NDB_PREFIX")
  end

  def test_activerecord_like_names_are_aliases
    assert_same Ndb::UniqueViolation, Ndb::RecordNotUnique
    assert_kind_of Ndb::Client, Ndb::Rails.client
  end

  # One Rails app per process: this test boots it and checks every effect.
  def test_boot_reads_ndb_yml_and_ensures_collections_when_eager_loading
    klass = model do
      attribute :a, :string, index: true
    end

    stream = "boot_#{NdbTestHelpers.next_name}"

    Ndb::Streams.reset!

    Dir.mktmpdir do |dir|
      FileUtils.mkdir_p(File.join(dir, "config"))
      File.write(File.join(dir, "config", "ndb.yml"), "test:\n  socket: #{TestDaemon.socket}\n  timeout: 3\n  streams:\n    concurrency: 2\n")

      FileUtils.mkdir_p(File.join(dir, "app", "streams"))
      File.write(File.join(dir, "app", "streams", "railtie_boot_consumer.rb"), <<~RUBY)
        class RailtieBootConsumer
          include Ndb::Streams

          stream "#{stream}", group: :boot

          def perform(event) = nil
        end
      RUBY

      app = Class.new(Rails::Application) do
        config.root = dir
        config.eager_load = true
        config.logger = Logger.new(nil)
        config.secret_key_base = "x"
        config.eager_load_paths.clear
      end

      def app.name = "NdbRailtieTestApp"
      app.initialize!

      assert_equal TestDaemon.socket, Ndb::Rails.configuration.socket
      assert_equal 3, Ndb::Rails.configuration.timeout
      assert(client.collections.any? { |c| c["name"] == klass.collection_name })
      assert_equal({ "concurrency" => 2 }, Ndb::Rails.configuration.streams)

      boot = Thread.list.find { |thread| thread.name == "ndb-streams-boot" }

      boot&.join(10)

      assert_equal ["RailtieBootConsumer"], Ndb::Streams.consumers.map(&:name)
      assert_equal ["boot"], client.stream(stream).groups.map { |row| row["name"] }
    end
  ensure
    Ndb::Rails.configuration.streams = {}
    Ndb::Streams.reset!
  end
end
