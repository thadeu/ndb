# frozen_string_literal: true

module Ndb
  # ActiveRecord-like name for a daemon error. It is the same class, so a
  # rescue of either name catches the same error.
  RecordNotUnique = UniqueViolation

  # A save or destroy matched nothing: the record changed or was deleted
  # since it was loaded (its "_v" guard did not match).
  class StaleObjectError < Error; end

  # Raised by save!/create!/update! when validations fail.
  class RecordInvalid < Error
    attr_reader :record

    def initialize(record)
      @record = record

      super("Validation failed: #{record.errors.full_messages.join(', ')}")
    end
  end
end
