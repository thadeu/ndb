# frozen_string_literal: true

require "rails/railtie"

module Ndb
  module Rails
    # Reads config/ndb.yml and declares the collection of every model on
    # boot when the app eager loads (production). In development a model
    # declares its collection on first use. It also loads app/streams and
    # makes the groups of the stream consumers, in a background thread.
    class Railtie < ::Rails::Railtie
      initializer "ndb.configure" do |app|
        path = app.root.join("config", "ndb.yml")

        if path.exist?
          Ndb::Rails.configuration.load_file(path, ::Rails.env)
          Ndb::Rails.configure { |_| }
        end
      end

      config.after_initialize do |app|
        if app.config.eager_load
          Ndb::Record.descendants.reject(&:abstract_class?).each(&:ensure_collection!)
        end

        Ndb::Streams::Boot.start(app)
      end
    end
  end
end
