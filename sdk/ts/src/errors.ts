export type ErrorCode =
  | 'unauthorized'
  | 'not_found'
  | 'duplicate_id'
  | 'unique_violation'
  | 'catalog_conflict'
  | 'catalog_invalid'
  | 'unknown_collection'
  | 'direct_collection'
  | 'invalid_request'
  | 'invalid_query'
  | 'invalid_pipeline'
  | 'group_limit_exceeded'
  | 'cursor_invalid'
  | 'cursor_mismatch'
  | 'cursor_expired'
  | 'request_too_large'
  | 'line_too_large'
  | 'not_ready'
  | 'closed'
  | 'watch_lagged'
  | 'invalid_position'
  | 'history_truncated'
  | 'ttl_not_enabled'
  | 'ttl_derived'
  | 'unknown_operator'
  | 'no_update'
  | 'conflicting_update'
  | 'invalid_field'
  | 'unindexed_many'
  | 'empty_filter'
  | 'confirm_required'
  | 'collection_exists'
  | 'out_of_window'
  | 'pitr_off'
  | 'lookup_invalid'
  | 'lookup_unindexed'
  | 'lookup_too_large'
  | 'invalid_doc'
  | 'missing_id'
  | 'invalid_id'
  | 'immutable_id'
  | 'invalid_batch'
  | 'invalid_path'
  | 'invalid_index'
  | 'invalid_blob_ref'
  | 'invalid_range'
  | 'invalid_pattern'
  | 'pattern_read_only'
  | 'pattern_too_wide'
  | 'not_supported'
  | 'forbidden'
  | 'too_many_requests'
  | 'unknown_route'
  | 'method_not_allowed'
  | 'stopping'
  | 'internal'
  | 'not_a_stream'
  | 'stream_append_only'
  | 'invalid_stream_id'
  | 'stream_id_too_small'
  | 'unknown_group'
  | 'group_exists'
  | 'invalid_update'
  | 'stale_object'
  | 'http_status'
  | (string & {})

export class NdbError extends Error {
  readonly code: ErrorCode
  readonly status: number

  constructor(code: ErrorCode, message: string, status = 0) {
    super(message ? `ndb: ${code}: ${message}` : `ndb: ${code}`)
    this.name = new.target.name
    this.code = code
    this.status = status
  }
}

export class UnauthorizedError extends NdbError {}
export class NotFoundError extends NdbError {}
export class DuplicateIdError extends NdbError {}
export class UniqueViolationError extends NdbError {}
export class InvalidQueryError extends NdbError {}
export class CursorError extends NdbError {}
export class WatchLaggedError extends NdbError {}
export class HistoryTruncatedError extends NdbError {}

// The update or delete body breaks the contract: an unknown operator, no write
// operator, two operators on one field, a field that starts with "$", many
// without an index, or an empty filter. The SDK also throws it, with the code
// invalid_update, when a key of the operators does not start with "$".
export class InvalidUpdateError extends NdbError {}

// The collection has no expires field (ttl_not_enabled), or its expiry comes
// from ttl + time (ttl_derived).
export class TtlError extends NdbError {}

// rename onto a name that exists, without replace.
export class CollectionExistsError extends NdbError {}

// restore to an instant out of the PITR window (out_of_window), or of a
// collection with pitr: off (pitr_off).
export class PitrError extends NdbError {}

// A lookup that is not valid (lookup_invalid), whose other side has no index
// (lookup_unindexed), or that attaches too many documents (lookup_too_large).
export class LookupError extends NdbError {}

// A stream route on a collection that is not a stream (not_a_stream).
export class NotAStreamError extends NdbError {}

// A write route of every collection on a stream (stream_append_only): add
// entries with append, delete them one by one or with a trim.
export class StreamAppendOnlyError extends NdbError {}

// A bad entry ID (invalid_stream_id), or an append ID that is not greater
// than the last one (stream_id_too_small).
export class StreamIdError extends NdbError {}

// The group does not exist (unknown_group).
export class UnknownGroupError extends NdbError {}

// A group create with a name that exists (group_exists).
export class GroupExistsError extends NdbError {}

// withLock lost the race on every try: the document changed between the read
// and the write each time.
export class StaleObjectError extends NdbError {}

const subclasses: Record<string, typeof NdbError> = {
  unauthorized: UnauthorizedError,
  not_found: NotFoundError,
  unknown_collection: NotFoundError,
  duplicate_id: DuplicateIdError,
  unique_violation: UniqueViolationError,
  invalid_query: InvalidQueryError,
  invalid_pipeline: InvalidQueryError,
  cursor_invalid: CursorError,
  cursor_mismatch: CursorError,
  cursor_expired: CursorError,
  watch_lagged: WatchLaggedError,
  history_truncated: HistoryTruncatedError,
  ttl_not_enabled: TtlError,
  ttl_derived: TtlError,
  unknown_operator: InvalidUpdateError,
  no_update: InvalidUpdateError,
  conflicting_update: InvalidUpdateError,
  invalid_field: InvalidUpdateError,
  unindexed_many: InvalidUpdateError,
  empty_filter: InvalidUpdateError,
  collection_exists: CollectionExistsError,
  out_of_window: PitrError,
  pitr_off: PitrError,
  lookup_invalid: LookupError,
  lookup_unindexed: LookupError,
  lookup_too_large: LookupError,
  not_a_stream: NotAStreamError,
  stream_append_only: StreamAppendOnlyError,
  invalid_stream_id: StreamIdError,
  stream_id_too_small: StreamIdError,
  unknown_group: UnknownGroupError,
  group_exists: GroupExistsError,
}

export function makeError(code: ErrorCode, message: string, status = 0): NdbError {
  const Cls = subclasses[code] ?? NdbError

  return new Cls(code, message, status)
}
