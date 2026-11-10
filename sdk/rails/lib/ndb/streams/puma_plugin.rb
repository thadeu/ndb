# frozen_string_literal: true

module Ndb
  module Streams
    # The logic of `plugin :ndb`, so that it can be tested without Puma.
    # lib/puma/plugin/ndb.rb connects it to Puma.
    module PumaPlugin
      ON = %w[1 true yes on].freeze
      OFF = %w[0 false no off].freeze

      # Seconds the consumers get to end their current entries at a stop.
      STOP_TIMEOUT = 30

      class << self
        # NDB_STREAMS_IN_PUMA turns the plugin on or off. Without it, the
        # plugin runs only in development.
        def enabled?(env = ENV)
          value = env["NDB_STREAMS_IN_PUMA"].to_s.strip.downcase

          return environment(env) == "development" if value.empty?
          return true if ON.include?(value)
          return false if OFF.include?(value)

          raise ArgumentError, "NDB_STREAMS_IN_PUMA must be true or false, not #{env['NDB_STREAMS_IN_PUMA']}"
        end

        def environment(env = ENV)
          %w[RAILS_ENV RACK_ENV APP_ENV].each do |key|
            value = env[key].to_s.strip

            return value unless value.empty?
          end

          "development"
        end

        # The binstub of the app when it has one, or the command of the gem
        # with Bundler.
        def command(root = Dir.pwd)
          binstub = File.join(root, "bin", "ndb")

          return [Gem.ruby, binstub, "streams"] if File.exist?(binstub)

          %w[bundle exec ndb streams]
        end
      end
    end
  end
end
