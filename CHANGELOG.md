# Changelog

All notable changes to this project are documented in this file.

The format follows [Keep a Changelog](https://keepachangelog.com/en/1.1.0/), and this project follows [Semantic Versioning](https://semver.org/).

## [Unreleased]

## [0.4.0] - 2026-11-07

### Added

- Collection patterns: `POST /v1/c/logs-2026-*/find` and `/aggregate` read every collection whose name starts with `logs-2026-`, for collections split by time. Rows are merged in the `sort` order (then `_coll`, then `_id`); each row starts with `_coll`, the name of its collection. `count`, `skip`, `limit` and `after` work over the whole result; each collection uses its own indexes. `GET /v1/c/logs-*` lists the matched names. Writes and admin calls on a pattern are `400 pattern_read_only`. See [Collection patterns](docs/dev/query.md#collection-patterns).
- Error codes `invalid_pattern`, `pattern_read_only` and `pattern_too_wide` (more than 256 collections).
- Date math on the time field: `{"at": {"$gte": "now-7d/d"}}`, with `+N`/`-N` in `s m h d w M y` and rounding with `/unit` (UTC). It resolves to a time value, so indexes and zone maps work. A `find` cursor keeps the `now` of its first page. See [Date math](docs/dev/query.md#date-math).
- `$elemMatch`: one element of an array matches the whole inner filter (field keys for objects, operators for scalars). See [Array elements](docs/dev/query.md#array-elements).
- SDKs: Go `ndb.ElemMatch`; TypeScript `$elemMatch` in `Where`. Date math strings work in every SDK as is.
- SDKs, queries built elsewhere (a search form, a saved search), sent as is: Go `ndb.Raw(map)`, `ndb.RawJSON([]byte)` and `Coll.Find(ctx, body)` (one page: rows, next, total); TypeScript `whereRaw(obj)` and `model.find(body)`; Rails `where_raw(hash)` (no Array-to-`$in` convention) and `Model.search(body)` (records, next cursor, total).
- SDKs: Go `ndb.Pattern[T](db, "logs-*")` and the `ndb:"_coll"` field; Ruby `ndb.find("logs-*", ...)`; Rails `self.collection_name = "logs-*"` and `record.source_collection`; TypeScript `db.model('logs-*', schema)` and `row._coll`.

### Changed

- On the time field (and `expires_field`), a string operand that starts with `now` is now date math. A malformed one (`"nowish"`, `"now-7x"`) is `invalid_query`; before 0.4.0 it was compared as a plain string and matched nothing.

## [0.3.3] - 2026-11-07

### Added

- Truncate and drop of a collection, for collections split by time. `POST /v1/c/{coll}/truncate?confirm={coll}` removes every document and keeps the definition and indexes; `DELETE /v1/c/{coll}?confirm={coll}` also removes the definition and the watch consumers. Both take a few ms for any size: ndb swaps the collection dir and deletes the old one in the background. Calls in flight finish first and new calls wait; backup and blob GC never see half a swap. A crash during a truncate is finished or rolled back at the next start. See [Truncate and drop](docs/dev/operations.md#truncate-and-drop).
- Watch: a `truncate` event (no `_id`). Each consumer gets it one time after a truncate, then the new changes. Open streams of the collection end at a truncate or drop.
- CLI: `ndb truncate <coll>` and `ndb drop <coll>` ask you to type the name, or take `-yes`.
- SDKs: Go `Truncate`/`Drop` and `OpTruncate`, Ruby `truncate`/`drop` and `Ndb::ConfirmRequired`, Rails `Model.truncate!`/`Model.drop!`, TypeScript `truncate()`/`drop()` and the `'truncate'` op.
- Error code `confirm_required` (400).

### Changed

- A WAL that holds a truncate marker cannot be read by ndb 0.3.2 or older.

## [0.3.2] - 2026-11-07

### Fixed

- A `many` update or delete of more than about 25k documents failed with `line_too_large`, because the full write went into one WAL record. ndb now splits it into records of up to half of `max_line_size`, so a `many` write has no size limit. Each record is atomic, the write as a whole is not. See [Update by filter](docs/dev/query.md#which-documents).

### Changed

- A `many` delete is faster: when the index alone answers the filter (one indexed field, `$eq`, `$in` or a range), ndb deletes the matches without reading them. Candidates are visited in segment and row order, so each Parquet row group is decoded one time. On 1M sealed documents, a delete of 10k takes about 0.14 s (0.6 s before), 100k takes about 0.4 s, and 868k takes about 3 s (both failed before).

- Docs: the console has its own **UI** section (top menu and tab): an overview with a screenshot gallery, Install, Docker (TCP, unix socket, Compose), and a guide with one part per screen. The old `/console` page is gone.
- Docs site on phones: the logo stays on the left and the menu button moves to the right; the sidebar opens from the right.
- Console: a build version such as `dev` shows as is, not as `vdev`.

## [0.3.1] - 2026-11-06

### Added

- Console: a Discover row in the New connection form finds ndb daemons on this machine (unix sockets and `127.0.0.1`) or on another host, and fills the URL. See [UI guide](docs/dev/ui/guide.md#new-connection-and-discover).

### Changed

- Console: New connection opens in a right drawer from a "+ New connection" card. Name is optional (taken from the URL), and Discover sits next to the URL field.
- Console: fields turn off spelling marks and the 1Password, LastPass, Bitwarden, and Dashlane fill prompts.

## [0.3.0] - 2026-11-06

### Added

- Ndb Console (`ndb-ui`): a web console in `console/` (Bun, Hono). Connections and Overview; a Browser with `where` filter, paging, a JSON tree editor (save as `$set`/`$unset` guarded by `_v`), insert and delete; Dev Tools to send raw requests; a read-only Watch tail; and Backups (download over the unix socket, restore command). The Dev Tools editor auto-indents, closes pairs, and indents with Tab; a hover button formats the JSON body. Release binaries for linux and darwin (`ndb-ui-<tag>-<os>-<arch>.tar.gz` with `ndb-ui-checksums.txt`) and the image `ghcr.io/thadeu/ndb-ui`. `install.sh --ui` installs it. See [UI](docs/dev/ui.md).

### Fixed

- Docs: Docker has a Compose section: a dev quick start with no config and no volume (`volumes_from`), the anonymous-volume warning, a production file, and TCP inside the Compose network.

## [0.2.2] - 2026-11-06

### Changed

- Defaults that make the image need no `command:`: without `-config`, the daemon and the CLI read `NDB_CONFIG`, then `./ndb.yml`, then `/etc/ndb/ndb.yml`. The default unix socket is `/run/ndb/ndb.sock` (was `/run/ndb.sock`), with mode `0660` (was the umask). `listen.unix_mode` sets the mode in `ndb.yml`. The image has no `CMD`; mount `ndb.yml` at `/etc/ndb/ndb.yml`.

## [0.2.1] - 2026-11-06

### Added

- CLI `ndb health`: exit 0 and print `ok` when the daemon is ready. Use it as the container health check (the image has no shell or curl).

### Fixed

- Docs: the Go install line uses `@latest`; the Go guide shows `Where().All()` for every document; the backup example shows version 0.2.0.

## [0.2.0] - 2026-11-06

### Changed

- **Breaking: `_id` is the primary key of every collection.** The `id` key of a definition (`ndb.yml`, `PUT /v1/c/{coll}`) is removed and is now `400 catalog_invalid`; declare a natural key with `unique: [field]`. In engine mode an insert without `_id` gets a monotonic UUIDv7 (text order = creation order); an insert with `_id` keeps any non-empty string. `_id` is always in the document.
- **Breaking: direct mode.** A line with `_id` is a document with that id, and later lines with the same `_id` are its next versions. A line without `_id` is a document without `_id`: `find`, `aggregate` and `watch` see it, the `/{_id}` routes do not.
- **Breaking:** `GET /v1/c/{coll}/ttl/{id}` is now `GET /v1/c/{coll}/{_id}/ttl`. The bodies of `expire` and `persist` take `_id`.
- An insert or upsert over an expired document that the sweeper has not deleted yet replaces it, instead of `409 duplicate_id`.
- **Breaking: backups are one `.ndb` file.** `ndb backup <dest>` writes NDJSON (header, catalog, watch cursors, WAL lines with their CRC, a snapshot when `wal_retention` removed early WAL files, blobs, an end line). `-gzip` writes `.ndb.gz`, `-` writes to stdout, `-no-blobs` leaves blobs out. `ndb restore` takes a file or stdin, finds gzip by its first bytes, and checks every line before it touches the data dir. The old backup folder is gone. `ndb backup` takes `-url unix://...`. `POST /v1/backup` streams the file.
- A unix socket path over the OS limit (about 104 bytes on macOS, 108 on Linux) gives a clear error.
- **Breaking:** every answer names the document id `_id`: insert `{"results":[{"_id","v"}]}`, `expire`, `persist`, `ttl`, and `watch` events (`{"pos","op","_id","v","doc"}`).
- SDKs: the id option of the schemas is removed. Go `ndb:"_id"`, `Get`, `UpdateID` (with `Guard`), `DeleteID`; TypeScript `get(_id)` (was `find(id)`), and `update`/`delete` take a string `_id` or a filter; Ruby `get`, and `update`/`delete` take a String `_id`; Rails `primary_key` is always `_id` and `record.id` is its alias.

### Added

- `GET /v1/c/{coll}/{_id}`, `POST /v1/c/{coll}/{_id}/update`, `DELETE /v1/c/{coll}/{_id}`. A fixed route (`find`, `stats`, ...) wins over an `_id` of the same name.
- CLI `ndb get <coll> <_id>`, and `ndb update <coll> <_id> '<ops>'` / `ndb delete <coll> <_id>`.
- `$setOnInsert`: set fields only when an upsert creates the document; a match is never overwritten.
- ndb-rails: dynamic finders `find_by_<attr>` / `find_by_<a>_and_<b>` (and `!`), `find_or_initialize_by`, atomic `find_or_create_by` / `find_or_create_by!` (one document even under races, through upsert and `$setOnInsert`), `destroy!` (`Ndb::RecordNotDestroyed`), `record.delete`, `Model.destroy(ids)`, `Model.delete(ids)`, `destroy_all`, `delete_all` (`.delete` is its alias).
- `ndb.NewID()` (Go), `Ndb.new_id` (Ruby), `newId()` (TypeScript): the same UUIDv7, for apps that write direct-mode files.
- Docs: one page per client (Go, Ruby, Rails, TypeScript, CLI) under Clients.

### Removed

- The `examples` job in CI. Run `cd examples && make check` locally.

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

[Unreleased]: https://github.com/thadeu/ndb/compare/v0.4.0...HEAD
[0.4.0]: https://github.com/thadeu/ndb/compare/v0.3.3...v0.4.0
[0.3.3]: https://github.com/thadeu/ndb/compare/v0.3.2...v0.3.3
[0.3.2]: https://github.com/thadeu/ndb/compare/v0.3.1...v0.3.2
[0.3.1]: https://github.com/thadeu/ndb/compare/v0.3.0...v0.3.1
[0.3.0]: https://github.com/thadeu/ndb/compare/v0.2.2...v0.3.0
[0.2.2]: https://github.com/thadeu/ndb/compare/v0.2.1...v0.2.2
[0.2.1]: https://github.com/thadeu/ndb/compare/v0.2.0...v0.2.1
[0.2.0]: https://github.com/thadeu/ndb/compare/v0.1.2...v0.2.0
[0.1.2]: https://github.com/thadeu/ndb/compare/v0.1.1...v0.1.2
[0.1.1]: https://github.com/thadeu/ndb/compare/v0.1.0...v0.1.1
[0.1.0]: https://github.com/thadeu/ndb/releases/tag/v0.1.0
