# frozen_string_literal: true

require "active_model"
require "active_support/all"

require "ndb_record/version"
require "ndb_record/errors"
require "ndb_record/configuration"
require "ndb_record/connection"
require "ndb_record/client"
require "ndb_record/conditions"
require "ndb_record/relation"
require "ndb_record/blob"
require "ndb_record/persistence"
require "ndb_record/base"

module NdbRecord
  class << self
    def configuration
      @configuration ||= Configuration.new
    end

    def configure
      yield configuration

      @client = nil
    end

    def client
      @client ||= Client.new(
        socket: configuration.socket,
        timeout: configuration.timeout,
        pool_size: configuration.pool_size
      )
    end
  end
end

require "ndb_record/railtie" if defined?(Rails::Railtie)
