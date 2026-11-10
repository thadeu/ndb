# frozen_string_literal: true

require "erb"
require "yaml"

module Ndb
  module Rails
    # The connection only: the address (url or socket), the token and the
    # timeouts. ready_timeout is how many seconds a call retries while the
    # daemon starts or recovers (0 turns it off). Unset address and token fall back to NDB_URL and NDB_TOKEN.
    class Configuration
      attr_accessor :url, :socket, :token, :timeout, :pool_size, :ready_timeout

      # collection_prefix goes before every collection name of the models
      # ("test2_" for a parallel test worker). nil by default.
      attr_accessor :collection_prefix

      # The `streams:` block of config/ndb.yml, for `ndb streams`:
      # "concurrency" (the threads of each consumer class) and "only" (the
      # names of the classes to run).
      attr_accessor :streams

      def initialize
        @timeout = 30
        @pool_size = 16
        @ready_timeout = 10
        @streams = {}
      end

      # Reads config/ndb.yml: one block per environment.
      def load_file(path, env)
        raw = YAML.safe_load(ERB.new(File.read(path)).result, aliases: true) || {}
        settings = raw.fetch(env.to_s, {})

        @url = settings["url"] if settings.key?("url")
        @socket = settings["socket"] if settings.key?("socket")
        @token = settings["token"] if settings.key?("token")
        @timeout = settings["timeout"] if settings.key?("timeout")
        @pool_size = settings["pool_size"] if settings.key?("pool_size")
        @ready_timeout = settings["ready_timeout"] if settings.key?("ready_timeout")
        @streams = settings["streams"] if settings["streams"].is_a?(Hash)

        self
      end
    end
  end
end
