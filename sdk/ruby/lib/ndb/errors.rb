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

  # with_lock lost the race on every try, or a model save or destroy matched
  # nothing: the document changed or was deleted since it was read (its "_v"
  # guard did not match).
  class StaleObjectError < Error; end
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

  # The daemon refused a token for this route, or a route over TCP.
  class Forbidden < Error; end

  # The IP failed the token check too many times, or max_watches streams are
  # open (HTTP 429). Wait for the Retry-After seconds.
  class TooManyRequests < Error; end

  # A document, an id, a batch, a path or an index definition is not valid.
  class InvalidDocument < Error; end

  # A collection pattern that is not valid, a write on one, or one that
  # matches too many collections.
  class InvalidPattern < Error; end

  # The route does not exist for a direct-mode collection.
  class NotSupported < Error; end

  # The blob reference is not sha256:<64 hex>.
  class InvalidBlobRef < Error; end

  # No route has this path, or not with this method.
  class UnknownRoute < Error; end

  # The group table of an aggregate is over its memory limit.
  class GroupLimitExceeded < Error; end

  # The body or one line is over max_line_size (or the blob limit).
  class RequestTooLarge < Error; end

  # The watch position is not valid.
  class InvalidPosition < Error; end

  # The daemon is shutting down (HTTP 503). Not retried by the client: the
  # request may have been taken, unlike NotReady.
  class Closed < Error; end

  # A bug or a disk error in the daemon; the message is in its log.
  class Internal < Error; end

  # A stream route on a collection that is not a stream, a bad entry ID, or
  # an append ID not greater than the last one.
  class StreamError < Error; end
  class NotAStream < StreamError; end

  # A write route of every collection on a stream: add entries with append,
  # delete them one by one or with a trim.
  class StreamAppendOnly < StreamError; end
  class InvalidStreamID < StreamError; end
  class StreamIDTooSmall < StreamError; end

  # The stream group does not exist, or a create names one that does.
  class UnknownGroup < StreamError; end
  class GroupExists < StreamError; end

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
    "lookup_too_large" => LookupError,
    "invalid_pipeline" => InvalidQuery,
    "forbidden" => Forbidden,
    "too_many_requests" => TooManyRequests,
    "invalid_doc" => InvalidDocument,
    "missing_id" => InvalidDocument,
    "invalid_id" => InvalidDocument,
    "immutable_id" => InvalidDocument,
    "invalid_batch" => InvalidDocument,
    "invalid_path" => InvalidDocument,
    "invalid_index" => InvalidDocument,
    "invalid_pattern" => InvalidPattern,
    "pattern_read_only" => InvalidPattern,
    "pattern_too_wide" => InvalidPattern,
    "not_supported" => NotSupported,
    "invalid_blob_ref" => InvalidBlobRef,
    "invalid_range" => InvalidBlobRef,
    "unknown_route" => UnknownRoute,
    "method_not_allowed" => UnknownRoute,
    "group_limit_exceeded" => GroupLimitExceeded,
    "request_too_large" => RequestTooLarge,
    "line_too_large" => RequestTooLarge,
    "invalid_position" => InvalidPosition,
    "closed" => Closed,
    "stopping" => Closed,
    "internal" => Internal,
    "not_a_stream" => NotAStream,
    "stream_append_only" => StreamAppendOnly,
    "invalid_stream_id" => InvalidStreamID,
    "stream_id_too_small" => StreamIDTooSmall,
    "unknown_group" => UnknownGroup,
    "group_exists" => GroupExists
  }.freeze

  def self.error_for(code, message, status)
    CODE_TO_ERROR.fetch(code, Error).new("#{code}: #{message}", code: code, status: status)
  end
end
