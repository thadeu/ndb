# M30 — One URL: token and database

Status: in progress (E30.1 and E30.2 done) · Depends on: nothing · Docs: `docs/dev/configuration.md`, `docs/dev/http.md`, `docs/dev/clients/`, `docs-site/llms-agents.md`

## Goal

`NDB_URL` holds all that a client needs, as in Redis and Postgres URLs:

```
tcp://:<token>@ndb-0.runa.voodu:7070/1billion
unix:///run/ndb/ndb.sock/1billion        (see "unix path" below)
https://:<token>@ndb.example.com/1billion
```

- **Token** in the userinfo (the password part; the user part is ignored
  for now, as Redis does with `default`).
- **Database** in the path: a named set of collections in one daemon. Two
  databases can each have a `users` collection.

This is also the base of the replication design
(`../backlog/replication.md`): a copy of another daemon goes in its own
database, with the names of its collections unchanged.

## Compatibility (rule of this milestone)

1billion runs on ndb as its primary database. Nothing it does today may
change.

- An URL with no path is the `default` database. Every URL of today is
  valid and means the same.
- The `default` database is the data dir of today, with no move of files.
  A new database is a sub dir. A start on an existing data dir changes
  nothing on disk.
- `NDB_TOKEN` still works. Order: the `token` option of the SDK, then the
  token of the URL, then `NDB_TOKEN`.
- Every route and key of today stays. The database is a new header, not a
  new path, so no route changes (HTTP API 1.x: add, do not rename).
- `NDB_PREFIX` (Go SDK) stays as it is.
- Test: a data dir made by 1.26 starts with the new binary and answers the
  same (golden test), and the SDKs of 1.26 work against the new daemon.

## Design

**Header.** The SDK sends `Ndb-Database: <name>` when the URL has a path.
No header is `default`. The daemon routes every collection route, stream
route, `watch` and `_tx` to the collections of that database.

**Names.** `[a-z0-9][a-z0-9_-]{0,62}`. `default` is reserved. An unknown
database: a write makes it (as a write makes a collection today), a read
answers `404 database_not_found`.

**On disk.** `<data_dir>/` is `default` (as today).
`<data_dir>/_databases/<name>/` holds the others, each a data dir of its own
(catalog, cursors, blobs, collections). Each collection already has its own
WAL, so a database adds no WAL. Built (E30.2): a database is an engine of
its own, opened by the default one at start; it shares the lock, the
settings, the encryption key and the write budget. Docs:
`docs/dev/databases.md`.

**Admin.** `GET /_databases`, `DELETE /_databases/{name}` (with
`?confirm=<name>`), in `_stats` and in the console.

**unix path.** `unix:///run/ndb/ndb.sock/1billion` is ambiguous (the path
is a file). Use a query: `unix:///run/ndb/ndb.sock?db=1billion`. The
query `?db=` is also accepted on tcp and https.

**Secrets.** The SDKs never print the token: not in errors, logs,
`inspect`, `toString` or the User-Agent.

## Epics

### E30.1 — Token in the URL (SDKs only)

- [x] Golden compatibility test, first: a data dir made by the last release
      (fixture in the repo) opens with the new binary and answers the same;
      it runs in CI on each commit (see the rule in `README.md`).
- [x] Shared cases in `spec/url/cases.json` for the parse: userinfo, no userinfo,
      percent-encoded token, the order option > URL > env, a bad URL.
- [x] Go, Ruby, Rails, TS, CLI (`-url`, also `https://`), console (`NDB_URL`): read the token from the URL.
- [x] Tests that the token is not in an error message or `inspect`.
- [x] Docs: the URL format on one page (`docs/dev/configuration.md`),
      linked from each client page.

### E30.2 — Databases in the daemon

- [x] Tests first: two databases with a `users` collection each; no header
      is `default`; a 1.26 data dir starts and answers the same (golden).
- [x] Engine: a database is a set of collections with its own catalog and
      dir; `default` is the root of today. Key rotation reaches every
      database; a broken database stops the start.
- [x] Server: the `Ndb-Database` header on every collection route;
      `/_databases` routes; `invalid_database` and `database_not_found` in
      `docs/dev/http/errors.md`; a streams service per database; scoped
      tokens stay in the default database; logs name the database.
- [x] Backup takes one database (header) or all (format 2; a daemon with no
      database still writes format 1). Restore puts every database back.
      PITR works per database. A restore of one database into a running
      daemon: later.
- [x] Limits and memory: the write budget and `memory_limit` are for the
      daemon (all databases).

### E30.3 — Databases in the SDKs and the console

- [ ] Shared cases in `spec/cases`: the same collection name in two
      databases.
- [ ] Go, Ruby, Rails, TS send the header from the URL path or `?db=`.
- [ ] CLI: `ndb query`, `stats`, `backup` take the database of the URL.
- [ ] Console: a database selector; a connection URL with a database.

## Exit criteria

- Every URL of today works and means the same; 1billion runs with the new
  daemon and the new gems with no change.
- `tcp://:<token>@host:7070/name` works in every SDK and the CLI.
- Two databases with the same collection names, isolated, in one daemon.
- `make bench`: every check PASSES.
- Docs and `docs-site/llms-agents.md` show the URL format.

## Out of scope

- Users and a role per database (the user part of the URL stays ignored).
- Replication (`../backlog/replication.md`).
