# frozen_string_literal: true

module Ndb
  # ActiveRecord-like names for two daemon errors. They are the same classes,
  # so a rescue of either name catches the same error.
  StaleObjectError = VersionConflict
  RecordNotUnique = UniqueViolation

  # Raised by save!/create!/update! when validations fail.
  class RecordInvalid < Error
    attr_reader :record

    def initialize(record)
      @record = record

      super("Validation failed: #{record.errors.full_messages.join(', ')}")
    end
  end
end
