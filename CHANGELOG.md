# Changelog

All notable changes to this project are documented in this file.

The format follows [Keep a Changelog](https://keepachangelog.com/en/1.1.0/), and this project follows [Semantic Versioning](https://semver.org/).

## [Unreleased]

### Added

- `$regex` and `$options` (`i`, `m`, `s`, `x`) in `where`: RE2 syntax, works on array fields, dot paths and inside `$not`, `$and`, `$or`. A literal `^prefix` without `i` or `m` uses the index and the zone maps.
- `$regex` support in the Go and TypeScript SDKs.
- Ruby gems: a `Regexp` is a valid `where` value (`ndb`, and `where`, `where.not`, `or` in `ndb-rails`). It always sends `m` (Ruby line anchors), maps `/i` to `i`, `/m` to `s`, `/x` to `x`, and rejects look-around, backreferences, atomic groups, possessive quantifiers, `\h`, `\G`, `\R` and `\X` with `Ndb::InvalidQuery` before the request.

## [0.1.0] - Unreleased

First release.

### Added

- `ndb` daemon: one static binary, one data directory, HTTP API over a unix socket or TCP.
- Durable WAL writes, columnar segments, and a column-scan planner with pruning.
- Two write modes: direct (apps append NDJSON files, ndb follows them) and engine (`insert`, `update`, `delete`, optimistic `if_version`).
- Mongo-style `find` with comparison and logical operators, sort, offset and cursor pagination.
- `aggregate` with `$match $group $sort $limit $skip $project` and `$dateTrunc`.
- `watch` and `ack` for outbox-style consumers, with `where` filters and WAL truncation (`wal_retention`).
- Secondary and unique indexes built in the background.
- TTL per document, retention, compaction, and a blob store for large values.
- Quarantine for bad lines in direct mode, online backup, Prometheus `/metrics`.
- Optional bearer token on the TCP listener (`-token-file`).
- Go SDK (`github.com/thadeu/ndb/sdk/go`, tag `sdk/go/v0.1.0`), Ruby SDK in two gems (`ndb`, a plain client with no runtime dependency, and `ndb-rails`, the ActiveModel layer that depends on it) and TypeScript SDK (`@thadeu/ndb`), all MIT.
- Release tooling: static binaries for Linux and macOS, multi-arch image at `ghcr.io/thadeu/ndb`.
- Docs in `docs/dev/`, a docs site, and deploy examples (single VM, cross-VM with WireGuard).

[Unreleased]: https://github.com/thadeu/ndb/compare/v0.1.0...HEAD
[0.1.0]: https://github.com/thadeu/ndb/releases/tag/v0.1.0
