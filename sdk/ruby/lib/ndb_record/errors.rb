# frozen_string_literal: true

module NdbRecord
  # An error the daemon answered (code + HTTP status), or a transport failure.
  class Error < StandardError
    attr_reader :code, :status

    def initialize(message = nil, code: nil, status: nil)
      super(message)

      @code = code
      @status = status
    end
  end

  class ConnectionError < Error; end
  class TimeoutError < ConnectionError; end

  class NotFound < Error; end
  class UnknownCollection < Error; end
  class StaleObjectError < Error; end
  class RecordNotUnique < Error; end
  class CatalogConflict < Error; end
  class CatalogInvalid < Error; end
  class ReadOnlyCollection < Error; end
  class InvalidQuery < Error; end
  class InvalidCursor < Error; end

  class RecordInvalid < Error
    attr_reader :record

    def initialize(record)
      @record = record

      super("Validation failed: #{record.errors.full_messages.join(', ')}")
    end
  end

  # The daemon error code -> the Ruby class.
  CODE_TO_ERROR = {
    "not_found" => NotFound,
    "unknown_collection" => UnknownCollection,
    "version_conflict" => StaleObjectError,
    "unique_violation" => RecordNotUnique,
    "duplicate_id" => RecordNotUnique,
    "catalog_conflict" => CatalogConflict,
    "catalog_invalid" => CatalogInvalid,
    "direct_collection" => ReadOnlyCollection,
    "invalid_query" => InvalidQuery,
    "invalid_request" => InvalidQuery,
    "cursor_invalid" => InvalidCursor,
    "cursor_mismatch" => InvalidCursor,
    "cursor_expired" => InvalidCursor
  }.freeze

  def self.error_for(code, message, status)
    CODE_TO_ERROR.fetch(code, Error).new("#{code}: #{message}", code: code, status: status)
  end
end
