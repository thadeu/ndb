# frozen_string_literal: true

module Ndb
  module Streams
    # A class with a wrong stream config.
    class ConfigurationError < ArgumentError; end

    # A class has `perform` but its `stream` has no `group:`.
    class MissingGroup < ConfigurationError; end

    # Two consumer classes use the same group on one stream.
    class DuplicateGroup < ConfigurationError; end
  end
end
