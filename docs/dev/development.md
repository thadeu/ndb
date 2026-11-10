# Development

## Commands

```bash
make test        # go test ./...
make race        # with the race detector
make lint        # golangci-lint (wsl enforces blank lines)
make build       # static binary: bin/ndb
make dist        # tar.gz + checksums.txt in dist/ (linux/amd64, linux/arm64, darwin/arm64)
test/smoke.sh    # builds ndb and runs every endpoint with curl
```

Keep local test runs light:

```bash
nice -n 10 go test -p 2 -race -parallel=2 -timeout 300s ./...
```

`make fuzz-smoke` runs each fuzz target for 10 s with two workers.

**CI and local runs are not the same.** CI (`.github/workflows/ci.yml`) runs one plain pass, `go test -short ./...`, with no race detector, to check that everything passes at a low cost. The heavy tests skip with `-short`: the kill and crash loops (`TestKillDuringIngestionLosesNothing`, `TestSealingKillsLoseNoAckedRow`, `TestDaemonRecoversAfterSIGKILL`, ...), the engine comparisons and the 257-collection pattern test. Run them on your machine before a release: `make test`, `make race`, `make bench`. A new test that kills processes, loops many rounds or takes more than a few seconds starts with:

```go
if testing.Short() {
	t.Skip("heavy: it runs without -short (make test), not in CI")
}
```

SDK tests:

```bash
(cd sdk/go && go test -race ./...)          # starts the daemon it needs
(cd sdk/ruby && bundle install && bundle exec rake test)   # gem ndb
(cd sdk/rails && bundle install && bundle exec rake test)  # gem ndb-rails
(cd sdk/ts && pnpm install && pnpm test && pnpm lint && pnpm typecheck && pnpm build)
```

## Code layout

Where the code lives, for contributors.

| Package | Job |
|---|---|
| `cmd/ndb` | flags, listeners, graceful stop, `ndb backup` |
| `internal/server` | HTTP routes, error mapping, metrics, `watch` stream |
| `internal/engine` | opens the data dir, owns the collections, blobs and backup |
| `internal/catalog` | collection definitions in `catalog.bolt`; merge of code, YAML and implicit declarations |
| `internal/config` | loads the optional `ndb.yml` |
| `internal/collection` | one collection: writes, reads, indexes, sealing, compaction, TTL |
| `internal/writer`, `internal/wal` | engine-mode WAL with CRC and group commit |
| `internal/tailer` | direct mode: follows the source files, keeps a cursor per file, quarantines bad lines |
| `internal/query`, `internal/agg` | parse and run `where`, `sort` and aggregate pipelines |
| `internal/segment` | Parquet segments |
| `internal/index` | `_id` and secondary indexes |
| `internal/feed`, `internal/cursors` | `watch` change feed and consumer positions |
| `internal/blob` | content-addressed blob store |
| `internal/recovery`, `internal/lock` | start-up recovery, one daemon per data dir (`flock`) |

## Docs site

The site in `docs-site/` is built from `docs/dev/*.md` (the only source; no copies). It uses VitePress with a custom theme and the tokens of `brand/`.

```bash
cd docs-site
pnpm install
pnpm docs:dev      # live reload
pnpm docs:build    # static site in docs-site/.vitepress/dist
```

A push to `main` that changes `docs/`, `docs-site/` or `brand/` deploys it to Cloudflare Pages with wrangler (`.github/workflows/docs.yml`, project `ndb-docs`). Links in the docs that point outside `docs/dev/` become GitHub links in the site.

## Rules

- Test first. Each change starts with a failing test.
- `gofmt` and `GOOS=linux go vet ./...` must be clean.
- Code style: a blank line before `if`, `for`, `switch`, `return`, `defer`, `go`, after a closing `}`, and between groups that do different things.
- Docs in `docs/dev/` change with the behavior.
- A change to storage, the WAL, memory, or the read and write paths runs `make bench` before it is merged, and every check must PASS. A FAIL is fixed, or its budget in `internal/bench/checks.go` is changed in the same commit, with the reason.
- `examples/` is not run in CI. Check it locally with `cd examples && make check` when an API changes.

## Release

`make tag patch|minor|major` pushes the release commit, waits for its CI run and then pushes the tag `vX.Y.Z`. The full checklist is in [releasing.md](releasing.md). The tests run only in CI.

- `release.yml` builds the static binaries with the version in `-ldflags "-X main.version=..."`, makes tar.gz files and `checksums.txt`, and creates the GitHub release with generated notes.
- `release.yml` also builds the console binaries and pushes the tags of the Go modules, `sdk/go/vX.Y.Z` and `sdk/go/ndbredis/vX.Y.Z`.
- `publish.yml` publishes the SDK packages at the same time: the gems `ndb`, then `ndb-rails` (job `gems`), and `@thadeu/ndb` (job `npm`).
- `ghcr.yml` builds and pushes the images at the same time: `ghcr.io/<owner>/ndb` (multi-arch) and `ghcr.io/<owner>/ndb-ui`.

`ndb -version` prints the version. Locally: `make dist VERSION=0.1.0`.

GoReleaser is not used: it would do the same job as `release.yml` (build matrix, archives, checksums, release), and the workflow is the same shape as the one in the sibling project `anway`.
