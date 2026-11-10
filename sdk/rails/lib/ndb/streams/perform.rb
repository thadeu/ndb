# frozen_string_literal: true

module Ndb
  module Streams
    # Runs `perform` of a consumer class on one event.
    module Perform
      class << self
        # A new instance for each event, in the Rails reloader when there
        # is an app (so a changed class is loaded again in development),
        # with the around_perform blocks around it. The options come from
        # the class of now (Streams.current). An error of `discard_on` is
        # logged and the call returns :discarded, so the entry is acked.
        # Any other error goes to the caller.
        def call(klass, event)
          run(klass, event).first
        end

        # perform_now: the same path as a delivery, and the value that
        # perform returns (nil when discarded).
        def now(klass, event)
          run(klass, event).last
        end

        # Inline mode: gives the new entries to each consumer of the stream.
        # The first call loads app/streams, so a consumer is there also
        # before the boot thread loads it.
        def inline(stream_name, ids, docs)
          Streams.load_classes_once!

          consumers = Streams.consumers_of(stream_name)

          return if consumers.empty?

          ids.zip(docs).each do |id, doc|
            consumers.each do |klass|
              event = Event.new(id: id, payload: JSON.parse(JSON.generate(doc)), deliveries: 1)

              call(klass, event)
            end
          end
        end

        private

        # Returns [:done, value of perform] or [:discarded, nil].
        def run(klass, event)
          wrap do
            klass = Streams.current(klass)
            options = klass.stream_options
            payload = { stream: klass.stream_name, group: options[:group], class: klass, event: event }

            begin
              value = Streams.instrument("perform.ndb_streams", payload) { chain(klass, event).call }

              [:done, value]
            rescue *options[:discard_on] => e
              Streams.logger.warn("ndb streams: #{klass.stream_label} discarded #{event.id}: #{e.class}: #{e.message}")

              [:discarded, nil]
            end
          end
        end

        def wrap(&block)
          app = Streams.rails_app

          return app.reloader.wrap(&block) if app.respond_to?(:reloader)
          return app.executor.wrap(&block) if app.respond_to?(:executor)

          block.call
        end

        def chain(klass, event)
          instance = klass.new
          perform = -> { instance.send(:perform, event) }

          klass.around_performs.reverse.inject(perform) do |inner, hook|
            -> { instance.instance_exec(event, inner, &hook) }
          end
        end
      end
    end
  end
end
