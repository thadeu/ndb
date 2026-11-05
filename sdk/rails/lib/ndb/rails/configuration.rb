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

      def initialize
        @timeout = 30
        @pool_size = 16
        @ready_timeout = 10
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

        self
      end
    end
  end
end
