# frozen_string_literal: true

module Ndb
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

  # The connect failed: the socket is missing or refuses. Nothing was sent.
  class ConnectError < ConnectionError; end

  # The daemon is up but still recovering (HTTP 503). Nothing was done.
  class NotReady < Error; end

  class Unauthorized < Error; end
  class NotFound < Error; end
  class UnknownCollection < Error; end
  class UniqueViolation < Error; end
  class CatalogConflict < Error; end
  class CatalogInvalid < Error; end
  class ReadOnlyCollection < Error; end
  class InvalidQuery < Error; end
  class InvalidCursor < Error; end

  # The update or delete body breaks the contract (see Client#update).
  class InvalidUpdate < Error; end
  class UnknownOperator < InvalidUpdate; end
  class NoUpdate < InvalidUpdate; end
  class ConflictingUpdate < InvalidUpdate; end
  class InvalidField < InvalidUpdate; end
  class UnindexedMany < InvalidUpdate; end
  class EmptyFilter < InvalidUpdate; end

  # The collection has no expires_field, or its expiry comes from ttl + time.
  class TtlNotEnabled < Error; end
  class TtlDerived < Error; end

  # The consumer was too slow: call watch again, it resumes from the last ack.
  class WatchLagged < Error; end

  # The WAL files that hold the history were deleted (wal_retention): the
  # consumer cannot resume from its position and must start over.
  class HistoryTruncated < Error; end

  # truncate or drop without the collection name as confirm.
  class ConfirmRequired < Error; end

  # rename onto a name that exists, without replace.
  class CollectionExists < Error; end

  # restore to an instant out of the PITR window of the collection.
  class OutOfWindow < Error; end

  # restore of a collection with pitr: off.
  class PitrOff < Error; end

  # A lookup that is not valid, whose other side has no index, or that
  # attaches too many documents.
  class LookupError < Error; end

  # The daemon error code -> the Ruby class.
  CODE_TO_ERROR = {
    "unauthorized" => Unauthorized,
    "not_ready" => NotReady,
    "not_found" => NotFound,
    "unknown_collection" => UnknownCollection,
    "unique_violation" => UniqueViolation,
    "duplicate_id" => UniqueViolation,
    "catalog_conflict" => CatalogConflict,
    "catalog_invalid" => CatalogInvalid,
    "direct_collection" => ReadOnlyCollection,
    "invalid_query" => InvalidQuery,
    "invalid_request" => InvalidQuery,
    "cursor_invalid" => InvalidCursor,
    "cursor_mismatch" => InvalidCursor,
    "cursor_expired" => InvalidCursor,
    "watch_lagged" => WatchLagged,
    "history_truncated" => HistoryTruncated,
    "ttl_not_enabled" => TtlNotEnabled,
    "ttl_derived" => TtlDerived,
    "unknown_operator" => UnknownOperator,
    "no_update" => NoUpdate,
    "conflicting_update" => ConflictingUpdate,
    "invalid_field" => InvalidField,
    "unindexed_many" => UnindexedMany,
    "empty_filter" => EmptyFilter,
    "confirm_required" => ConfirmRequired,
    "collection_exists" => CollectionExists,
    "out_of_window" => OutOfWindow,
    "pitr_off" => PitrOff,
    "lookup_invalid" => LookupError,
    "lookup_unindexed" => LookupError,
    "lookup_too_large" => LookupError
  }.freeze

  def self.error_for(code, message, status)
    CODE_TO_ERROR.fetch(code, Error).new("#{code}: #{message}", code: code, status: status)
  end
end
