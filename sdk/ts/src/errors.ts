export type ErrorCode =
  | 'unauthorized'
  | 'not_found'
  | 'version_conflict'
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
export class VersionConflictError extends NdbError {}
export class DuplicateIdError extends NdbError {}
export class UniqueViolationError extends NdbError {}
export class InvalidQueryError extends NdbError {}
export class CursorError extends NdbError {}
export class WatchLaggedError extends NdbError {}
export class HistoryTruncatedError extends NdbError {}

// The collection has no expires field (ttl_not_enabled), or its expiry comes
// from ttl + time (ttl_derived).
export class TtlError extends NdbError {}

const subclasses: Record<string, typeof NdbError> = {
  unauthorized: UnauthorizedError,
  not_found: NotFoundError,
  unknown_collection: NotFoundError,
  version_conflict: VersionConflictError,
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
}

export function makeError(code: ErrorCode, message: string, status = 0): NdbError {
  const Cls = subclasses[code] ?? NdbError

  return new Cls(code, message, status)
}
