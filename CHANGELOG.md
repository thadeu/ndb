# Changelog

All notable changes to this project are documented in this file.

The format follows [Keep a Changelog](https://keepachangelog.com/en/1.1.0/), and this project follows [Semantic Versioning](https://semver.org/).

## [Unreleased]

## [0.1.2] - 2026-11-06

### Changed

- **Breaking: atomic update by filter.** `POST /v1/c/{coll}/update` takes one object that mixes a filter and write operators (`$set`, `$unset`, `$inc`, `$push`, `$pull`, `$min`, `$max`; `$and`, `$or`, `$nor` and plain keys are the filter). Options are in the query string: `many=true`, `upsert=true`, `expire_in`. The filter is matched and the write is made inside the single writer of the collection, against the newest state, so concurrent updates never interleave. The answer is `{"matched","modified","doc"}` (no `doc` with `many`); nothing matched is `{"matched":0,"modified":0}` with HTTP 200. `modified` is 0, and no version is written, when the operators leave the document as it is.
- **Breaking:** `POST /v1/c/{coll}/delete` takes a filter as the body, with `many=true`, and answers `{"deleted":N}`.
- **Breaking:** the old update shape (`{"update":{"id","set","unset","if_version"}}`), the `if_version` field of `delete` and `expire`, and the `version_conflict` error are removed. Optimistic concurrency is a filter: `{"id":"x","_v":7,"$set":{...}}` matches nothing when the version moved.
- `many` needs the id or a ready index in the filter (`400 unindexed_many`). `upsert` creates the document from the equality fields of the filter, then applies the operators. New contract errors (HTTP 400): `unknown_operator`, `no_update`, `conflicting_update`, `invalid_field`, `empty_filter`.
- SDKs: Go `col.Update(ctx, filter, ops, ndb.Many(), ndb.Upsert(), ndb.ExpireIn(d))` and `col.Delete(ctx, filter, opts...)`; Ruby `client.update(coll, filter, ops, many:, upsert:, expire_in:)` and `client.delete(coll, filter, many:)`; TypeScript `update(filter, ops, { many, upsert, expireIn })` and `delete(filter, { many })`. Every SDK refuses an operator key without `$` before sending. `IfVersion`, `Unset`, `ifVersion` and `if_version:` are removed.
- ndb-rails: `save`, `update` and `destroy` filter by the id and `_v` and raise `Ndb::StaleObjectError` when nothing matched (it is now its own class, code `stale_object`).

### Added

- `$nor` in `where` and in update filters.
- CLI `ndb update <collection> '<json>' [--many] [--upsert] [--expire-in 60s]` and `ndb delete <collection> '<json>' [--many]`.
- Docs: dedicated Query, Aggregate and Watch pages with SDK tabs; the error table moved to the HTTP API page.
- ndb-rails: `record.increment!(attr, by = 1)`, `record.decrement!(attr, by = 1)`, `Model.where(...).update(ops)` and `Model.where(...).delete`.

## [0.1.1] - 2026-11-05

### Added

- Per-document expiry like Redis: `expire_in` (a duration string or seconds) on `insert` and `update`, `POST /v1/c/{coll}/expire` (`in` or `at`, `if_version`), `POST /v1/c/{coll}/persist` and `GET /v1/c/{coll}/ttl/{id}` (`-1` = never expires). They need `expires_field` (`400 ttl_not_enabled`; `400 ttl_derived` with only `ttl` + `time`), are normal writes (new version, WAL, `watch` event), and `expire_in` wins over a value of the field in the same call. See "TTL like Redis" and "Freeing disk" in `docs/dev/configuration.md`: disk from expired documents returns after compaction, and fully only with `wal_retention`.
- CLI `ndb expire <collection> <id> <duration>`, `ndb persist <collection> <id>` and `ndb ttl <collection> <id>`.
- SDKs: Go `ndb.ExpireIn`, `InsertWith`, `Expire`, `ExpireAt`, `Persist`, `TTL`; Ruby `insert/update expire_in:`, `expire`, `persist`, `ttl`; ndb-rails `create(attrs, expire_in:)`, `record.expire`, `record.persist_ttl!`, `record.ttl`; TypeScript `expireIn`, `expire`, `expireAt`, `persist`, `ttl` and the schema `expires` option.

- `install.sh` and `uninstall.sh`, served at `https://ndb.clowk.in`: `curl -fsSL https://ndb.clowk.in/install.sh | bash` installs the release binary after a sha256 check; `--systemd` also sets up the `ndb` user, `/var/lib/ndb`, `/etc/ndb/ndb.yml` and `ndb.service`.
- HTTP API for operators and a future web console: `GET /v1/stats` (every collection with rows, write mode, indexes, quarantine, segment and WAL file counts, bytes per part, totals, blobs and cursors bytes, version, uptime), `GET /v1/size`, `GET /v1/c/{coll}` (the catalog definition), `POST /v1/c/{coll}/compact` (engine mode; direct mode is `400 not_supported`) and `GET /v1/c/{coll}/quarantine?limit=N`. `GET /v1/c/{coll}/stats` keeps its fields and adds `segments`, `wal_files`, `bytes` and `total`.
- CLI commands `ndb collections [name]`, `ndb compact <collection>`, `ndb quarantine <collection>` and `ndb query <collection> ['<where>']` (`-sort`, `-fields`, `-limit`, `-aggregate`), all with `-json`, `-url`, `-config` and `-data-dir`. `ndb stats` and `ndb size` now call the API first and read the data dir only when no daemon answers.
- Tables fit the terminal: box-drawing borders (ASCII when the locale is not UTF-8 or `TERM=dumb`), bold header, right-aligned numbers, human sizes, thousands separators, cells cut with `…` to the terminal width (`COLUMNS` fallback), color only on a terminal without `NO_COLOR`.
- CLI in the same binary: `ndb restore` (refuses a locked or non-empty data dir without `-force`, and checks the backup by recovering it first), `ndb stats` (daemon HTTP API or data dir, `-json`), `ndb size` (disk usage per collection and part, `-json`), `ndb version`, `ndb help`, and `ndb serve` as an explicit alias of the daemon. Running `ndb` with only flags still starts the daemon.

## [0.1.0] - 2026-11-05

First release.

### Added

- `$regex` and `$options` (`i`, `m`, `s`, `x`) in `where`: RE2 syntax, works on array fields, dot paths and inside `$not`, `$and`, `$or`. A literal `^prefix` without `i` or `m` uses the index and the zone maps.
- `$regex` support in the Go and TypeScript SDKs.
- Ruby gems: a `Regexp` is a valid `where` value (`ndb`, and `where`, `where.not`, `or` in `ndb-rails`). It always sends `m` (Ruby line anchors), maps `/i` to `i`, `/m` to `s`, `/x` to `x`, and rejects look-around, backreferences, atomic groups, possessive quantifiers, `\h`, `\G`, `\R` and `\X` with `Ndb::InvalidQuery` before the request.
- `ndb` daemon: one static binary, one data directory, HTTP API over a unix socket or TCP.
- Durable WAL writes, columnar segments, and a column-scan planner with pruning.
- Two write modes: direct (apps append NDJSON files, ndb follows them) and engine (`insert`, `update`, `delete`, optimistic `if_version`).
- NoSQL `find` with comparison and logical operators, sort, offset and cursor pagination.
- `aggregate` with `$match $group $sort $limit $skip $project` and `$dateTrunc`.
- `watch` and `ack` for outbox-style consumers, with `where` filters and WAL truncation (`wal_retention`).
- Secondary and unique indexes built in the background.
- TTL per document, retention, compaction, and a blob store for large values.
- Quarantine for bad lines in direct mode, online backup, Prometheus `/metrics`.
- Optional bearer token on the TCP listener (`-token-file`).
- Go SDK (`github.com/thadeu/ndb/sdk/go`, tag `sdk/go/v0.1.0`), Ruby SDK in two gems (`ndb`, a plain client with no runtime dependency, and `ndb-rails`, the ActiveModel layer that depends on it) and TypeScript SDK (`@thadeu/ndb`), all MIT.
- Release tooling: static binaries for Linux and macOS, multi-arch image at `ghcr.io/thadeu/ndb`.
- Docs in `docs/dev/`, a docs site, and deploy examples (single VM, cross-VM with WireGuard).

[Unreleased]: https://github.com/thadeu/ndb/compare/v0.1.2...HEAD
[0.1.2]: https://github.com/thadeu/ndb/compare/v0.1.1...v0.1.2
[0.1.1]: https://github.com/thadeu/ndb/compare/v0.1.0...v0.1.1
[0.1.0]: https://github.com/thadeu/ndb/releases/tag/v0.1.0
