# frozen_string_literal: true

require "erb"
require "yaml"

module NdbRecord
  # The connection only: the address (url or socket), the token and the
  # timeouts. Unset address and token fall back to NDB_URL and NDB_TOKEN.
  class Configuration
    attr_accessor :url, :socket, :token, :timeout, :pool_size

    def initialize
      @timeout = 30
      @pool_size = 16
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

      self
    end
  end
end
