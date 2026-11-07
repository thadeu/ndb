# Errors

Every error has the same body, and a stable `code` that clients branch on:

```json
{ "error": { "code": "unknown_collection", "message": "unknown_collection: calls" } }
```

- `code` is part of the contract: it changes only in a major release.
- `message` is for people. Do not parse it.
- The HTTP status follows the code (table below). A write that matches nothing is not an error: it is `200` with `"matched": 0`.
- A `_watch` stream that already started reports an error as a line `{"error":{"code","message"}}`, then ends.

The SDKs turn each code into a typed error: Go `ndb.ErrUnknownCollection` (`errors.Is`), Ruby `Ndb::UnknownCollection`, TypeScript `UnknownCollectionError`.

| Code | HTTP | Meaning |
|---|---|---|
| `invalid_request` | 400 | Bad body: not JSON, unknown key, empty `docs`, a bad option. |
| `invalid_query` | 400 | Bad `query`, `sort`, operator, date math or key. |
| `invalid_pipeline` | 400 | Bad aggregate pipeline. |
| `cursor_invalid` | 400 | The `after` token is broken. |
| `cursor_mismatch` | 400 | The token was made for another query, sort or pattern. |
| `cursor_expired` | 400 | The snapshot of the cursor expired. |
| `unknown_operator`, `no_update`, `conflicting_update`, `invalid_field`, `unindexed_many`, `empty_filter` | 400 | An update or delete breaks the [contract](../query.md#contract-errors). |
| `invalid_pattern` | 400 | A collection pattern that is not a prefix and one `*` at the end. |
| `pattern_read_only` | 400 | A write or admin route on a collection pattern. |
| `pattern_too_wide` | 400 | The pattern matches more than 256 collections. |
| `confirm_required` | 400 | `_truncate` or drop without `confirm` equal to the collection name. |
| `catalog_invalid` | 400 | The collection definition or name is not valid (for example, it has `id`). |
| `ttl_not_enabled` | 400 | `expire_in`, `_expire` or `_persist` on a collection without `expires_field`. |
| `ttl_derived` | 400 | The same on a collection whose expiry comes from `ttl` + `time`. |
| `not_supported` | 400 | The route does not exist for a direct-mode collection. |
| `invalid_doc`, `missing_id`, `invalid_id`, `immutable_id`, `invalid_batch`, `invalid_path`, `invalid_index` | 400 | A document or a definition is not valid. |
| `invalid_blob_ref` | 400 | The blob reference is not `sha256:<64 hex>`. |
| `invalid_position` | 400 | The watch position is not valid. |
| `unauthorized` | 401 | A TCP request without the right bearer token (`-token-file`). |
| `forbidden` | 403 | For example, `/_backup` over TCP. |
| `not_found` | 404 | No document (or blob) with this id. |
| `unknown_collection` | 404 | The collection does not exist. |
| `unknown_route` | 404 | No route has this path. |
| `method_not_allowed` | 405 | The path exists with other methods (see `Allow`). |
| `duplicate_id` | 409 | A document with this `_id` exists. |
| `unique_violation` | 409 | A `unique` field value exists. |
| `catalog_conflict` | 409 | Two declarations disagree (`write`, `path`, `time`). |
| `direct_collection` | 409 | A write on a direct-mode collection. |
| `history_truncated` | 410 | A `_watch` needs WAL files that `wal_retention` deleted. |
| `request_too_large`, `line_too_large` | 413 | The body or a line is over `max_line_size` (or the blob limit). |
| `group_limit_exceeded` | 422 | The group table of an aggregate is over its memory limit. |
| `canceled` | 499 | The client left before the answer (logs and metrics only). |
| `internal` | 500 | A bug or a disk error. The message stays in the daemon log. |
| `not_ready` | 503 | The daemon is recovering. Retry. |
| `closed` | 503 | The daemon is shutting down. |
| `watch_lagged` | (stream) | The consumer was too slow. Reconnect: it resumes after the last ack. |
