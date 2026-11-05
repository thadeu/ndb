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

      dev = NdbRecord::Configuration.new.load_file(path, "development")
      prod = NdbRecord::Configuration.new.load_file(path, "production")

      assert_equal ["/tmp/dev.sock", 7, 16], [dev.socket, dev.timeout, dev.pool_size]
      assert_equal ["/run/ndb/ndb.sock", 30, 4], [prod.socket, prod.timeout, prod.pool_size]
    end
  end

  # One Rails app per process: this test boots it and checks both effects.
  def test_boot_reads_ndb_yml_and_ensures_collections_when_eager_loading
    klass = model do
      primary_key :cid
      attribute :a, :string, index: true
    end

    Dir.mktmpdir do |dir|
      FileUtils.mkdir_p(File.join(dir, "config"))
      File.write(File.join(dir, "config", "ndb.yml"), "test:\n  socket: #{TestDaemon.socket}\n  timeout: 3\n")

      app = Class.new(Rails::Application) do
        config.root = dir
        config.eager_load = true
        config.logger = Logger.new(nil)
        config.secret_key_base = "x"
        config.eager_load_paths.clear
      end

      def app.name = "NdbRailtieTestApp"
      app.initialize!

      assert_equal TestDaemon.socket, NdbRecord.configuration.socket
      assert_equal 3, NdbRecord.configuration.timeout
      assert(client.collections.any? { |c| c["name"] == klass.collection_name })
    end
  end
end
