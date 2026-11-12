# frozen_string_literal: true

require "active_model"
require "active_support/all"
require "ndb"

require "ndb/rails/version"
require "ndb/rails/errors"
require "ndb/rails/configuration"
require "ndb/rails/dynamic_finders"
require "ndb/rails/relation"
require "ndb/rails/validations"
require "ndb/rails/blob"
require "ndb/rails/persistence"
require "ndb/rails/locking"
require "ndb/rails/transactions"
require "ndb/rails/associations"
require "ndb/rails/record"
require "ndb/streams"

I18n.load_path << File.expand_path("rails/locale/en.yml", __dir__)

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

      # A new client on next use (after a configuration change); models
      # declare their collections again.
      def reset_client!
        @client = nil
      end

      def client
        @client ||= Client.new(
          url: configuration.url,
          socket: configuration.socket,
          token: configuration.token,
          timeout: configuration.timeout,
          pool_size: configuration.pool_size,
          ready_timeout: configuration.ready_timeout,
          user_agent: user_agent
        )
      end

      # The tokens that go after the User-Agent of the ndb gem: this gem,
      # the Rails version when Rails is loaded, then the token of the app
      # (config user_agent).
      def user_agent
        tokens = ["ndb-rails/#{VERSION}"]

        tokens << "rails/#{::Rails.version}" if defined?(::Rails) && ::Rails.respond_to?(:version)
        tokens << configuration.user_agent.to_s.strip

        tokens.reject(&:empty?).join(" ")
      end
    end
  end
end

require "ndb/rails/railtie" if defined?(::Rails::Railtie)
