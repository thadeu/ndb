# frozen_string_literal: true

require "erb"
require "yaml"

module NdbRecord
  # The connection only: the socket path and the timeouts.
  class Configuration
    attr_accessor :socket, :timeout, :pool_size

    def initialize
      @socket = "/var/run/ndb/ndb.sock"
      @timeout = 30
      @pool_size = 16
    end

    # Reads config/ndb.yml: one block per environment.
    def load_file(path, env)
      raw = YAML.safe_load(ERB.new(File.read(path)).result, aliases: true) || {}
      settings = raw.fetch(env.to_s, {})

      @socket = settings["socket"] if settings.key?("socket")
      @timeout = settings["timeout"] if settings.key?("timeout")
      @pool_size = settings["pool_size"] if settings.key?("pool_size")

      self
    end
  end
end
