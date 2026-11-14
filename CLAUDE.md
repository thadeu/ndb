# ndb — guide for AI agents

Read this file first. It tells you what ndb is, where the code lives, how to test it, and the
rules of this repo. The full docs are in `docs/dev/` (the source of https://ndb.clowk.in), and
`docs-site/llms-agents.md` is the short version for agents that *use* ndb.

## What ndb is

One Go daemon (`ndb`) that stores NDJSON and answers HTTP/JSON:

- **Elasticsearch routes:** `POST /{c}/_search`, `_count`, `_bulk`, `_aggregate`,
  `_update_by_query`, `_delete_by_query`, index patterns (`logs-2026-*`), date math (`now-7d/d`).
- **MongoDB filters and updates:** `$in $gte $regex $elemMatch $and $or`, `$set $inc $push`,
  aggregate `$match $group $sort $project $dateTrunc`.
- **Outbox and queues:** `watch` + `ack` (change feed with consumer positions) and Redis-style
  **streams** (`/_streams`: groups, PEL, nack with delayed retry, claim, dead letter `<stream>_dead`).
- One node, one data directory. No cluster, no replication, no SQL, no joins.

Two write modes:

- **Engine mode:** SDKs send writes. The writer group-commits them to the WAL (NDJSON + CRC32-C,
  `fsync`) before the answer (`durability: strict`, the default; `relaxed` = 200 ms window).
- **Direct mode:** the app appends to its own `.ndjson` files; the tailer follows them.

The data path is WAL or source files → memtable + indexes → Parquet segments (sealed by size or
age). The NDJSON is the truth; segments, indexes (`.ids` with a bloom filter) and checkpoints are
derived and can be rebuilt. Delivery to consumers is at least once.

Listeners: a unix socket (no token) and optional TCP (`127.0.0.1:7070` by default, bearer token
with `-token-file`). Security defaults are on: Origin check, Host check when there is no token,
token lockout. See `docs/dev/security.md`.

## Repo map

| Path | What |
|---|---|
| `cmd/ndb` | the binary: `ndb serve`, flags, listeners, and the CLI (`stats`, `query`, `backup`, ...) |
| `cmd/ndbgen` | deterministic NDJSON test data generator |
| `internal/server` | HTTP routes, errors, guards (`guard.go`, `limit.go`, `conns.go`), logs, metrics |
| `internal/engine` | opens the data dir, owns collections, blobs, backup |
| `internal/collection` | one collection: writes, reads, indexes, sealing, compaction, TTL |
| `internal/writer`, `internal/wal` | engine-mode WAL, group commit |
| `internal/tailer` | direct mode, quarantine of bad lines |
| `internal/query`, `internal/agg` | filters, sort, aggregate pipeline |
| `internal/segment`, `internal/index` | Parquet segments, `_id` and secondary indexes |
| `internal/feed`, `internal/cursors` | `watch` change feed, consumer positions |
| `internal/streams` | Redis-style streams (ids, groups, PEL, nack/retry) |
| `internal/catalog`, `internal/config` | collection definitions (`catalog.bolt`), `ndb.yml` |
| `internal/recovery`, `internal/lock` | start-up recovery, one daemon per data dir |
| `internal/logging` | leveled async logs (debug, info, warn, error, quiet) |
| `internal/bench` | `make bench` scenarios and their PASS/FAIL budgets (`checks.go`) |
| `internal/spec` + `spec/` | **shared conformance cases** (`spec/cases`, `spec/streams`) run by the daemon and every SDK |
| `sdk/go` | Go client (`sdk/go/ndbredis`: Redis-style stream commands; own `go.mod`) |
| `sdk/ruby` | gem `ndb` (plain client) |
| `sdk/rails` | gem `ndb-rails` (models, `Ndb::Streams` consumers, `bundle exec ndb streams`) |
| `sdk/ts` | npm `@thadeu/ndb` (also `@thadeu/ndb/ioredis` and `@thadeu/ndb/node-redis`) |
| `console/` | `ndb-ui`, the web console (Bun + Hono JSX). It has its own `console/CLAUDE.md` |
| `docs/dev/` | all docs; `docs/dev/http.md` + `docs/dev/http/` is the API contract |
| `docs-site/` | VitePress site built from `docs/dev/` (no copies) |
| `examples/` | runnable examples (not in CI; `cd examples && make check`) |
| `deploy/` | Docker (`ndb`, `ndb-ui`, `stack`), systemd, compose examples |
| `test/` | `smoke.sh` (every endpoint with curl), golden files, PITR e2e |
| `.plans/` | milestone index, backlog, playbooks (done plans are removed; see git history) |

## Run and test

```bash
make build                      # bin/ndb
bin/ndb serve                   # data in ~/.ndb, HTTP on 127.0.0.1:7070
curl localhost:7070/_health

go test -short ./...            # what CI runs (cheap, no race)
nice -n 10 go test -p 2 -race -parallel=2 -timeout 300s ./...   # local, light
make test / make race / make lint / make bench                  # before a release
test/smoke.sh

(cd sdk/go && go test -race ./...)
(cd sdk/ruby && bundle exec rake test)
(cd sdk/rails && bundle exec rake test)
(cd sdk/ts && pnpm test && pnpm lint && pnpm typecheck)
```

- Heavy tests (kill loops, crash loops, long comparisons) start with `if testing.Short() { t.Skip(...) }`.
- Tests that need a daemon start their own. Never use a Redis on port 6379 for tests that
  flush data: it may be the maintainer's own Redis.
- No `make bench` unless asked, or unless the change touches storage, the WAL, memory or the
  read/write paths (then every check must PASS).

## Rules for changes

- **Test first.** Write the failing test, then the code.
- **The daemon owns meaning.** SDKs only build JSON. A new operator or route goes in the daemon,
  then in the docs, then in every SDK (Go, Ruby, Rails when it fits, TS) with tests.
- **Same answer everywhere.** A behavior shared by all clients gets a case in `spec/cases` or
  `spec/streams`, so the daemon and every SDK run it.
- **Docs change with behavior** in the same commit: `docs/dev/*.md`, the HTTP pages in
  `docs/dev/http/`, the client pages in `docs/dev/clients/`, and `docs-site/llms-agents.md`.
- **HTTP API is stable (1.x):** routes and keys change only in a major release. Add, do not rename.
- **Format:** `gofmt -l .` prints nothing; `GOOS=linux go vet ./...` is clean; `make lint` (wsl).
- **Blank lines (enforced by `wsl`):** one blank line before `if`, `for`, `switch`, `return`,
  `defer`, `go` (also before `if err != nil`), after a closing `}`, and between groups of lines
  that do different jobs. Never a wall of lines.
- **Simple:** the minimum that solves the problem. No speculative options.
- **Docs style:** short sentences, simple words (ASD-STE100 style), as in the existing pages.
- **User-Agent:** each client sends its own (`ndb-go/…`, `ndb-ruby/…`, `ndb-rails/…`, `ndb-ts/…`,
  `ndb-cli/…`). Keep it when you add a transport.

## Git, CI and releases

- **Conventional Commits** for every commit and PR title: `type(scope): summary`, lower case
  type, scope optional, summary in the imperative, no period at the end.
  - Types: `feat`, `fix`, `docs`, `refactor`, `perf`, `test`, `build`, `ci`, `chore`, `release`.
  - Scopes: the part you changed, for example `server`, `query`, `streams`, `wal`, `cli`,
    `sdk-go`, `sdk-ruby`, `rails`, `sdk-ts`, `console`, `docs-site`, `deploy`.
  - Examples: `feat(streams): add claim due`, `fix(server): refuse a cross-site write`,
    `docs: link the docs site in the README`, `ci: run the tests once`.
  - A breaking change of the HTTP API adds `!` (`feat(server)!: ...`) and a `BREAKING CHANGE:`
    line in the body.
  - `tag(patch|minor|major): summary` ships a release once CI passes on `main` (see below). Use
    it only when the maintainer asks for a release.
  - `release: X.Y.Z` is made only by `scripts/release.sh` (`make tag` or the tag workflow). CI
    matches the prefix `release: ` to keep a release run from being cancelled, so do not use it
    for anything else.
- Fixes and small changes go directly on `main`. Larger features use a branch and a PR; the
  maintainer merges with squash.
- **Do not watch CI** (no `gh run watch`, no polling): the maintainer follows it.
- **Do not create tags by hand.** A release starts in one of two ways, both through
  `scripts/release.sh` (it bumps every version, also `sdk/go/ndbredis/go.mod`, and pushes `main`
  and the tag):
  - a commit on `main` whose subject starts with `tag(patch|minor|major):`. When CI passes,
    `.github/workflows/tag.yml` runs the script and then the release jobs (release, ghcr,
    publish, docs) for the new tag;
  - the maintainer runs `make tag patch|minor` on a machine.
- CI (`.github/workflows/ci.yml`) is the only place with tests. On a `v*` tag, its `tag` job
  calls `release.yml` (binaries, GitHub Release, Go tags), `ghcr.yml` (images `ndb`, `ndb-ui`,
  `ndb-stack`) and `publish.yml` (gems, npm). `docs.yml` deploys the site when docs change.
- Keep CI cheap: one `go test -short` pass, no `-race`, no macOS runners.

### No Claude attribution

These rules apply to every session: local, web, desktop and the iOS app.

- Never credit Claude in a commit or a PR. Do not add a `Co-Authored-By: Claude` trailer,
  a "Generated with Claude Code" line, or a session link. `.claude/settings.json` sets
  `attribution` to empty texts and `sessionUrl: false`.
- The commit author is always the maintainer (`thadeu`), never `Claude <noreply@anthropic.com>`.
  Before the first commit of a session, run `git config user.name` and `git config user.email`.
  If the name is `Claude` or the email is `noreply@anthropic.com`, set both to the
  identity of the maintainer's own commits (`git log --author=thadeu -1 --format='%an %ae'`).
- Do not rewrite old history to remove past credits.

## Where to read more

- `docs/dev/architecture.md`: components, data flow, guarantees.
- `docs/dev/http.md`: the HTTP contract; `docs/dev/http/errors.md` for error codes.
- `docs/dev/query.md`, `aggregate.md`, `watch.md`, `streams.md`: the query language and consumers.
- `docs/dev/storage.md`, `wal.md`, `pitr.md`: on-disk layout, durability, recovery.
- `docs/dev/configuration.md`: `ndb.yml`, flags, durability, limits.
- `docs/dev/development.md`, `releasing.md`: tests, docs site, release steps.
