# frozen_string_literal: true

require "active_model"
require "active_support/all"
require "ndb"

require "ndb/rails/version"
require "ndb/rails/errors"
require "ndb/rails/configuration"
require "ndb/rails/relation"
require "ndb/rails/blob"
require "ndb/rails/persistence"
require "ndb/rails/record"

module Ndb
  module Rails
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
          url: configuration.url,
          socket: configuration.socket,
          token: configuration.token,
          timeout: configuration.timeout,
          pool_size: configuration.pool_size,
          ready_timeout: configuration.ready_timeout
        )
      end
    end
  end
end

require "ndb/rails/railtie" if defined?(::Rails::Railtie)
