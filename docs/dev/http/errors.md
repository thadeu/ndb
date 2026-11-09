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
| `out_of_window` | 400 | A restore to an instant out of the PITR window; the message gives the start of the window. |
| `pitr_off` | 400 | A restore of a collection with `pitr: off`. |
| `lookup_invalid` | 400 | A [lookup](../query.md#lookup) or `$lookup` that is not valid (a relation without a kind or a foreign key, an unknown key, more than two levels, a `$lookup` before another stage). |
| `lookup_unindexed` | 400 | The other side of a lookup has no index on the field it searches. The message gives the `_indexes` route. |
| `confirm_required` | 400 | `_truncate` or drop without `confirm` equal to the collection name. |
| `catalog_invalid` | 400 | The collection definition or name is not valid (for example, it has `id`). |
| `ttl_not_enabled` | 400 | `expire_in`, `_expire` or `_persist` on a collection without `expires_field`. |
| `ttl_derived` | 400 | The same on a collection whose expiry comes from `ttl` + `time`. |
| `not_supported` | 400 | The route does not exist for a direct-mode collection. |
| `invalid_doc`, `missing_id`, `invalid_id`, `immutable_id`, `invalid_batch`, `invalid_path`, `invalid_index` | 400 | A document or a definition is not valid. |
| `invalid_blob_ref` | 400 | The blob reference is not `sha256:<64 hex>`. |
| `invalid_position` | 400 | The watch position is not valid. |
| `unauthorized` | 401 | A TCP request without a known bearer token (`-token-file` or `tokens:`). |
| `forbidden` | 403 | A scoped token without the right for this route and collection; `POST /_backup` over TCP. |
| `not_found` | 404 | No document (or blob) with this id. |
| `unknown_collection` | 404 | The collection does not exist. |
| `collection_exists` | 409 | A rename to a name that exists (without `replace`), or a restore `to` a name that exists. |
| `unknown_route` | 404 | No route has this path. |
| `method_not_allowed` | 405 | The path exists with other methods (see `Allow`). |
| `duplicate_id` | 409 | A document with this `_id` exists. |
| `unique_violation` | 409 | A `unique` field value exists. |
| `catalog_conflict` | 409 | Two declarations disagree (`write`, `path`, `time`). |
| `direct_collection` | 409 | A write on a direct-mode collection. |
| `history_truncated` | 410 | A `_watch` needs WAL files that `wal_retention` deleted. |
| `request_too_large`, `line_too_large` | 413 | The body or a line is over `max_line_size` (or the blob limit). |
| `lookup_too_large` | 422 | A lookup attaches more than 10 000 documents to one answer. |
| `group_limit_exceeded` | 422 | The group table of an aggregate is over its memory limit. |
| `canceled` | 499 | The client left before the answer (logs and metrics only). |
| `internal` | 500 | A bug or a disk error. The message stays in the daemon log. |
| `not_ready` | 503 | The daemon is recovering. Retry. |
| `closed`, `stopping` | 503 | The daemon is shutting down. Retry on another daemon or later. |
| `watch_lagged` | (stream) | The consumer was too slow. Reconnect: it resumes after the last ack. |
