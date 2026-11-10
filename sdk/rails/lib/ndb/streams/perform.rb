# frozen_string_literal: true

module Ndb
  module Streams
    # Runs `perform` of a consumer class on one event.
    module Perform
      class << self
        # A new instance for each event, in the Rails executor when there
        # is an app, with the around_perform blocks around it. An error of
        # `discard_on` is logged and the call returns :discarded, so the
        # entry is acked. Any other error goes to the caller.
        def call(klass, event)
          options = klass.stream_options

          wrap do
            klass = Streams.current(klass)
            payload = { stream: klass.stream_name, group: options[:group], class: klass, event: event }

            Streams.instrument("perform.ndb_streams", payload) { chain(klass, event).call }
          end

          :done
        rescue *options[:discard_on] => e
          Streams.logger.warn("ndb streams: #{klass.stream_label} discarded #{event.id}: #{e.class}: #{e.message}")

          :discarded
        end

        # Inline mode: gives the new entries to each consumer of the stream.
        def inline(stream_name, ids, docs)
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

        def wrap(&block)
          app = Streams.rails_app

          return block.call unless app.respond_to?(:executor)

          app.executor.wrap(&block)
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
