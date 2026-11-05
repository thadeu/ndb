# M10 — Ruby client (gem `ndb-rails`)

Status: done (light verification) · Depends on: M07 (blob attribute waits for M12) · Plan: §12.2

## Goal

`Ndb::Record` on ActiveModel, so a Rails app uses `Post.create`,
`Post.where`, `Post.group(...).count` with no config on the daemon side.
No query semantics in the gem: it only builds JSON.

## Epics

### E10.1 Transport

- [ ] Tests first: against a real daemon started by the test suite
- [ ] HTTP on a unix socket. Note: `Net::HTTP` has no native unix socket support; choose between `httpx` (supports unix sockets) or a small `Socket`-based adapter
- [ ] Keep-alive / connection pool (thread-safe for Puma)
- [ ] NDJSON stream reader; errors mapped to `Ndb::*Error`

### E10.2 Base and attributes

- [ ] Tests first: naming (`Post` → `posts`), `collection_name=`, `abstract_class`, `primary_key`
- [ ] `Ndb::Record` with `ActiveModel::Model`, `Attributes`, `Validations`, `Callbacks`, `Dirty`, `Serializers::JSON`
- [ ] `attribute :x, :type, index: true`
- [ ] `source "<glob>"` for direct-mode collections

### E10.3 Relation

- [ ] Tests first: the Ruby → JSON table in §12.2, one test per row
- [ ] Lazy `Ndb::Relation`: `where`, `where.not`, `or`, `order`, `limit`, `offset`
- [ ] Loaders: `each`, `to_a`, `first`, `find`, `find_by`, `count`, `pluck`, `exists?`
- [ ] Pagination: `page(n).per(size)`, `after(token)` + `next_cursor`, `find_each`, `in_batches`
- [ ] Aggregates: `group(...).count`, `sum`, `average`, `minimum`, `maximum`

### E10.4 Persistence

- [ ] Tests first: dirty fields → `$set`; `if_version` → `Ndb::StaleObjectError`; validation failure stops the write
- [ ] `create`, `create!`, `save`, `update`, `update!`, `destroy`
- [ ] Callbacks: `before/after_save`, `before/after_create`, `before/after_destroy`

### E10.5 Railtie

- [ ] `config/ndb.yml` per environment (socket path, timeouts) — connection only, no collections
- [ ] `ensure_collection` for each model on boot (eager load in production; first use in development)

### E10.6 Blob attribute (after M12)

- [ ] `attribute :recording, :blob` returns an IO-like object; upload on save

### E10.7 Shared suite

- [ ] Runner (Minitest or RSpec) for all `spec/cases/`

## Exit criteria

- [ ] All `spec/cases/` pass through the gem
- [ ] Sample Rails app: `Post.create!`, `Post.where(...).order(...).limit`, `Post.group(:status).count`, `find_each`, works with `form_with`
- [ ] Thread-safety test under Puma-like concurrency

## Out of scope

- Associations (see [backlog/relationships](../backlog/relationships.md)). Migrations.
