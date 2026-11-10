# frozen_string_literal: true

module Ndb
  module Streams
    # The work at the boot of an app: load app/streams and make the missing
    # groups, in a background thread, so the boot never waits for ndb.
    module Boot
      ATTEMPTS = 5

      class << self
        # Returns the thread. backoff: seconds before the second attempt;
        # it doubles after each one.
        def start(app = Streams.rails_app, attempts: ATTEMPTS, backoff: 1)
          Thread.new do
            Thread.current.name = "ndb-streams-boot"

            run(app, attempts: attempts, backoff: backoff)
          end
        end

        # True when the groups are there. A failure is a warning, not an
        # error: the runner makes the groups again when it starts.
        def run(app, attempts: ATTEMPTS, backoff: 1)
          load_classes(app)

          attempt = 1

          begin
            Streams.ensure_groups!

            true
          rescue Ndb::Error => e
            if attempt < attempts
              sleep(backoff * (2**(attempt - 1)))

              attempt += 1

              retry
            end

            Streams.logger.warn("ndb streams: the groups were not made at boot (#{e.class}: #{e.message})")

            false
          end
        rescue StandardError, ScriptError => e
          Streams.logger.warn("ndb streams: the boot failed (#{e.class}: #{e.message})")

          false
        end

        private

        def load_classes(app)
          return unless app

          return Streams.load_app!(app) unless app.respond_to?(:executor)

          app.executor.wrap { Streams.load_app!(app) }
        end
      end
    end
  end
end
