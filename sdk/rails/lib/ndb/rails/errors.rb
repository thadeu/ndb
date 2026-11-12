# frozen_string_literal: true

module Ndb
  # ActiveRecord-like name for a daemon error. It is the same class, so a
  # rescue of either name catches the same error.
  RecordNotUnique = UniqueViolation

  # Raised by destroy! when a before_destroy callback aborts.
  class RecordNotDestroyed < Error
    attr_reader :record

    def initialize(message = "Failed to destroy the record", record = nil)
      @record = record

      super(message)
    end
  end

  # Raised by save, destroy and delete of a record whose readonly? is true.
  class ReadOnlyRecord < Error; end

  # Raised by save!/create!/update! when validations fail.
  class RecordInvalid < Error
    attr_reader :record

    def initialize(record)
      @record = record

      super("Validation failed: #{record.errors.full_messages.join(', ')}")
    end
  end
end
